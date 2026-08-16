// Package document 编排文档相关的应用用例。
package document

import (
	"context"
	"errors"
	"fmt"

	accessdomain "rag-reasoning-platform/backend/internal/domain/access"
	documentdomain "rag-reasoning-platform/backend/internal/domain/document"
)

// ErrInvalidID 表示调用者提供的文档 ID 不合法。
var ErrInvalidID = errors.New("document ID must be positive")

// Service 提供文档相关的应用操作。
//
// Service 依赖领域层定义的 Repository 接口，
// 不直接依赖 PostgreSQL 或 pgx。
type Service struct {
	repository documentdomain.ScopedFinder
}

// NewService 创建文档应用服务。
func NewService(repository documentdomain.ScopedFinder) *Service {
	return &Service{
		repository: repository,
	}
}

// GetByID 校验文档 ID，并从仓储读取文档。
func (s *Service) GetByID(
	ctx context.Context,
	scope accessdomain.OwnerScope,
	id int64,
) (documentdomain.Document, error) {
	if id <= 0 {
		return documentdomain.Document{}, ErrInvalidID
	}

	foundDocument, err := s.repository.GetByID(ctx, scope, id)
	if err != nil {
		// 使用 %w 包装后，HTTP 层仍可通过 errors.Is
		// 识别 documentdomain.ErrNotFound。
		return documentdomain.Document{}, fmt.Errorf(
			"get document by ID: %w",
			err,
		)
	}

	return foundDocument, nil
}
