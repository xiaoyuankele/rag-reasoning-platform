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

// ScopedDocumentRepository 对每一次文档读写都强制执行 OwnerScope。
// 它与迁移期保留的系统仓储分开，防止面向用户的用例误用无作用域查询。
type ScopedDocumentRepository struct {
	pool *pgxpool.Pool
}

var _ documentdomain.ScopedRepository = (*ScopedDocumentRepository)(nil)

// NewScopedDocumentRepository 创建面向已认证用户的文档仓储。
func NewScopedDocumentRepository(pool *pgxpool.Pool) *ScopedDocumentRepository {
	return &ScopedDocumentRepository{pool: pool}
}

// Create 在指定所有者范围内保存文档元数据。
func (r *ScopedDocumentRepository) Create(
	ctx context.Context,
	scope accessdomain.OwnerScope,
	input documentdomain.CreateInput,
) (documentdomain.Document, error) {
	if !scope.IsValid() {
		return documentdomain.Document{}, accessdomain.ErrInvalidOwnerScope
	}

	const query = `
		INSERT INTO documents (
			owner_user_id,
			original_name,
			storage_path,
			mime_type,
			size_bytes,
			sha256
		)
		VALUES ($1, $2, $3, $4, $5, $6)
		RETURNING
			id,
			owner_user_id,
			title,
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

	savedDocument, err := scanScopedDocument(r.pool.QueryRow(
		ctx,
		query,
		scope.OwnerUserID(),
		input.OriginalName,
		input.StoragePath,
		input.MIMEType,
		input.SizeBytes,
		input.SHA256,
	))
	if err != nil {
		return documentdomain.Document{}, fmt.Errorf("create scoped document: %w", err)
	}
	return savedDocument, nil
}

// CreateOrGetBySHA256 在指定所有者范围内原子创建文档或返回已有副本。
//
// INSERT ... ON CONFLICT 由数据库唯一索引解决并发竞争：即使两个请求同时上传
// 相同内容，也只有一个请求能够创建记录。极少数情况下，如果冲突记录正好被
// 另一个事务删除，方法会进行有界重试，避免返回一个已经消失的文档。
func (r *ScopedDocumentRepository) CreateOrGetBySHA256(
	ctx context.Context,
	scope accessdomain.OwnerScope,
	input documentdomain.CreateInput,
) (documentdomain.CreateOrGetResult, error) {
	if !scope.IsValid() {
		return documentdomain.CreateOrGetResult{}, accessdomain.ErrInvalidOwnerScope
	}

	const maximumAttempts = 3
	for attempt := 0; attempt < maximumAttempts; attempt++ {
		createdDocument, err := r.insertUnlessOwnerHashExists(ctx, scope, input)
		if err == nil {
			return documentdomain.CreateOrGetResult{
				Document: createdDocument,
				Created:  true,
			}, nil
		}
		if !errors.Is(err, pgx.ErrNoRows) {
			return documentdomain.CreateOrGetResult{}, fmt.Errorf(
				"create scoped document unless duplicate: %w",
				err,
			)
		}

		existingDocument, err := r.getByOwnerAndSHA256(ctx, scope, input.SHA256)
		if err == nil {
			return documentdomain.CreateOrGetResult{
				Document: existingDocument,
				Created:  false,
			}, nil
		}
		if !errors.Is(err, pgx.ErrNoRows) {
			return documentdomain.CreateOrGetResult{}, fmt.Errorf(
				"get duplicate scoped document: %w",
				err,
			)
		}
	}

	return documentdomain.CreateOrGetResult{}, errors.New(
		"create or get scoped document: conflicting document disappeared during retry",
	)
}

func (r *ScopedDocumentRepository) insertUnlessOwnerHashExists(
	ctx context.Context,
	scope accessdomain.OwnerScope,
	input documentdomain.CreateInput,
) (documentdomain.Document, error) {
	const query = `
		INSERT INTO documents (
			owner_user_id,
			original_name,
			storage_path,
			mime_type,
			size_bytes,
			sha256
		)
		VALUES ($1, $2, $3, $4, $5, $6)
		ON CONFLICT (owner_user_id, sha256) DO NOTHING
		RETURNING
			id,
			owner_user_id,
			title,
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

	return scanScopedDocument(r.pool.QueryRow(
		ctx,
		query,
		scope.OwnerUserID(),
		input.OriginalName,
		input.StoragePath,
		input.MIMEType,
		input.SizeBytes,
		input.SHA256,
	))
}

func (r *ScopedDocumentRepository) getByOwnerAndSHA256(
	ctx context.Context,
	scope accessdomain.OwnerScope,
	sha256 string,
) (documentdomain.Document, error) {
	const query = `
		SELECT
			id,
			owner_user_id,
			title,
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
		WHERE owner_user_id = $1
		  AND sha256 = $2
	`

	return scanScopedDocument(r.pool.QueryRow(
		ctx,
		query,
		scope.OwnerUserID(),
		sha256,
	))
}

// GetByID 只查询同时匹配文档 ID 和 OwnerScope 的记录。
func (r *ScopedDocumentRepository) GetByID(
	ctx context.Context,
	scope accessdomain.OwnerScope,
	id int64,
) (documentdomain.Document, error) {
	if !scope.IsValid() {
		return documentdomain.Document{}, accessdomain.ErrInvalidOwnerScope
	}

	const query = `
		SELECT
			id,
			owner_user_id,
			title,
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
		  AND owner_user_id = $2
	`

	foundDocument, err := scanScopedDocument(
		r.pool.QueryRow(ctx, query, id, scope.OwnerUserID()),
	)
	if errors.Is(err, pgx.ErrNoRows) {
		return documentdomain.Document{}, documentdomain.ErrNotFound
	}
	if err != nil {
		return documentdomain.Document{}, fmt.Errorf("get scoped document by ID: %w", err)
	}
	return foundDocument, nil
}

// List 只统计并返回 OwnerScope 内的文档。
func (r *ScopedDocumentRepository) List(
	ctx context.Context,
	scope accessdomain.OwnerScope,
	options documentdomain.ListOptions,
) (documentdomain.ListResult, error) {
	if !scope.IsValid() {
		return documentdomain.ListResult{}, accessdomain.ErrInvalidOwnerScope
	}

	const countQuery = `
		SELECT COUNT(*)
		FROM documents
		WHERE owner_user_id = $1
	`
	var total int64
	if err := r.pool.QueryRow(ctx, countQuery, scope.OwnerUserID()).Scan(&total); err != nil {
		return documentdomain.ListResult{}, fmt.Errorf("count scoped documents: %w", err)
	}

	const listQuery = `
		SELECT
			id,
			owner_user_id,
			title,
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
		WHERE owner_user_id = $1
		ORDER BY created_at DESC, id DESC
		LIMIT $2
		OFFSET $3
	`

	rows, err := r.pool.Query(
		ctx,
		listQuery,
		scope.OwnerUserID(),
		options.Limit,
		options.Offset,
	)
	if err != nil {
		return documentdomain.ListResult{}, fmt.Errorf("query scoped documents: %w", err)
	}
	defer rows.Close()

	documents := make([]documentdomain.Document, 0)
	for rows.Next() {
		listedDocument, err := scanScopedDocument(rows)
		if err != nil {
			return documentdomain.ListResult{}, fmt.Errorf("scan scoped listed document: %w", err)
		}
		documents = append(documents, listedDocument)
	}
	if err := rows.Err(); err != nil {
		return documentdomain.ListResult{}, fmt.Errorf("iterate scoped documents: %w", err)
	}

	return documentdomain.ListResult{Documents: documents, Total: total}, nil
}

// Delete 只删除同时匹配文档 ID 和 OwnerScope 的记录。
func (r *ScopedDocumentRepository) Delete(
	ctx context.Context,
	scope accessdomain.OwnerScope,
	id int64,
) error {
	if !scope.IsValid() {
		return accessdomain.ErrInvalidOwnerScope
	}

	commandTag, err := r.pool.Exec(
		ctx,
		`DELETE FROM documents WHERE id = $1 AND owner_user_id = $2`,
		id,
		scope.OwnerUserID(),
	)
	if err != nil {
		return fmt.Errorf("delete scoped document: %w", err)
	}
	if commandTag.RowsAffected() == 0 {
		return documentdomain.ErrNotFound
	}
	return nil
}

// scanScopedDocument 扫描已经通过 owner_user_id 过滤的文档记录。
func scanScopedDocument(row pgx.Row) (documentdomain.Document, error) {
	var foundDocument documentdomain.Document
	var status string

	if err := row.Scan(
		&foundDocument.ID,
		&foundDocument.OwnerUserID,
		&foundDocument.Title,
		&foundDocument.OriginalName,
		&foundDocument.StoragePath,
		&foundDocument.MIMEType,
		&foundDocument.SizeBytes,
		&foundDocument.SHA256,
		&status,
		&foundDocument.ErrorMessage,
		&foundDocument.CreatedAt,
		&foundDocument.UpdatedAt,
	); err != nil {
		return documentdomain.Document{}, err
	}

	foundDocument.Status = documentdomain.Status(status)
	if foundDocument.OwnerUserID <= 0 {
		return documentdomain.Document{}, fmt.Errorf("invalid document owner user ID %d", foundDocument.OwnerUserID)
	}
	if !foundDocument.Status.IsValid() {
		return documentdomain.Document{}, fmt.Errorf("invalid document status %q", status)
	}
	return foundDocument, nil
}
