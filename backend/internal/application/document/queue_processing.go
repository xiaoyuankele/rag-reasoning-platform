package document

import (
	"context"
	"errors"
	"fmt"

	accessdomain "rag-reasoning-platform/backend/internal/domain/access"
	documentdomain "rag-reasoning-platform/backend/internal/domain/document"
)

// ErrDocumentNotProcessable 表示文档当前状态不允许创建解析任务。
var ErrDocumentNotProcessable = errors.New(
	"document status does not allow processing",
)

// QueueProcessingService 编排“为文档创建解析任务”的用例。
type QueueProcessingService struct {
	documents documentdomain.ScopedFinder
	jobs      documentdomain.ScopedProcessingJobCreator
}

// NewQueueProcessingService 创建解析任务排队服务。
func NewQueueProcessingService(
	documents documentdomain.ScopedFinder,
	jobs documentdomain.ScopedProcessingJobCreator,
) *QueueProcessingService {
	return &QueueProcessingService{
		documents: documents,
		jobs:      jobs,
	}
}

// Queue 验证文档后创建 queued 状态的解析任务。
//
// 文档保持 uploaded 或 failed，直到后台 worker 真正领取任务时，
// 才会转换成 processing，避免“已经标记处理中但还没有 worker 执行”。
func (s *QueueProcessingService) Queue(
	ctx context.Context,
	scope accessdomain.OwnerScope,
	documentID int64,
) (documentdomain.ProcessingJob, error) {
	if documentID <= 0 {
		return documentdomain.ProcessingJob{}, ErrInvalidID
	}

	foundDocument, err := s.documents.GetByID(ctx, scope, documentID)
	if err != nil {
		return documentdomain.ProcessingJob{}, fmt.Errorf(
			"get document before queuing processing: %w",
			err,
		)
	}

	if foundDocument.Status != documentdomain.StatusUploaded &&
		foundDocument.Status != documentdomain.StatusFailed {
		return documentdomain.ProcessingJob{},
			ErrDocumentNotProcessable
	}

	createdJob, err := s.jobs.CreateProcessingJob(
		ctx,
		scope,
		foundDocument.ID,
	)
	if err != nil {
		return documentdomain.ProcessingJob{}, fmt.Errorf(
			"create document processing job: %w",
			err,
		)
	}

	return createdJob, nil
}
