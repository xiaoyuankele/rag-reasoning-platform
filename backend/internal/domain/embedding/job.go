// Package embedding 定义文本向量化相关的稳定领域概念和端口。
package embedding

import (
	"context"
	"errors"
	"time"
)

var (
	// ErrActiveJobExists 表示同一份文档已经有排队中或执行中的向量任务。
	ErrActiveJobExists = errors.New("active embedding job already exists")

	// ErrJobNotFound 表示指定的向量任务不存在。
	ErrJobNotFound = errors.New("embedding job not found")

	// ErrNoQueuedJob 表示当前没有到达执行时间的 queued 向量任务。
	// 这是 Worker 空闲时的正常结果，不是系统故障。
	ErrNoQueuedJob = errors.New("no queued embedding job")

	// ErrJobNotProcessing 表示任务不存在或已经不处于 processing，不能被当前 Worker 收尾。
	ErrJobNotProcessing = errors.New("embedding job is not processing")
)

// JobStatus 是向量任务的生命周期状态。
type JobStatus string

const (
	JobStatusQueued     JobStatus = "queued"
	JobStatusProcessing JobStatus = "processing"
	JobStatusSucceeded  JobStatus = "succeeded"
	JobStatusFailed     JobStatus = "failed"
)

// IsValid 判断任务状态是否由当前系统支持。
func (s JobStatus) IsValid() bool {
	switch s {
	case JobStatusQueued,
		JobStatusProcessing,
		JobStatusSucceeded,
		JobStatusFailed:
		return true
	default:
		return false
	}
}

// Job 表示一次把文档文本块转换为向量的异步任务。
//
// ModelName 和 Dimensions 会在任务创建时冻结。这样即使服务器配置随后改变，
// 历史任务仍能说明自己使用了哪个模型以及期望生成多少维向量。
type Job struct {
	ID            int64
	DocumentID    int64
	ModelName     string
	Dimensions    int
	Status        JobStatus
	AttemptCount  int
	ErrorMessage  *string
	NextAttemptAt time.Time
	PromptTokens  *int
	TotalTokens   *int
	CreatedAt     time.Time
	UpdatedAt     time.Time
	StartedAt     *time.Time
	CompletedAt   *time.Time
}

// JobCreator 定义创建向量任务所需的最小持久化能力。
type JobCreator interface {
	CreateEmbeddingJob(
		ctx context.Context,
		documentID int64,
		modelName string,
		dimensions int,
	) (Job, error)
}

// JobClaimer 定义 Worker 原子领取下一条到期任务所需的能力。
//
// 具体仓储必须保证同一条任务不会被两个并发 Worker 同时领取。
// 没有可领取任务时返回 ErrNoQueuedJob。
type JobClaimer interface {
	ClaimNextEmbeddingJob(ctx context.Context) (Job, error)
}

// InterruptedJobRecoverer 定义单实例应用启动时恢复遗留 processing 任务的能力。
//
// 第一版没有 lease 和 heartbeat，因此只有确认旧进程已经退出后才能调用该能力。
// recoveredAt 同时作为任务下一次允许执行的时间。
type InterruptedJobRecoverer interface {
	RequeueInterruptedEmbeddingJobs(
		ctx context.Context,
		recoveredAt time.Time,
		errorMessage string,
	) (recoveredCount int64, err error)
}

// JobFinalizer 定义 Worker 在一次执行结束后可以采用的三种收尾方式。
//
// MarkEmbeddingJobSucceeded 的具体 PostgreSQL 实现必须在同一个事务中保存
// 全部向量并把任务改为 succeeded，避免出现“任务成功但向量只写了一半”。
type JobFinalizer interface {
	MarkEmbeddingJobSucceeded(
		ctx context.Context,
		jobID int64,
		completion JobCompletion,
	) error

	// RequeueEmbeddingJob 把暂时失败的任务重新放回队列，并设置下次最早执行时间。
	RequeueEmbeddingJob(
		ctx context.Context,
		jobID int64,
		nextAttemptAt time.Time,
		errorMessage string,
	) error

	// MarkEmbeddingJobFailed 记录无需继续重试的永久失败。
	MarkEmbeddingJobFailed(
		ctx context.Context,
		jobID int64,
		errorMessage string,
	) error
}
