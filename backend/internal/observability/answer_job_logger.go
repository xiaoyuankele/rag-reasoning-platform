package observability

import (
	"context"
	"log/slog"

	answerapplication "rag-reasoning-platform/backend/internal/application/answer"
)

// AnswerJobLogger 把异步问答 Worker 事件转换为不含正文的结构化日志。
type AnswerJobLogger struct {
	logger *slog.Logger
}

var _ answerapplication.JobEventObserver = (*AnswerJobLogger)(nil)
var _ answerapplication.JobRetentionObserver = (*AnswerJobLogger)(nil)

// NewAnswerJobLogger 创建异步问答观测适配器。
func NewAnswerJobLogger(logger *slog.Logger) *AnswerJobLogger {
	if logger == nil {
		panic("NewAnswerJobLogger requires a non-nil logger")
	}
	return &AnswerJobLogger{logger: logger}
}

// ObserveAnswerJobEvent 只记录任务状态、重试、耗时和匿名队列快照。
func (l *AnswerJobLogger) ObserveAnswerJobEvent(
	ctx context.Context,
	event answerapplication.JobEvent,
) {
	attributes := []slog.Attr{
		slog.String("event", string(event.Type)),
		slog.Int64("answer_job_id", event.JobID),
		slog.String("status", string(event.Status)),
		slog.Int("attempt_count", event.AttemptCount),
		slog.Int("retry_count", event.RetryCount),
		slog.Int64("queue_wait_ms", event.QueueWait.Milliseconds()),
	}
	if event.Type != answerapplication.JobEventStarted {
		attributes = append(
			attributes,
			slog.Int64("execution_duration_ms", event.ExecutionDuration.Milliseconds()),
			slog.Int64("total_ms", event.TotalDuration.Milliseconds()),
			slog.Bool("recovered", event.Recovered),
		)
	}
	if event.QueueStats != nil {
		attributes = append(
			attributes,
			slog.Int64("queued_count", event.QueueStats.QueuedCount),
			slog.Int64("ready_queued_count", event.QueueStats.ReadyQueuedCount),
			slog.Int64("processing_count", event.QueueStats.ProcessingCount),
			slog.Int64("max_owner_processing_count", event.QueueStats.MaxOwnerProcessingCount),
			slog.Int64("oldest_ready_wait_ms", event.QueueStats.OldestReadyWait.Milliseconds()),
		)
	}
	if event.QueueStatsError != nil {
		attributes = append(
			attributes,
			slog.Any("queue_stats_error", event.QueueStatsError),
		)
	}
	if event.NextAttemptAt != nil {
		attributes = append(
			attributes,
			slog.Time("next_attempt_at", *event.NextAttemptAt),
		)
	}
	if event.ErrorCode != answerapplication.JobErrorCodeNone {
		attributes = append(
			attributes,
			slog.String("error_code", string(event.ErrorCode)),
		)
	}
	if event.ErrorCategory != "" {
		attributes = append(
			attributes,
			slog.String("error_category", string(event.ErrorCategory)),
		)
	}
	if event.Err != nil {
		attributes = append(attributes, slog.Any("error", event.Err))
	}

	l.logger.LogAttrs(
		ctx,
		answerJobEventLevel(event),
		"Answer job lifecycle event",
		attributes...,
	)
}

func answerJobEventLevel(event answerapplication.JobEvent) slog.Level {
	switch event.Type {
	case answerapplication.JobEventFailed,
		answerapplication.JobEventInterrupted,
		answerapplication.JobEventUnfinished:
		return slog.LevelError
	case answerapplication.JobEventRequeued:
		return slog.LevelWarn
	default:
		if event.QueueStatsError != nil {
			return slog.LevelWarn
		}
		return slog.LevelInfo
	}
}

// ObserveAnswerJobRetention 记录不含用户内容的保留期清理结果。
func (l *AnswerJobLogger) ObserveAnswerJobRetention(
	ctx context.Context,
	event answerapplication.JobRetentionEvent,
) {
	l.logger.LogAttrs(
		ctx,
		slog.LevelInfo,
		"Expired answer jobs deleted",
		slog.String("event", "answer_jobs_cleaned"),
		slog.Int64("deleted_count", event.DeletedCount),
		slog.Time("completed_before", event.CompletedBefore),
		slog.Int("batch_size", event.BatchSize),
		slog.Int64("duration_ms", event.Duration.Milliseconds()),
	)
}
