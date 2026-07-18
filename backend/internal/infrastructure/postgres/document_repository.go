// Package postgres 提供领域仓储的 PostgreSQL 实现。
package postgres

import (
	"context"
	"errors"
	"fmt"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"rag-reasoning-platform/backend/internal/domain/document"
)

// DocumentRepository 使用 pgx 连接池保存和查询文档。
type DocumentRepository struct {
	pool *pgxpool.Pool
}

// 编译期接口检查：方法不完整时，项目将无法编译。
var _ document.Repository = (*DocumentRepository)(nil)

// NewDocumentRepository 创建 PostgreSQL 文档仓储。
func NewDocumentRepository(pool *pgxpool.Pool) *DocumentRepository {
	return &DocumentRepository{
		pool: pool,
	}
}

// Create 保存文档元数据，并返回数据库生成的完整记录。
func (r *DocumentRepository) Create(
	ctx context.Context,
	input document.CreateInput,
) (document.Document, error) {
	const query = `
		INSERT INTO documents (
			original_name,
			storage_path,
			mime_type,
			size_bytes,
			sha256
		)
		VALUES ($1, $2, $3, $4, $5)
		RETURNING
			id,
			original_name,
			storage_path,
			mime_type,
			size_bytes,
			sha256,
			status,
			error_message,
			created_at,
			updated_at
	`

	row := r.pool.QueryRow(
		ctx,
		query,
		input.OriginalName,
		input.StoragePath,
		input.MIMEType,
		input.SizeBytes,
		input.SHA256,
	)

	savedDocument, err := scanDocument(row)
	if err != nil {
		return document.Document{}, fmt.Errorf("create document: %w", err)
	}

	return savedDocument, nil
}

// GetByID 根据主键查询文档。
func (r *DocumentRepository) GetByID(
	ctx context.Context,
	id int64,
) (document.Document, error) {
	const query = `
		SELECT
			id,
			original_name,
			storage_path,
			mime_type,
			size_bytes,
			sha256,
			status,
			error_message,
			created_at,
			updated_at
		FROM documents
		WHERE id = $1
	`

	row := r.pool.QueryRow(ctx, query, id)

	foundDocument, err := scanDocument(row)
	if errors.Is(err, pgx.ErrNoRows) {
		// 把 pgx 错误转换成稳定的领域错误。
		return document.Document{}, document.ErrNotFound
	}
	if err != nil {
		return document.Document{}, fmt.Errorf("get document by ID: %w", err)
	}

	return foundDocument, nil
}

// scanDocument 把一行 PostgreSQL 查询结果转换成领域模型。
func scanDocument(row pgx.Row) (document.Document, error) {
	var foundDocument document.Document
	var status string

	err := row.Scan(
		&foundDocument.ID,
		&foundDocument.OriginalName,
		&foundDocument.StoragePath,
		&foundDocument.MIMEType,
		&foundDocument.SizeBytes,
		&foundDocument.SHA256,
		&status,
		// ErrorMessage 是 *string，因此这里传入 **string；pgx 会将 SQL NULL 转换成 nil。
		&foundDocument.ErrorMessage,
		&foundDocument.CreatedAt,
		&foundDocument.UpdatedAt,
	)
	if err != nil {
		return document.Document{}, fmt.Errorf("scan document: %w", err)
	}

	foundDocument.Status = document.Status(status)
	if !foundDocument.Status.IsValid() {
		return document.Document{}, fmt.Errorf(
			"scan document: invalid status %q",
			status,
		)
	}

	return foundDocument, nil
}
