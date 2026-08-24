package answer

import (
	"context"
	"errors"
	"fmt"
	"time"
)

var (
	// ErrAnswerJobRetentionDependencies 表示清理服务缺少仓储或观测端口。
	ErrAnswerJobRetentionDependencies = errors.New(
		"answer job retention dependencies must be provided",
	)
	// ErrInvalidAnswerJobRetention 表示保留期或单批删除数不是正数。
	ErrInvalidAnswerJobRetention = errors.New(
		"answer job retention and cleanup batch size must be positive",
	)
)

// JobRetentionRepository 定义清理终态异步问答所需的最小持久化能力。
type JobRetentionRepository interface {
	DeleteExpiredAnswerJobs(
		ctx context.Context,
		completedBefore time.Time,
		limit int,
	) (int64, error)
}

// JobRetentionEvent 是不含问题、答案、来源和 Owner 的安全清理事件。
type JobRetentionEvent struct {
	DeletedCount    int64
	CompletedBefore time.Time
	BatchSize       int
	Duration        time.Duration
}

// JobRetentionObserver 隔离清理用例与具体日志实现。
type JobRetentionObserver interface {
	ObserveAnswerJobRetention(context.Context, JobRetentionEvent)
}

// JobRetentionService 按固定保留期分批删除已结束的异步问答任务。
// 它实现 WorkerLoop 所需的 RunOnce 形状，但不执行远程模型调用。
type JobRetentionService struct {
	jobs      JobRetentionRepository
	observer  JobRetentionObserver
	retention time.Duration
	batchSize int
	now       func() time.Time
}

// NewJobRetentionService 创建异步问答保留期清理服务。
func NewJobRetentionService(
	jobs JobRetentionRepository,
	observer JobRetentionObserver,
	retention time.Duration,
	batchSize int,
) (*JobRetentionService, error) {
	if jobs == nil || observer == nil {
		return nil, ErrAnswerJobRetentionDependencies
	}
	if retention <= 0 || batchSize <= 0 {
		return nil, ErrInvalidAnswerJobRetention
	}
	return &JobRetentionService{
		jobs:      jobs,
		observer:  observer,
		retention: retention,
		batchSize: batchSize,
		now:       time.Now,
	}, nil
}

// RunOnce 删除一批 completed_at 早于保留边界的 succeeded/failed/canceled 任务。
// 返回 handled=true 时，WorkerLoop 会立即继续下一批；清空后才进入轮询等待。
func (s *JobRetentionService) RunOnce(ctx context.Context) (bool, error) {
	startedAt := time.Now()
	completedBefore := s.now().Add(-s.retention)
	deletedCount, err := s.jobs.DeleteExpiredAnswerJobs(
		ctx,
		completedBefore,
		s.batchSize,
	)
	if err != nil {
		return false, fmt.Errorf("delete expired answer jobs: %w", err)
	}
	if deletedCount == 0 {
		return false, nil
	}
	s.observer.ObserveAnswerJobRetention(ctx, JobRetentionEvent{
		DeletedCount:    deletedCount,
		CompletedBefore: completedBefore,
		BatchSize:       s.batchSize,
		Duration:        time.Since(startedAt),
	})
	return true, nil
}
