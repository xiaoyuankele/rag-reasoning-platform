package answer

import (
	"context"
	"errors"
	"time"

	accessdomain "rag-reasoning-platform/backend/internal/domain/access"
)

var (
	// ErrAnswerJobNotFound 表示任务不存在或不属于当前用户。
	ErrAnswerJobNotFound = errors.New("answer job not found")

	// ErrNoQueuedAnswerJob 表示 Worker 当前没有可领取任务，是正常空闲状态。
	ErrNoQueuedAnswerJob = errors.New("no queued answer job")

	// ErrAnswerJobNotProcessing 表示 Worker 试图收尾一条不再处于 processing 的任务。
	ErrAnswerJobNotProcessing = errors.New("answer job is not processing")

	// ErrAnswerJobProcessingCannotCancel 表示任务已经开始调用检索或生成能力。
	ErrAnswerJobProcessingCannotCancel = errors.New(
		"processing answer job cannot be canceled",
	)

	// ErrAnswerJobTerminalCannotCancel 表示成功或失败任务已经结束。
	ErrAnswerJobTerminalCannotCancel = errors.New(
		"terminal answer job cannot be canceled",
	)

	// ErrAnswerOwnerQueueCapacity 表示当前用户的持久化等待预算已满。
	ErrAnswerOwnerQueueCapacity = errors.New("answer owner queue capacity exhausted")

	// ErrAnswerGlobalQueueCapacity 表示整个服务的持久化等待预算已满。
	ErrAnswerGlobalQueueCapacity = errors.New("answer global queue capacity exhausted")

	// ErrInvalidAnswerJobAdmissionLimits 表示持久化队列容量配置无效。
	ErrInvalidAnswerJobAdmissionLimits = errors.New(
		"answer job admission limits are invalid",
	)

	// ErrInvalidAnswerJobSchedulingPolicy 表示 Owner 公平领取策略无效。
	ErrInvalidAnswerJobSchedulingPolicy = errors.New(
		"answer job scheduling policy is invalid",
	)
)

// JobStatus 是一条异步问答任务的稳定生命周期。
type JobStatus string

const (
	JobStatusQueued     JobStatus = "queued"
	JobStatusProcessing JobStatus = "processing"
	JobStatusSucceeded  JobStatus = "succeeded"
	JobStatusFailed     JobStatus = "failed"
	JobStatusCanceled   JobStatus = "canceled"
)

// IsValid 判断状态是否属于当前状态机。
func (s JobStatus) IsValid() bool {
	switch s {
	case JobStatusQueued,
		JobStatusProcessing,
		JobStatusSucceeded,
		JobStatusFailed,
		JobStatusCanceled:
		return true
	default:
		return false
	}
}

// JobErrorCode 是可以持久化并安全返回前端的失败分类。
// 底层 SQL、Provider 响应和密钥等诊断信息不能写入该字段。
type JobErrorCode string

const (
	JobErrorCodeNone                   JobErrorCode = ""
	JobErrorCodeExecutionFailed        JobErrorCode = "answer_execution_failed"
	JobErrorCodeTemporarilyUnavailable JobErrorCode = "answer_temporarily_unavailable"
	JobErrorCodeWorkerInterrupted      JobErrorCode = "answer_worker_interrupted"
)

// JobResult 保存已经成功完成的问答结果快照。
type JobResult struct {
	Answer           string
	ResponseLanguage ResponseLanguage
	Sources          []Source
	PromptTokens     int
	CompletionTokens int
	TotalTokens      int
}

// Job 表示当前用户提交的一次可持久化、可轮询异步问答。
// OwnerUserID 只供 Repository 和 Worker 建立可信作用域，不写入 HTTP 响应。
type Job struct {
	ID                        int64
	OwnerUserID               int64
	DocumentID                *int64
	Query                     string
	TopK                      int
	RequestedResponseLanguage ResponseLanguage
	Status                    JobStatus
	AttemptCount              int
	ErrorCode                 JobErrorCode
	ErrorMessage              *string
	Result                    *JobResult
	NextAttemptAt             time.Time
	CreatedAt                 time.Time
	UpdatedAt                 time.Time
	StartedAt                 *time.Time
	CompletedAt               *time.Time
}

// JobAdmissionLimits 约束数据库中 queued 任务数量。
// processing 已取得执行槽位，不计入等待预算。
type JobAdmissionLimits struct {
	MaxQueuedJobsPerOwner int
	MaxQueuedJobsGlobal   int
}

// IsValid 判断用户等待预算是否不大于全局预算。
func (l JobAdmissionLimits) IsValid() bool {
	return l.MaxQueuedJobsPerOwner > 0 &&
		l.MaxQueuedJobsGlobal >= l.MaxQueuedJobsPerOwner
}

// JobSchedulingPolicy 定义 Worker 按 Owner 轮询领取任务的规则。
type JobSchedulingPolicy struct {
	MaxInFlightPerOwner         int
	MaxBorrowedInFlightPerOwner int
	StarvationThreshold         time.Duration
}

// IsValid 判断基础并发、借用上限和防饥饿阈值是否有效。
func (p JobSchedulingPolicy) IsValid() bool {
	return p.MaxInFlightPerOwner > 0 &&
		p.MaxBorrowedInFlightPerOwner >= p.MaxInFlightPerOwner &&
		p.StarvationThreshold > 0
}

// ScopedJobRepository 是用户创建、查询和取消异步问答所需的最小端口。
type ScopedJobRepository interface {
	CreateAnswerJob(
		ctx context.Context,
		scope accessdomain.OwnerScope,
		input Input,
	) (Job, error)
	GetAnswerJobByID(
		ctx context.Context,
		scope accessdomain.OwnerScope,
		jobID int64,
	) (Job, error)
	CancelAnswerJob(
		ctx context.Context,
		scope accessdomain.OwnerScope,
		jobID int64,
	) (Job, error)
}

// JobWorkerRepository 组合 Worker 的领取、成功、重排和失败收尾能力。
type JobWorkerRepository interface {
	ClaimNextAnswerJob(ctx context.Context) (Job, error)
	MarkAnswerJobSucceeded(
		ctx context.Context,
		jobID int64,
		output Output,
	) error
	RequeueAnswerJob(
		ctx context.Context,
		jobID int64,
		nextAttemptAt time.Time,
		errorCode JobErrorCode,
		errorMessage string,
	) error
	MarkAnswerJobFailed(
		ctx context.Context,
		jobID int64,
		errorCode JobErrorCode,
		errorMessage string,
	) error
}

// InterruptedJobRecoverer 在应用重启时恢复上次遗留的 processing 任务。
type InterruptedJobRecoverer interface {
	RequeueInterruptedAnswerJobs(
		ctx context.Context,
		recoveredAt time.Time,
		errorCode JobErrorCode,
		errorMessage string,
	) (int64, error)
}
