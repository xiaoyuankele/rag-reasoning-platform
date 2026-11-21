package document

import (
	"context"
	"fmt"

	accessdomain "rag-reasoning-platform/backend/internal/domain/access"
	documentdomain "rag-reasoning-platform/backend/internal/domain/document"
)

// ProcessingJobCancelService 编排“当前用户取消一条排队中解析任务”的用例。
type ProcessingJobCancelService struct {
	jobs documentdomain.ScopedProcessingJobCanceler
}

// NewProcessingJobCancelService 创建解析任务取消服务。
func NewProcessingJobCancelService(
	jobs documentdomain.ScopedProcessingJobCanceler,
) *ProcessingJobCancelService {
	return &ProcessingJobCancelService{jobs: jobs}
}

// Cancel 校验任务 ID，再把并发安全的状态转换交给持久化端口。
func (s *ProcessingJobCancelService) Cancel(
	ctx context.Context,
	scope accessdomain.OwnerScope,
	jobID int64,
) (documentdomain.ProcessingJob, error) {
	if jobID <= 0 {
		return documentdomain.ProcessingJob{}, ErrInvalidProcessingJobID
	}

	canceledJob, err := s.jobs.CancelProcessingJob(ctx, scope, jobID)
	if err != nil {
		return documentdomain.ProcessingJob{}, fmt.Errorf(
			"cancel processing job: %w",
			err,
		)
	}
	return canceledJob, nil
}
