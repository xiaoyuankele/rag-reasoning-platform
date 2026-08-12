package embedding

import (
	"context"
	"errors"
	"fmt"
	"time"

	embeddingdomain "rag-reasoning-platform/backend/internal/domain/embedding"
)

const safeInterruptedEmbeddingMessage = "embedding generation was interrupted"

var ErrEmbeddingRecoveryDependencies = errors.New(
	"embedding recovery dependencies must be provided",
)

// InterruptedJobRecoveryService 编排单实例应用启动时的向量任务恢复。
//
// 它决定恢复时间和安全错误说明；具体 UPDATE 由任务仓储实现。
type InterruptedJobRecoveryService struct {
	jobs embeddingdomain.InterruptedJobRecoverer
	now  func() time.Time
}

// NewInterruptedJobRecoveryService 创建生产环境使用的向量任务恢复服务。
func NewInterruptedJobRecoveryService(
	jobs embeddingdomain.InterruptedJobRecoverer,
) (*InterruptedJobRecoveryService, error) {
	return newInterruptedJobRecoveryService(jobs, time.Now)
}

func newInterruptedJobRecoveryService(
	jobs embeddingdomain.InterruptedJobRecoverer,
	now func() time.Time,
) (*InterruptedJobRecoveryService, error) {
	if jobs == nil || now == nil {
		return nil, ErrEmbeddingRecoveryDependencies
	}

	return &InterruptedJobRecoveryService{
		jobs: jobs,
		now:  now,
	}, nil
}

// Recover 把遗留 processing 任务重新放回立即可执行的 queued 队列。
func (s *InterruptedJobRecoveryService) Recover(
	ctx context.Context,
) (int64, error) {
	recoveredCount, err := s.jobs.RequeueInterruptedEmbeddingJobs(
		ctx,
		s.now(),
		safeInterruptedEmbeddingMessage,
	)
	if err != nil {
		return 0, fmt.Errorf("recover interrupted embedding jobs: %w", err)
	}

	return recoveredCount, nil
}
