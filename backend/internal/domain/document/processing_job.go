package document

import (
	"context"
	"errors"
	"time"
)

// ErrActiveProcessingJobExists 表示文档已经存在排队中或处理中的任务。
var ErrActiveProcessingJobExists = errors.New(
	"active document processing job already exists",
)

// ProcessingJobStatus 是文档解析任务状态。
type ProcessingJobStatus string

const (
	ProcessingJobStatusQueued     ProcessingJobStatus = "queued"
	ProcessingJobStatusProcessing ProcessingJobStatus = "processing"
	ProcessingJobStatusSucceeded  ProcessingJobStatus = "succeeded"
	ProcessingJobStatusFailed     ProcessingJobStatus = "failed"
)

// IsValid 判断任务状态是否由当前系统支持。
func (s ProcessingJobStatus) IsValid() bool {
	switch s {
	case ProcessingJobStatusQueued,
		ProcessingJobStatusProcessing,
		ProcessingJobStatusSucceeded,
		ProcessingJobStatusFailed:
		return true
	default:
		return false
	}
}

// ProcessingJob 表示一次异步文档解析任务。
type ProcessingJob struct {
	ID           int64
	DocumentID   int64
	Status       ProcessingJobStatus
	AttemptCount int
	ErrorMessage *string
	CreatedAt    time.Time
	UpdatedAt    time.Time
	StartedAt    *time.Time
	CompletedAt  *time.Time
}

// ProcessingJobCreator 定义创建解析任务所需的持久化能力。
type ProcessingJobCreator interface {
	CreateProcessingJob(
		ctx context.Context,
		documentID int64,
	) (ProcessingJob, error)
}
