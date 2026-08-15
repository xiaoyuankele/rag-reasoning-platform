package observability

import (
	"context"
	"log/slog"
	"time"

	embeddingapplication "rag-reasoning-platform/backend/internal/application/embedding"
)

// EmbeddingJobLogger 把向量任务事件转换成结构化 slog 日志。
type EmbeddingJobLogger struct {
	logger *slog.Logger
}

var _ embeddingapplication.JobEventObserver = (*EmbeddingJobLogger)(nil)

// NewEmbeddingJobLogger 创建向量任务日志适配器。
func NewEmbeddingJobLogger(logger *slog.Logger) *EmbeddingJobLogger {
	if logger == nil {
		panic("NewEmbeddingJobLogger requires a non-nil logger")
	}
	return &EmbeddingJobLogger{logger: logger}
}

// ObserveEmbeddingJobEvent 输出不包含 chunk 正文、向量值和密钥的任务事件。
func (l *EmbeddingJobLogger) ObserveEmbeddingJobEvent(
	ctx context.Context,
	event embeddingapplication.JobEvent,
) {
	level := embeddingJobEventLevel(event.Type)
	attributes := []slog.Attr{
		slog.String("event", string(event.Type)),
		slog.Int64("embedding_job_id", event.JobID),
		slog.Int64("document_id", event.DocumentID),
		slog.String("model_name", event.ModelName),
		slog.Int("dimensions", event.Dimensions),
		slog.Int("attempt_count", event.AttemptCount),
		slog.String("status", string(event.Status)),
	}

	if event.Type != embeddingapplication.JobEventStarted {
		attributes = append(
			attributes,
			slog.Int64("duration_ms", event.Duration.Milliseconds()),
			slog.Int64("provider_duration_ms", event.ProviderDuration.Milliseconds()),
			slog.Int("provider_call_count", event.ProviderCallCount),
			slog.Int("prompt_tokens", event.PromptTokens),
			slog.Int("total_tokens", event.TotalTokens),
			slog.Int("generated_vector_count", event.GeneratedVectorCount),
		)
	}
	if event.NextAttemptAt != nil {
		attributes = append(
			attributes,
			slog.String("next_attempt_at", event.NextAttemptAt.UTC().Format(time.RFC3339Nano)),
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
		level,
		"Embedding job lifecycle event",
		attributes...,
	)
}

func embeddingJobEventLevel(eventType embeddingapplication.JobEventType) slog.Level {
	switch eventType {
	case embeddingapplication.JobEventRequeued,
		embeddingapplication.JobEventInterrupted:
		return slog.LevelWarn
	case embeddingapplication.JobEventFailed,
		embeddingapplication.JobEventUnfinished:
		return slog.LevelError
	default:
		return slog.LevelInfo
	}
}
