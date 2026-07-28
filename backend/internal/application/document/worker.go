package document

import (
	"context"
	"errors"
	"fmt"

	documentdomain "rag-reasoning-platform/backend/internal/domain/document"
)

// Worker 编排后台文档解析任务。
//
// 当前阶段先实现安全领取；后续再接入文档处理器以及成功、失败状态回写。
type Worker struct {
	jobs documentdomain.ProcessingJobClaimer
}

// NewWorker 创建文档解析 Worker。
func NewWorker(
	jobs documentdomain.ProcessingJobClaimer,
) *Worker {
	return &Worker{
		jobs: jobs,
	}
}

// ClaimNext 尝试领取下一条排队任务。
//
// claimed 为 false 且 err 为 nil，表示队列当前为空，这是 Worker 空闲时
// 的正常结果；claimed 为 true 表示返回的任务已进入 processing。
func (w *Worker) ClaimNext(
	ctx context.Context,
) (
	job documentdomain.ProcessingJob,
	claimed bool,
	err error,
) {
	foundJob, err := w.jobs.ClaimNextProcessingJob(ctx)
	if errors.Is(err, documentdomain.ErrNoQueuedProcessingJob) {
		return documentdomain.ProcessingJob{}, false, nil
	}
	if err != nil {
		return documentdomain.ProcessingJob{}, false, fmt.Errorf(
			"claim next processing job: %w",
			err,
		)
	}

	return foundJob, true, nil
}
