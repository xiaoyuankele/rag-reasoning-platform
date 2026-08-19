package embedding

import (
	"context"
	"fmt"

	accessdomain "rag-reasoning-platform/backend/internal/domain/access"
	embeddingdomain "rag-reasoning-platform/backend/internal/domain/embedding"
)

// CancelService 编排“当前用户取消一条向量任务”的应用用例。
type CancelService struct {
	jobs embeddingdomain.ScopedJobCanceler
}

// NewCancelService 创建向量任务取消服务。
func NewCancelService(jobs embeddingdomain.ScopedJobCanceler) *CancelService {
	return &CancelService{jobs: jobs}
}

// Cancel 校验应用参数，再把并发安全的状态转换交给持久化端口。
func (s *CancelService) Cancel(
	ctx context.Context,
	scope accessdomain.OwnerScope,
	jobID int64,
) (embeddingdomain.Job, error) {
	if jobID <= 0 {
		return embeddingdomain.Job{}, ErrInvalidEmbeddingJobID
	}

	canceledJob, err := s.jobs.CancelEmbeddingJob(ctx, scope, jobID)
	if err != nil {
		return embeddingdomain.Job{}, fmt.Errorf("cancel embedding job: %w", err)
	}
	return canceledJob, nil
}
