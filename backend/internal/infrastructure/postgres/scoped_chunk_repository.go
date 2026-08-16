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
