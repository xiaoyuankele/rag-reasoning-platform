package observability

import (
	"context"
	"log/slog"

	answerapplication "rag-reasoning-platform/backend/internal/application/answer"
)

// AnswerAdmissionLogger 把问答并发闸门事件转换成结构化 slog 日志。
type AnswerAdmissionLogger struct {
	logger *slog.Logger
}

var _ answerapplication.AnswerAdmissionEventObserver = (*AnswerAdmissionLogger)(nil)

// NewAnswerAdmissionLogger 创建问答并发观测适配器。
func NewAnswerAdmissionLogger(logger *slog.Logger) *AnswerAdmissionLogger {
	if logger == nil {
		panic("NewAnswerAdmissionLogger requires a non-nil logger")
	}
	return &AnswerAdmissionLogger{logger: logger}
}

// ObserveAnswerAdmissionEvent 输出容量和耗时，不记录问题、Prompt、答案或证据。
func (l *AnswerAdmissionLogger) ObserveAnswerAdmissionEvent(
	ctx context.Context,
	event answerapplication.AnswerAdmissionEvent,
) {
	attributes := []slog.Attr{
		slog.String("event", string(event.Type)),
		slog.Int64("wait_duration_ms", event.WaitDuration.Milliseconds()),
		slog.Int("in_flight", event.InFlight),
		slog.Int("max_concurrency", event.MaxConcurrency),
		slog.Int("owner_in_flight", event.OwnerInFlight),
		slog.Int("owner_max_concurrency", event.OwnerMaxConcurrency),
		slog.Int("waiting", event.Waiting),
		slog.Int("max_waiting", event.MaxWaiting),
		slog.Int("owner_waiting", event.OwnerWaiting),
		slog.Int("owner_max_waiting", event.OwnerMaxWaiting),
	}
	if event.Outcome != "" {
		attributes = append(
			attributes,
			slog.String("outcome", string(event.Outcome)),
		)
	}
	if event.Err != nil {
		attributes = append(attributes, slog.Any("error", event.Err))
	}
	if event.Type == answerapplication.AnswerAdmissionEventReleased ||
		event.Type == answerapplication.AnswerDistributedAdmissionEventReleased {
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
		answerAdmissionEventLevel(event),
		"Answer concurrency admission event",
		attributes...,
	)
}

func answerAdmissionEventLevel(
	event answerapplication.AnswerAdmissionEvent,
) slog.Level {
	if event.Type == answerapplication.AnswerAdmissionEventRejected ||
		event.Type == answerapplication.AnswerDistributedAdmissionEventRejected {
		switch event.Outcome {
		case answerapplication.AnswerAdmissionOutcomeCapacityTimeout,
			answerapplication.AnswerAdmissionOutcomeOwnerCapacity,
			answerapplication.AnswerAdmissionOutcomeGlobalCapacity,
			answerapplication.AnswerAdmissionOutcomeCoordinationError:
			return slog.LevelWarn
		}
	}
	if event.Outcome == answerapplication.AnswerAdmissionOutcomeCoordinationError {
		return slog.LevelWarn
	}
	return slog.LevelInfo
}
