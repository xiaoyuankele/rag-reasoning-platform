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

// ErrProcessingJobNotFound 表示指定解析任务不存在。
var ErrProcessingJobNotFound = errors.New(
	"document processing job not found",
)

// ErrNoQueuedProcessingJob 表示当前没有可以被 Worker 领取的排队任务。
//
// 这不是系统故障，而是 Worker 空闲时的正常结果。
var ErrNoQueuedProcessingJob = errors.New(
	"no queued document processing job",
)

// ErrProcessingJobNotProcessing 表示任务或关联文档不处于 processing，
// 因此不能被标记为成功或失败。
var ErrProcessingJobNotProcessing = errors.New(
	"document processing job is not processing",
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

// ProcessingJobFinder 定义按任务 ID 查询解析任务所需的能力。
type ProcessingJobFinder interface {
	GetProcessingJobByID(
		ctx context.Context,
		jobID int64,
	) (ProcessingJob, error)
}

// ProcessingJobClaimer 定义 Worker 原子领取下一条排队任务所需的能力。
type ProcessingJobClaimer interface {
	ClaimNextProcessingJob(
		ctx context.Context,
	) (ProcessingJob, error)
}

// ProcessingJobFinalizer 定义 Worker 完成一次执行后所需的状态回写能力。
type ProcessingJobFinalizer interface {
	MarkProcessingJobSucceeded(
		ctx context.Context,
		jobID int64,
	) error

	MarkProcessingJobFailed(
		ctx context.Context,
		jobID int64,
		errorMessage string,
	) error
}
