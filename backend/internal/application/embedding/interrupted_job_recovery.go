package embedding

import (
	"context"
	"errors"
	"fmt"

	embeddingdomain "rag-reasoning-platform/backend/internal/domain/embedding"
)

var ErrEmbeddingRecoveryDependencies = errors.New(
	"embedding recovery dependencies must be provided",
)

// ExpiredJobRecoveryService 编排多实例安全的向量任务租约恢复。
// 它只决定安全错误说明；是否真正过期由 PostgreSQL 时间判断。
type ExpiredJobRecoveryService struct {
	jobs embeddingdomain.ExpiredJobRecoverer
}

// NewExpiredJobRecoveryService 创建生产环境使用的向量任务恢复服务。
func NewExpiredJobRecoveryService(
	jobs embeddingdomain.ExpiredJobRecoverer,
) (*ExpiredJobRecoveryService, error) {
	if jobs == nil {
		return nil, ErrEmbeddingRecoveryDependencies
	}

	return &ExpiredJobRecoveryService{
		jobs: jobs,
	}, nil
}

// Recover 把真正过期的 processing 任务重新放回 queued 队列。
func (s *ExpiredJobRecoveryService) Recover(
	ctx context.Context,
) (int64, error) {
	recoveredCount, err := s.jobs.RequeueExpiredEmbeddingJobs(
		ctx,
		safeExpiredEmbeddingMessage,
	)
	if err != nil {
		return 0, fmt.Errorf("recover expired embedding jobs: %w", err)
	}

	return recoveredCount, nil
}
