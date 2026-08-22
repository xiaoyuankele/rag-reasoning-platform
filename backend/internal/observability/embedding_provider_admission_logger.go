package observability

import (
	"context"
	"log/slog"

	embeddingapplication "rag-reasoning-platform/backend/internal/application/embedding"
)

// EmbeddingProviderAdmissionLogger 把共享远程 Embedding 闸门事件转换为
// 结构化 slog 日志。
type EmbeddingProviderAdmissionLogger struct {
	logger *slog.Logger
}

var _ embeddingapplication.EmbeddingProviderAdmissionObserver = (*EmbeddingProviderAdmissionLogger)(nil)

// NewEmbeddingProviderAdmissionLogger 创建远程 Embedding 容量观测适配器。
func NewEmbeddingProviderAdmissionLogger(
	logger *slog.Logger,
) *EmbeddingProviderAdmissionLogger {
	if logger == nil {
		panic("NewEmbeddingProviderAdmissionLogger requires a non-nil logger")
	}
	return &EmbeddingProviderAdmissionLogger{logger: logger}
}

// ObserveEmbeddingProviderAdmissionEvent 输出来源、容量和耗时，不记录输入
// 文本、向量、远程响应或 API Key。
func (l *EmbeddingProviderAdmissionLogger) ObserveEmbeddingProviderAdmissionEvent(
	ctx context.Context,
	event embeddingapplication.EmbeddingProviderAdmissionEvent,
) {
	attributes := []slog.Attr{
		slog.String("event", string(event.Type)),
		slog.String("origin", string(event.Origin)),
		slog.Int64("wait_duration_ms", event.WaitDuration.Milliseconds()),
		slog.Int("origin_in_flight", event.OriginInFlight),
		slog.Int("origin_max_concurrency", event.OriginMaxConcurrency),
		slog.Int("in_flight", event.InFlight),
		slog.Int("max_concurrency", event.MaxConcurrency),
	}
	if event.Outcome != "" {
		attributes = append(
			attributes,
			slog.String("outcome", string(event.Outcome)),
		)
	}
	if event.Type == embeddingapplication.EmbeddingProviderAdmissionEventReleased {
		attributes = append(
			attributes,
			slog.Int64(
				"execution_duration_ms",
				event.ExecutionDuration.Milliseconds(),
			),
		)
	}
	if requestID, ok := RequestIDFromContext(ctx); ok {
		attributes = append(attributes, slog.String("request_id", requestID))
	}

	l.logger.LogAttrs(
		ctx,
		embeddingProviderAdmissionEventLevel(event),
		"Embedding provider concurrency admission event",
		attributes...,
	)
}

func embeddingProviderAdmissionEventLevel(
	event embeddingapplication.EmbeddingProviderAdmissionEvent,
) slog.Level {
	if event.Type == embeddingapplication.EmbeddingProviderAdmissionEventRejected &&
		event.Outcome == embeddingapplication.EmbeddingProviderAdmissionOutcomeCapacityTimeout {
		return slog.LevelWarn
	}
	return slog.LevelInfo
}
