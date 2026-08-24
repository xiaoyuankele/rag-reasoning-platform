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

// NewAnswerJobLogger 创建异步问答观测适配器。
func NewAnswerJobLogger(logger *slog.Logger) *AnswerJobLogger {
	if logger == nil {
		panic("NewAnswerJobLogger requires a non-nil logger")
	}
	return &AnswerJobLogger{logger: logger}
}

// ObserveAnswerJobEvent 只记录任务状态、重试和耗时。
func (l *AnswerJobLogger) ObserveAnswerJobEvent(
	ctx context.Context,
	event answerapplication.JobEvent,
) {
	attributes := []slog.Attr{
		slog.String("event", string(event.Type)),
		slog.Int64("answer_job_id", event.JobID),
		slog.String("status", string(event.Status)),
		slog.Int("attempt_count", event.AttemptCount),
	}
	if event.Duration > 0 {
		attributes = append(
			attributes,
			slog.Int64("duration_ms", event.Duration.Milliseconds()),
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
	if event.Err != nil {
		attributes = append(attributes, slog.Any("error", event.Err))
	}

	l.logger.LogAttrs(
		ctx,
		answerJobEventLevel(event.Type),
		"Answer job lifecycle event",
		attributes...,
	)
}

func answerJobEventLevel(eventType answerapplication.JobEventType) slog.Level {
	switch eventType {
	case answerapplication.JobEventFailed,
		answerapplication.JobEventInterrupted,
		answerapplication.JobEventUnfinished:
		return slog.LevelError
	case answerapplication.JobEventRequeued:
		return slog.LevelWarn
	default:
		return slog.LevelInfo
	}
}
