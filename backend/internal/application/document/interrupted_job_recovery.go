package document

import (
	"context"
	"fmt"

	documentdomain "rag-reasoning-platform/backend/internal/domain/document"
)

const safeInterruptedProcessingMessage = "document processing lease expired and was requeued"

// ExpiredJobRecoveryService 编排应用启动时的过期租约恢复。
//
// Service 决定业务层使用的安全错误说明；具体事务和 SQL 由仓储实现。
type ExpiredJobRecoveryService struct {
	jobs documentdomain.ExpiredProcessingJobRecoverer
}

// NewExpiredJobRecoveryService 创建过期任务租约恢复服务。
func NewExpiredJobRecoveryService(
	jobs documentdomain.ExpiredProcessingJobRecoverer,
) *ExpiredJobRecoveryService {
	return &ExpiredJobRecoveryService{jobs: jobs}
}

// Recover 只把租约到期的 processing 任务重新放回 queued。
//
// 返回值表示实际恢复的任务数量；零表示没有遗留任务，是正常结果。
func (s *ExpiredJobRecoveryService) Recover(
	ctx context.Context,
) (int64, error) {
	recoveredCount, err := s.jobs.RequeueExpiredProcessingJobs(
		ctx,
		safeInterruptedProcessingMessage,
	)
	if err != nil {
		return 0, fmt.Errorf(
			"recover interrupted processing jobs: %w",
			err,
		)
	}

	return recoveredCount, nil
}
