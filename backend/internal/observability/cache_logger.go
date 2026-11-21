package observability

import (
	"context"
	"log/slog"

	answerapplication "rag-reasoning-platform/backend/internal/application/answer"
	embeddingapplication "rag-reasoning-platform/backend/internal/application/embedding"
)

// QueryVectorCacheLogger 把查询向量缓存事件转换成不含问题明文的结构化日志。
type QueryVectorCacheLogger struct {
	logger *slog.Logger
}

var _ embeddingapplication.QueryVectorCacheEventObserver = (*QueryVectorCacheLogger)(nil)

// NewQueryVectorCacheLogger 创建查询向量缓存日志适配器。
func NewQueryVectorCacheLogger(logger *slog.Logger) *QueryVectorCacheLogger {
	if logger == nil {
		panic("NewQueryVectorCacheLogger requires a non-nil logger")
	}
	return &QueryVectorCacheLogger{logger: logger}
}

// ObserveQueryVectorCacheEvent 输出命中分类、模型和等待时间。
func (l *QueryVectorCacheLogger) ObserveQueryVectorCacheEvent(
	ctx context.Context,
	event embeddingapplication.QueryVectorCacheEvent,
) {
	attributes := []slog.Attr{
		slog.String("event", string(event.Type)),
		slog.String("provider", event.Provider),
		slog.String("model_name", event.ModelName),
		slog.Int("dimensions", event.Dimensions),
	}
	if event.WaitDuration > 0 {
		attributes = append(
			attributes,
			slog.Int64("wait_duration_ms", event.WaitDuration.Milliseconds()),
		)
	}
	if event.Err != nil {
		attributes = append(attributes, slog.Any("error", event.Err))
	}
	if requestID, ok := RequestIDFromContext(ctx); ok {
		attributes = append(attributes, slog.String("request_id", requestID))
	}

	l.logger.LogAttrs(
		ctx,
		queryVectorCacheEventLevel(event.Type),
		"Query vector cache event",
		attributes...,
	)
}

func queryVectorCacheEventLevel(
	eventType embeddingapplication.QueryVectorCacheEventType,
) slog.Level {
	switch eventType {
	case embeddingapplication.QueryVectorCacheReadFailed,
		embeddingapplication.QueryVectorCacheWriteFailed:
		return slog.LevelWarn
	default:
		return slog.LevelDebug
	}
}

// AnswerCacheLogger 把问答结果缓存事件转换成不含问题、答案和 Owner 的日志。
type AnswerCacheLogger struct {
	logger *slog.Logger
}

var _ answerapplication.AnswerCacheEventObserver = (*AnswerCacheLogger)(nil)

// NewAnswerCacheLogger 创建问答缓存日志适配器。
func NewAnswerCacheLogger(logger *slog.Logger) *AnswerCacheLogger {
	if logger == nil {
		panic("NewAnswerCacheLogger requires a non-nil logger")
	}
	return &AnswerCacheLogger{logger: logger}
}

// ObserveAnswerCacheEvent 输出缓存结果和 corpus revision，不输出缓存正文。
func (l *AnswerCacheLogger) ObserveAnswerCacheEvent(
	ctx context.Context,
	event answerapplication.AnswerCacheEvent,
) {
	attributes := []slog.Attr{
		slog.String("event", string(event.Type)),
		slog.Int64("corpus_revision", event.CorpusRevision),
	}
	if event.WaitDuration > 0 {
		attributes = append(
			attributes,
			slog.Int64("wait_duration_ms", event.WaitDuration.Milliseconds()),
		)
	}
	if event.Err != nil {
		attributes = append(attributes, slog.Any("error", event.Err))
	}
	if requestID, ok := RequestIDFromContext(ctx); ok {
		attributes = append(attributes, slog.String("request_id", requestID))
	}

	l.logger.LogAttrs(
		ctx,
		answerCacheEventLevel(event.Type),
		"Answer result cache event",
		attributes...,
	)
}

func answerCacheEventLevel(eventType answerapplication.AnswerCacheEventType) slog.Level {
	switch eventType {
	case answerapplication.AnswerCacheReadFailed,
		answerapplication.AnswerCacheWriteFailed,
		answerapplication.AnswerCacheRevisionFailed:
		return slog.LevelWarn
	case answerapplication.AnswerCacheRevisionChanged:
		return slog.LevelInfo
	default:
		return slog.LevelDebug
	}
}
