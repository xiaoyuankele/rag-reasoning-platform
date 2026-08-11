package postgres

import (
	"context"
	"errors"
	"fmt"
	"strings"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"rag-reasoning-platform/backend/internal/domain/document"
)

// ChunkRepository 使用 PostgreSQL 保存统一文本块。
type ChunkRepository struct {
	pool *pgxpool.Pool
}

var _ document.ChunkRepository = (*ChunkRepository)(nil)
var _ document.ChunkSearcher = (*ChunkRepository)(nil)

// NewChunkRepository 创建 PostgreSQL 文本块仓储。
func NewChunkRepository(pool *pgxpool.Pool) *ChunkRepository {
	return &ChunkRepository{
		pool: pool,
	}
}

// ReplaceForDocument 在同一事务中删除旧文本块并写入新文本块。
func (r *ChunkRepository) ReplaceForDocument(
	ctx context.Context,
	documentID int64,
	chunks []document.ChunkInput,
) error {
	for _, chunk := range chunks {
		if !chunk.HasValidPageRange() {
			return fmt.Errorf(
				"%w: chunk index %d",
				document.ErrInvalidChunkPageRange,
				chunk.Index,
			)
		}
	}

	transaction, err := r.pool.Begin(ctx) //开始事务
	if err != nil {
		return fmt.Errorf(
			"begin replace document chunks transaction: %w",
			err,
		)
	}
	defer func() {
		_ = transaction.Rollback(context.Background()) //事务回滚
	}()

	// 锁定文档可以同时完成存在性检查，并防止替换过程中被并发删除。
	var lockedDocumentID int64
	err = transaction.QueryRow(
		ctx,
		`
			SELECT id
			FROM documents
			WHERE id = $1
			FOR UPDATE
		`,
		documentID,
	).Scan(&lockedDocumentID)
	if errors.Is(err, pgx.ErrNoRows) {
		return document.ErrNotFound
	}
	if err != nil {
		return fmt.Errorf(
			"lock document before replacing chunks: %w",
			err,
		)
	}

	if _, err := transaction.Exec( //Exec 执行SQL语句，不返回行数据？
		ctx,
		"DELETE FROM text_chunks WHERE document_id = $1",
		lockedDocumentID,
	); err != nil {
		return fmt.Errorf(
			"delete old document chunks: %w",
			err,
		)
	}

	const insertQuery = `
		INSERT INTO text_chunks (
			document_id,
			chunk_index,
			content,
			page_start,
			page_end
		)
		VALUES ($1, $2, $3, $4, $5)
	`

	for _, chunk := range chunks {
		if _, err := transaction.Exec(
			ctx,
			insertQuery,
			lockedDocumentID,
			chunk.Index,
			chunk.Content,
			chunk.PageStart,
			chunk.PageEnd,
		); err != nil {
			return fmt.Errorf(
				"insert document chunk %d: %w",
				chunk.Index,
				err,
			)
		}
	}

	if err := transaction.Commit(ctx); err != nil {
		return fmt.Errorf(
			"commit replaced document chunks: %w",
			err,
		)
	}

	return nil
}

// ListByDocumentID 按 chunk_index 升序查询一份文档的全部文本块。
func (r *ChunkRepository) ListByDocumentID(
	ctx context.Context,
	documentID int64,
) ([]document.TextChunk, error) {
	// 先检查文档本身是否存在。
	//
	// 不能只查询 text_chunks，因为“查询结果为空”存在两种不同含义：
	// 1. 文档存在，但还没有生成文本块；
	// 2. 文档根本不存在。
	const documentExistsQuery = `
		SELECT id
		FROM documents
		WHERE id = $1
	`

	var existingDocumentID int64
	err := r.pool.QueryRow(
		ctx,
		documentExistsQuery,
		documentID,
	).Scan(&existingDocumentID)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, document.ErrNotFound
	}
	if err != nil {
		return nil, fmt.Errorf(
			"check document before listing chunks: %w",
			err,
		)
	}

	// 查询字段的排列顺序必须和 scanTextChunk 中的 Scan 参数顺序一致。
	// ORDER BY 保证调用者拿到的文本块顺序与原文顺序一致。
	const listChunksQuery = `
		SELECT
			id,
			document_id,
			chunk_index,
			content,
			page_start,
			page_end,
			created_at
		FROM text_chunks
		WHERE document_id = $1
		ORDER BY chunk_index ASC
	`

	rows, err := r.pool.Query(
		ctx,
		listChunksQuery,
		existingDocumentID,
	)
	if err != nil {
		return nil, fmt.Errorf(
			"query document chunks: %w",
			err,
		)
	}
	// defer 把 Close 安排在当前函数返回前执行。
	// 放在查询成功后，可以确保后续任何提前返回都能释放查询资源。
	defer rows.Close()

	// 使用非 nil 空切片，让“文档存在但没有文本块”返回 [] 而不是 null。
	textChunks := make([]document.TextChunk, 0)
	for rows.Next() {
		textChunk, err := scanTextChunk(rows)
		if err != nil {
			return nil, fmt.Errorf(
				"scan listed document chunk: %w",
				err,
			)
		}
		textChunks = append(textChunks, textChunk)
	}

	// Next 返回 false 既可能表示读取结束，也可能表示读取中途出错。
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf(
			"iterate document chunks: %w",
			err,
		)
	}

	return textChunks, nil
}

// Search 跨 ready 文档检索包含指定关键词的统一文本块。
//
// P3 使用大小写不敏感的字面子串匹配，保证连续中文和英文都能正确
// 检索；PostgreSQL 通过 pg_trgm GIN 索引加速该 ILIKE 条件。
func (r *ChunkRepository) Search(
	ctx context.Context,
	options document.SearchOptions,
) (document.SearchResult, error) {
	queryPattern := literalSubstringPattern(options.Query)

	const countQuery = `
		SELECT COUNT(*)
		FROM text_chunks AS chunk
		JOIN documents AS source_document
			ON source_document.id = chunk.document_id
		WHERE source_document.status = $1
		  AND chunk.content ILIKE $2 ESCAPE E'\\'
		  AND ($3::BIGINT IS NULL OR chunk.document_id = $3)
	`

	var total int64
	if err := r.pool.QueryRow(
		ctx,
		countQuery,
		document.StatusReady,
		queryPattern,
		options.DocumentID,
	).Scan(&total); err != nil {
		return document.SearchResult{}, fmt.Errorf(
			"count matching document chunks: %w",
			err,
		)
	}

	// 即使没有命中也返回非 nil 空切片，保证上层能够稳定输出 JSON []。
	hits := make([]document.SearchHit, 0)
	if total == 0 {
		return document.SearchResult{
			Hits:  hits,
			Total: 0,
		}, nil
	}

	const searchQuery = `
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
		WHERE source_document.status = $1
		  AND chunk.content ILIKE $2 ESCAPE E'\\'
		  AND ($3::BIGINT IS NULL OR chunk.document_id = $3)
		ORDER BY
			source_document.created_at DESC,
			source_document.id DESC,
			chunk.chunk_index ASC,
			chunk.id ASC
		LIMIT $4
		OFFSET $5
	`

	rows, err := r.pool.Query(
		ctx,
		searchQuery,
		document.StatusReady,
		queryPattern,
		options.DocumentID,
		options.Limit,
		options.Offset,
	)
	if err != nil {
		return document.SearchResult{}, fmt.Errorf(
			"query matching document chunks: %w",
			err,
		)
	}
	defer rows.Close()

	for rows.Next() {
		hit, err := scanSearchHit(rows)
		if err != nil {
			return document.SearchResult{}, fmt.Errorf(
				"scan matching document chunk: %w",
				err,
			)
		}
		hits = append(hits, hit)
	}
	if err := rows.Err(); err != nil {
		return document.SearchResult{}, fmt.Errorf(
			"iterate matching document chunks: %w",
			err,
		)
	}

	return document.SearchResult{
		Hits:  hits,
		Total: total,
	}, nil
}

// literalSubstringPattern 把用户关键词转换成 ILIKE 字面子串模式。
//
// 反斜杠是当前 SQL 的 ESCAPE 字符；百分号和下划线是 LIKE 通配符。
// 先转义这三类字符，再在两端添加百分号，才能保持“字面子串”语义。
func literalSubstringPattern(query string) string {
	replacer := strings.NewReplacer(
		`\`, `\\`,
		`%`, `\%`,
		`_`, `\_`,
	)

	escapedQuery := replacer.Replace(query)

	return "%" + escapedQuery + "%"
}

func scanTextChunk(row pgx.Row) (document.TextChunk, error) {
	var chunk document.TextChunk

	err := row.Scan(
		&chunk.ID,
		&chunk.DocumentID,
		&chunk.Index,
		&chunk.Content,
		&chunk.PageStart,
		&chunk.PageEnd,
		&chunk.CreatedAt,
	)
	if err != nil {
		return document.TextChunk{}, err
	}

	return chunk, nil
}

// scanSearchHit 把文档与文本块 JOIN 后的一行转换成搜索命中模型。
func scanSearchHit(row pgx.Row) (document.SearchHit, error) {
	var hit document.SearchHit

	err := row.Scan(
		&hit.ChunkID,
		&hit.DocumentID,
		&hit.ChunkIndex,
		&hit.Title,
		&hit.OriginalName,
		&hit.MIMEType,
		&hit.Content,
		&hit.PageStart,
		&hit.PageEnd,
	)
	if err != nil {
		return document.SearchHit{}, err
	}

	return hit, nil
}
