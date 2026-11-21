package observability

import (
	"context"
	"log/slog"

	documentapplication "rag-reasoning-platform/backend/internal/application/document"
)

// UploadAdmissionLogger 把上传容量闸门事件转换成结构化 slog 日志。
type UploadAdmissionLogger struct {
	logger *slog.Logger
}

var _ documentapplication.UploadAdmissionEventObserver = (*UploadAdmissionLogger)(nil)

// NewUploadAdmissionLogger 创建上传容量观测适配器。
func NewUploadAdmissionLogger(logger *slog.Logger) *UploadAdmissionLogger {
	if logger == nil {
		panic("NewUploadAdmissionLogger requires a non-nil logger")
	}
	return &UploadAdmissionLogger{logger: logger}
}

// ObserveUploadAdmissionEvent 输出容量、耗时和读取字节数。
// 文件名、内容、哈希、路径和用户标识不会进入该日志。
func (l *UploadAdmissionLogger) ObserveUploadAdmissionEvent(
	ctx context.Context,
	event documentapplication.UploadAdmissionEvent,
) {
	attributes := []slog.Attr{
		slog.String("event", string(event.Type)),
		slog.Int64("wait_duration_ms", event.WaitDuration.Milliseconds()),
		slog.Int("owner_in_flight", event.OwnerInFlight),
		slog.Int("owner_max_concurrency", event.OwnerMaxConcurrency),
		slog.Int("global_in_flight", event.GlobalInFlight),
		slog.Int("global_max_concurrency", event.GlobalMaxConcurrency),
	}
	if event.Outcome != "" {
		attributes = append(
			attributes,
			slog.String("outcome", string(event.Outcome)),
		)
	}
	if event.Type == documentapplication.UploadAdmissionEventReleased {
		attributes = append(
			attributes,
			slog.Int64(
				"execution_duration_ms",
				event.ExecutionDuration.Milliseconds(),
			),
			slog.Int64("file_bytes", event.BytesRead),
			slog.Bool("duplicate", event.Duplicate),
		)
	}
	if requestID, ok := RequestIDFromContext(ctx); ok {
		attributes = append(attributes, slog.String("request_id", requestID))
	}

	l.logger.LogAttrs(
		ctx,
		uploadAdmissionEventLevel(event),
		"Upload concurrency admission event",
		attributes...,
	)
}

func uploadAdmissionEventLevel(
	event documentapplication.UploadAdmissionEvent,
) slog.Level {
	if event.Type == documentapplication.UploadAdmissionEventRejected &&
		(event.Outcome == documentapplication.UploadAdmissionOutcomeOwnerCapacityTimeout ||
			event.Outcome == documentapplication.UploadAdmissionOutcomeGlobalCapacityTimeout) {
		return slog.LevelWarn
	}
	return slog.LevelInfo
}
