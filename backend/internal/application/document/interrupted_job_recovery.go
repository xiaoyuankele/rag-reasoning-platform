package document

import (
	"context"
	"fmt"

	documentdomain "rag-reasoning-platform/backend/internal/domain/document"
)

const safeInterruptedProcessingMessage = "document processing was interrupted"

// InterruptedJobRecoveryService 编排应用启动时的中断任务恢复。
//
// Service 决定业务层使用的安全错误说明；具体事务和 SQL 由仓储实现。
type InterruptedJobRecoveryService struct {
	jobs documentdomain.InterruptedProcessingJobRecoverer
}

// NewInterruptedJobRecoveryService 创建中断任务恢复服务。
func NewInterruptedJobRecoveryService(
	jobs documentdomain.InterruptedProcessingJobRecoverer,
) *InterruptedJobRecoveryService {
	return &InterruptedJobRecoveryService{jobs: jobs}
}

// Recover 把上一次进程异常退出遗留的 processing 任务恢复为 failed。
//
// 返回值表示实际恢复的任务数量；零表示没有遗留任务，是正常结果。
func (s *InterruptedJobRecoveryService) Recover(
	ctx context.Context,
) (int64, error) {
	recoveredCount, err := s.jobs.MarkInterruptedProcessingJobsFailed(
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
