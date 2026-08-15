package observability

import (
	"context"
	"log/slog"

	answerapplication "rag-reasoning-platform/backend/internal/application/answer"
)

// GenerationCallLogger 把在线问答的生成事件转换成结构化 slog 日志。
type GenerationCallLogger struct {
	logger *slog.Logger
}

var _ answerapplication.GenerationEventObserver = (*GenerationCallLogger)(nil)

// NewGenerationCallLogger 创建在线生成调用日志适配器。
func NewGenerationCallLogger(logger *slog.Logger) *GenerationCallLogger {
	if logger == nil {
		panic("NewGenerationCallLogger requires a non-nil logger")
	}
	return &GenerationCallLogger{logger: logger}
}

// ObserveGenerationEvent 输出不包含问题、Prompt、证据正文、答案和密钥的调用事件。
func (l *GenerationCallLogger) ObserveGenerationEvent(
	ctx context.Context,
	event answerapplication.GenerationEvent,
) {
	attributes := []slog.Attr{
		slog.String("event", string(event.Type)),
		slog.String("model_name", event.ModelName),
		slog.String("response_language", string(event.ResponseLanguage)),
		slog.Int("requested_top_k", event.RequestedTopK),
		slog.Int("evidence_count", event.EvidenceCount),
	}

	if requestID, ok := RequestIDFromContext(ctx); ok {
		attributes = append(attributes, slog.String("request_id", requestID))
	}
	if event.DocumentID != nil {
		attributes = append(attributes, slog.Int64("document_id", *event.DocumentID))
	}
	if event.Type == answerapplication.GenerationEventSucceeded ||
		event.Type == answerapplication.GenerationEventFailed {
		attributes = append(
			attributes,
			slog.Int64("provider_duration_ms", event.ProviderDuration.Milliseconds()),
		)
	}
	if event.Type == answerapplication.GenerationEventSucceeded {
		attributes = append(
			attributes,
			slog.Int("prompt_tokens", event.PromptTokens),
			slog.Int("completion_tokens", event.CompletionTokens),
			slog.Int("total_tokens", event.TotalTokens),
		)
	}
	if event.SkipReason != "" {
		attributes = append(
			attributes,
			slog.String("skip_reason", string(event.SkipReason)),
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
		generationEventLevel(event.Type),
		"Answer generation lifecycle event",
		attributes...,
	)
}

func generationEventLevel(eventType answerapplication.GenerationEventType) slog.Level {
	if eventType == answerapplication.GenerationEventFailed {
		return slog.LevelError
	}
	return slog.LevelInfo
}
