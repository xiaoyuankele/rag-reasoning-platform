package document

import (
	"context"
	"fmt"

	accessdomain "rag-reasoning-platform/backend/internal/domain/access"
	documentdomain "rag-reasoning-platform/backend/internal/domain/document"
)

// documentDeleteRepository 定义删除用例需要的最小仓储能力：
// 先查询文档取得存储路径，再删除数据库记录。
type documentDeleteRepository interface {
	documentdomain.ScopedFinder
	documentdomain.ScopedDeleter
}

// fileDeleter 定义删除用例需要的最小文件存储能力。
type fileDeleter interface {
	Delete(ctx context.Context, storagePath string) error
}

// DeleteService 编排文档文件和数据库记录的删除流程。
type DeleteService struct {
	repository documentDeleteRepository
	storage    fileDeleter
}

// NewDeleteService 创建文档删除应用服务。
func NewDeleteService(repository documentDeleteRepository, storage fileDeleter) *DeleteService {
	return &DeleteService{
		repository: repository,
		storage:    storage,
	}
}

// Delete 删除指定文档。
//
// 操作顺序固定为：查询记录、删除文件、删除数据库记录。
// 本地文件删除是幂等的，因此数据库删除失败后仍可安全重试。
func (s *DeleteService) Delete(
	ctx context.Context,
	scope accessdomain.OwnerScope,
	id int64,
) error {
	if id <= 0 {
		return ErrInvalidID
	}

	foundDocument, err := s.repository.GetByID(ctx, scope, id)
	if err != nil {
		return fmt.Errorf("get document by id: %w", err)
	}

	if err := s.storage.Delete(ctx, foundDocument.StoragePath); err != nil {
		return fmt.Errorf("delete stored document file: %w", err)
	}

	if err := s.repository.Delete(ctx, scope, id); err != nil {
		return fmt.Errorf("delete document record: %w", err)
	}

	return nil
}
