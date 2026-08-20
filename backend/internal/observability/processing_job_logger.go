package observability

import (
	"context"
	"log/slog"

	documentapplication "rag-reasoning-platform/backend/internal/application/document"
)

// ProcessingJobLogger 把 Application 产生的任务事件转换成结构化 slog 日志。
type ProcessingJobLogger struct {
	logger *slog.Logger
}

var _ documentapplication.ProcessingJobEventObserver = (*ProcessingJobLogger)(nil)

// NewProcessingJobLogger 创建文档解析任务日志适配器。
func NewProcessingJobLogger(logger *slog.Logger) *ProcessingJobLogger {
	if logger == nil {
		panic("NewProcessingJobLogger requires a non-nil logger")
	}

	return &ProcessingJobLogger{logger: logger}
}

// ObserveProcessingJobEvent 输出不含文档正文、路径和密钥的任务生命周期日志。
func (l *ProcessingJobLogger) ObserveProcessingJobEvent(
	ctx context.Context,
	event documentapplication.ProcessingJobEvent,
) {
	level := slog.LevelInfo
	if event.Type == documentapplication.ProcessingJobEventFailed ||
		event.Type == documentapplication.ProcessingJobEventUnfinished {
		level = slog.LevelError
	}

	attributes := []slog.Attr{
		slog.String("event", string(event.Type)),
		slog.Int64("processing_job_id", event.JobID),
		slog.Int64("document_id", event.DocumentID),
		slog.Int("attempt_count", event.AttemptCount),
		slog.String("status", string(event.Status)),
		slog.Int64("queue_wait_ms", event.QueueWait.Milliseconds()),
	}
	if event.Type != documentapplication.ProcessingJobEventStarted {
		attributes = append(
			attributes,
			slog.Int64("processor_ms", event.ProcessorDuration.Milliseconds()),
			slog.Int64("total_ms", event.TotalDuration.Milliseconds()),
			slog.Int64("file_bytes", event.FileBytes),
			slog.Int("chunk_count", event.ChunkCount),
		)
	}
	if event.ErrorCode != "" {
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
		level,
		"Document processing job lifecycle event",
		attributes...,
	)
}
