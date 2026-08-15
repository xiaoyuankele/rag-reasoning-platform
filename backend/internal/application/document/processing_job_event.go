package document

import (
	"context"
	"time"

	documentdomain "rag-reasoning-platform/backend/internal/domain/document"
)

// ProcessingJobEventType 是文档解析任务生命周期中的稳定事件类型。
//
// 它描述已经发生的业务事实，不规定日志使用 JSON、文本还是外部监控系统；
// 因此 Application 不需要依赖 slog 等具体日志工具。
type ProcessingJobEventType string

const (
	// ProcessingJobEventStarted 表示 Worker 已经从队列领取任务。
	ProcessingJobEventStarted ProcessingJobEventType = "processing_job_started"

	// ProcessingJobEventSucceeded 表示文本块与任务状态都已成功落库。
	ProcessingJobEventSucceeded ProcessingJobEventType = "processing_job_succeeded"

	// ProcessingJobEventFailed 表示任务已经安全地持久化为 failed。
	ProcessingJobEventFailed ProcessingJobEventType = "processing_job_failed"

	// ProcessingJobEventUnfinished 表示 Worker 已领取任务，但未能把任务写入终态。
	// 此时数据库中可能仍是 processing，需要启动恢复或人工排查。
	ProcessingJobEventUnfinished ProcessingJobEventType = "processing_job_unfinished"
)

// ProcessingJobEvent 是 Application 交给可观测性适配器的任务事件数据。
//
// Err 只供后端诊断使用，不能直接返回给前端；Duration 在 started 事件中为零，
// 在其他终结事件中表示本次 Worker 执行耗时。
type ProcessingJobEvent struct {
	Type         ProcessingJobEventType
	JobID        int64
	DocumentID   int64
	AttemptCount int
	Status       documentdomain.ProcessingJobStatus
	Duration     time.Duration
	Err          error
}

// ProcessingJobEventObserver 是 Application 与任务日志实现之间的插口契约。
//
// 观察器不得改变任务处理结果，因此方法没有 error 返回值。生产环境可以用
// slog 实现，测试环境可以用 Fake 收集事件。
type ProcessingJobEventObserver interface {
	ObserveProcessingJobEvent(
		ctx context.Context,
		event ProcessingJobEvent,
	)
}
