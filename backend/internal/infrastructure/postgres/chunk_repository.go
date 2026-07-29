package postgres

import (
	"context"
	"errors"
	"fmt"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"rag-reasoning-platform/backend/internal/domain/document"
)

// ChunkRepository 使用 PostgreSQL 保存统一文本块。
type ChunkRepository struct {
	pool *pgxpool.Pool
}

var _ document.ChunkRepository = (*ChunkRepository)(nil)

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
			content
		)
		VALUES ($1, $2, $3)
	`

	for _, chunk := range chunks {
		if _, err := transaction.Exec(
			ctx,
			insertQuery,
			lockedDocumentID,
			chunk.Index,
			chunk.Content,
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

func scanTextChunk(row pgx.Row) (document.TextChunk, error) {
	var chunk document.TextChunk

	err := row.Scan(
		&chunk.ID,
		&chunk.DocumentID,
		&chunk.Index,
		&chunk.Content,
		&chunk.CreatedAt,
	)
	if err != nil {
		return document.TextChunk{}, err
	}

	return chunk, nil
}
