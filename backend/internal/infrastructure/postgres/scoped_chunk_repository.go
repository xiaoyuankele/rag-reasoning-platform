package postgres

import (
	"context"
	"errors"
	"fmt"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	accessdomain "rag-reasoning-platform/backend/internal/domain/access"
	documentdomain "rag-reasoning-platform/backend/internal/domain/document"
)

// ScopedChunkRepository 为已认证用户分页读取自己文档的文本块。
// 文本块写入仍由系统 Worker 使用 ChunkRepository 完成。
type ScopedChunkRepository struct {
	pool *pgxpool.Pool
}

var _ documentdomain.ScopedChunkPageLister = (*ScopedChunkRepository)(nil)
var _ documentdomain.ScopedChunkSearcher = (*ScopedChunkRepository)(nil)

// NewScopedChunkRepository 创建带文档所有者边界的文本块读取仓储。
func NewScopedChunkRepository(pool *pgxpool.Pool) *ScopedChunkRepository {
	return &ScopedChunkRepository{pool: pool}
}

// ListPageByDocumentID 只统计并返回当前 OwnerScope 内指定文档的文本块。
func (r *ScopedChunkRepository) ListPageByDocumentID(
	ctx context.Context,
	scope accessdomain.OwnerScope,
	documentID int64,
	options documentdomain.ChunkPageOptions,
) (documentdomain.ChunkPageResult, error) {
	if !scope.IsValid() {
		return documentdomain.ChunkPageResult{}, accessdomain.ErrInvalidOwnerScope
	}

	// LEFT JOIN 保留“文档存在但还没有 chunks”的 total=0 语义。
	const countQuery = `
		SELECT COUNT(chunk.id)
		FROM documents AS source_document
		LEFT JOIN text_chunks AS chunk
		  ON chunk.document_id = source_document.id
		WHERE source_document.id = $1
		  AND source_document.owner_user_id = $2
		GROUP BY source_document.id
	`

	var total int64
	err := r.pool.QueryRow(
		ctx,
		countQuery,
		documentID,
		scope.OwnerUserID(),
	).Scan(&total)
	if errors.Is(err, pgx.ErrNoRows) {
		return documentdomain.ChunkPageResult{}, documentdomain.ErrNotFound
	}
	if err != nil {
		return documentdomain.ChunkPageResult{}, fmt.Errorf(
			"count scoped document chunks: %w",
			err,
		)
	}

	const listQuery = `
		SELECT
			chunk.id,
			chunk.document_id,
			chunk.chunk_index,
			chunk.content,
			chunk.page_start,
			chunk.page_end,
			chunk.created_at
		FROM text_chunks AS chunk
		JOIN documents AS source_document
		  ON source_document.id = chunk.document_id
		WHERE source_document.id = $1
		  AND source_document.owner_user_id = $2
		ORDER BY chunk.chunk_index ASC
		LIMIT $3
		OFFSET $4
	`

	rows, err := r.pool.Query(
		ctx,
		listQuery,
		documentID,
		scope.OwnerUserID(),
		options.Limit,
		options.Offset,
	)
	if err != nil {
		return documentdomain.ChunkPageResult{}, fmt.Errorf(
			"query scoped document chunk page: %w",
			err,
		)
	}
	defer rows.Close()

	chunks := make([]documentdomain.TextChunk, 0)
	for rows.Next() {
		chunk, err := scanTextChunk(rows)
		if err != nil {
			return documentdomain.ChunkPageResult{}, fmt.Errorf(
				"scan scoped document chunk page: %w",
				err,
			)
		}
		chunks = append(chunks, chunk)
	}
	if err := rows.Err(); err != nil {
		return documentdomain.ChunkPageResult{}, fmt.Errorf(
			"iterate scoped document chunk page: %w",
			err,
		)
	}

	return documentdomain.ChunkPageResult{Chunks: chunks, Total: total}, nil
}

// Search 在当前 OwnerScope 的 ready 文档中执行关键词检索。
// count 和 data 两条 SQL 使用完全相同的 owner、状态、关键词和文档过滤条件，
// 避免分页总数与实际结果来自不同的数据边界。
func (r *ScopedChunkRepository) Search(
	ctx context.Context,
	scope accessdomain.OwnerScope,
	options documentdomain.SearchOptions,
) (documentdomain.SearchResult, error) {
	if !scope.IsValid() {
		return documentdomain.SearchResult{}, accessdomain.ErrInvalidOwnerScope
	}

	matchClause, matchArguments, err := keywordMatchClause(options, 3)
	if err != nil {
		return documentdomain.SearchResult{}, fmt.Errorf("build scoped keyword match clause: %w", err)
	}
	documentIDPlaceholder := 3 + len(matchArguments)

	countQuery := fmt.Sprintf(`
		SELECT COUNT(*)
		FROM text_chunks AS chunk
		JOIN documents AS source_document
		  ON source_document.id = chunk.document_id
		WHERE source_document.owner_user_id = $1
		  AND source_document.status = $2
		  AND %s
		  AND ($%d::BIGINT IS NULL OR chunk.document_id = $%d)
	`, matchClause, documentIDPlaceholder, documentIDPlaceholder)
	countArguments := []any{scope.OwnerUserID(), documentdomain.StatusReady}
	countArguments = append(countArguments, matchArguments...)
	countArguments = append(countArguments, options.DocumentID)

	var total int64
	if err := r.pool.QueryRow(
		ctx,
		countQuery,
		countArguments...,
	).Scan(&total); err != nil {
		return documentdomain.SearchResult{}, fmt.Errorf(
			"count scoped matching document chunks: %w",
			err,
		)
	}

	hits := make([]documentdomain.SearchHit, 0)
	if total == 0 {
		return documentdomain.SearchResult{Hits: hits, Total: 0}, nil
	}

	limitPlaceholder := documentIDPlaceholder + 1
	offsetPlaceholder := documentIDPlaceholder + 2
	searchQuery := fmt.Sprintf(`
		SELECT
			chunk.id,
			chunk.document_id,
			chunk.chunk_index,
			source_document.title,
			source_document.original_name,
			source_document.mime_type,
			chunk.content,
			chunk.page_start,
			chunk.page_end
		FROM text_chunks AS chunk
		JOIN documents AS source_document
		  ON source_document.id = chunk.document_id
		WHERE source_document.owner_user_id = $1
		  AND source_document.status = $2
		  AND %s
		  AND ($%d::BIGINT IS NULL OR chunk.document_id = $%d)
		ORDER BY
			source_document.created_at DESC,
			source_document.id DESC,
			chunk.chunk_index ASC,
			chunk.id ASC
		LIMIT $%d
		OFFSET $%d
	`, matchClause, documentIDPlaceholder, documentIDPlaceholder, limitPlaceholder, offsetPlaceholder)
	searchArguments := append([]any(nil), countArguments...)
	searchArguments = append(searchArguments, options.Limit, options.Offset)

	rows, err := r.pool.Query(
		ctx,
		searchQuery,
		searchArguments...,
	)
	if err != nil {
		return documentdomain.SearchResult{}, fmt.Errorf(
			"query scoped matching document chunks: %w",
			err,
		)
	}
	defer rows.Close()

	for rows.Next() {
		hit, err := scanSearchHit(rows)
		if err != nil {
			return documentdomain.SearchResult{}, fmt.Errorf(
				"scan scoped matching document chunk: %w",
				err,
			)
		}
		hits = append(hits, hit)
	}
	if err := rows.Err(); err != nil {
		return documentdomain.SearchResult{}, fmt.Errorf(
			"iterate scoped matching document chunks: %w",
			err,
		)
	}

	return documentdomain.SearchResult{Hits: hits, Total: total}, nil
}
