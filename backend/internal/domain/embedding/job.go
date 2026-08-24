// Package embedding 定义文本向量化相关的稳定领域概念和端口。
package embedding

import (
	"context"
	"errors"
	"time"

	accessdomain "rag-reasoning-platform/backend/internal/domain/access"
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

	// ErrJobProcessingCannotCancel 表示任务已经被 Worker 领取，不能再由用户取消。
	ErrJobProcessingCannotCancel = errors.New("processing embedding job cannot be canceled")

	// ErrJobTerminalCannotCancel 表示成功或失败的历史任务已经结束，不能取消。
	ErrJobTerminalCannotCancel = errors.New("terminal embedding job cannot be canceled")

	// ErrOwnerActiveJobLimitExceeded 表示当前用户的活动向量任务已经达到上限。
	ErrOwnerActiveJobLimitExceeded = errors.New("owner active embedding job limit exceeded")

	// ErrGlobalActiveJobLimitExceeded 表示系统的活动向量任务已经达到上限。
	ErrGlobalActiveJobLimitExceeded = errors.New("global active embedding job limit exceeded")

	// ErrInvalidJobAdmissionLimits 表示内部组装了无效的向量任务容量配置。
	ErrInvalidJobAdmissionLimits = errors.New("embedding job admission limits are invalid")

	// ErrInvalidJobSchedulingPolicy 表示 Worker 收到了无效的 Owner 公平策略。
	ErrInvalidJobSchedulingPolicy = errors.New("embedding job scheduling policy is invalid")
)

// JobStatus 是向量任务的生命周期状态。
type JobStatus string

const (
	// JobStatusWaitingDocument 表示用户已经申请向量化，但文档文本块尚未就绪。
	// 解析成功事务会把该状态原子转换为 queued，Worker 不会提前领取它。
	JobStatusWaitingDocument JobStatus = "waiting_document"
	JobStatusQueued          JobStatus = "queued"
	JobStatusProcessing      JobStatus = "processing"
	JobStatusSucceeded       JobStatus = "succeeded"
	JobStatusFailed          JobStatus = "failed"
	JobStatusCanceled        JobStatus = "canceled"
)

// IsValid 判断任务状态是否由当前系统支持。
func (s JobStatus) IsValid() bool {
	switch s {
	case JobStatusWaitingDocument,
		JobStatusQueued,
		JobStatusProcessing,
		JobStatusSucceeded,
		JobStatusFailed,
		JobStatusCanceled:
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

// JobRequestResult 表示一次向量化申请的持久化结果。
// Created=true 表示本次创建了新任务；false 表示返回同一文档已有的活动任务。
type JobRequestResult struct {
	Job     Job
	Created bool
}

// JobAdmissionLimits 是创建向量任务时必须原子执行的容量约束。
// 活动任务包含 waiting_document、queued 和 processing。
type JobAdmissionLimits struct {
	MaxActiveJobsPerOwner int
	MaxActiveJobsGlobal   int
}

// IsValid 判断用户上限和全局上限能否组成有效的准入策略。
func (l JobAdmissionLimits) IsValid() bool {
	return l.MaxActiveJobsPerOwner > 0 &&
		l.MaxActiveJobsGlobal >= l.MaxActiveJobsPerOwner
}

// JobSchedulingPolicy 定义 Embedding Worker 领取任务时的 Owner 公平规则。
//
// MaxInFlightPerOwner 是多个用户竞争时的基础并发上限；
// MaxBorrowedInFlightPerOwner 是没有其他用户可获得基础槽位时的绝对上限；
// StarvationThreshold 控制一个已到执行时间的 queued 任务等待多久后优先处理。
type JobSchedulingPolicy struct {
	MaxInFlightPerOwner         int
	MaxBorrowedInFlightPerOwner int
	StarvationThreshold         time.Duration
}

// IsValid 判断公平、借用和防饥饿规则能否组成有效策略。
func (p JobSchedulingPolicy) IsValid() bool {
	return p.MaxInFlightPerOwner > 0 &&
		p.MaxBorrowedInFlightPerOwner >= p.MaxInFlightPerOwner &&
		p.StarvationThreshold > 0
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

// JobFinder 定义按照任务 ID 查询单个向量任务所需的最小能力。
//
// Application 只依赖这个接口，不需要知道数据来自 PostgreSQL、测试 fake，
// 还是未来的其他存储实现。
type JobFinder interface {
	GetEmbeddingJobByID(ctx context.Context, jobID int64) (Job, error)
}

// ScopedJobRequester 定义为当前所有者的文档持久化向量化意图的能力。
//
// 实现必须与文档状态建立原子边界：ready 文档创建 queued 任务，尚未完成
// 文本处理的文档创建 waiting_document 任务。文档不存在和属于其他用户
// 都必须返回 document.ErrNotFound。
type ScopedJobRequester interface {
	RequestEmbeddingJob(
		ctx context.Context,
		scope accessdomain.OwnerScope,
		documentID int64,
		modelName string,
		dimensions int,
	) (JobRequestResult, error)
}

// ScopedJobFinder 定义只能查询当前所有者文档所关联向量任务的能力。
// 任务不存在和属于其他用户都必须返回 ErrJobNotFound。
type ScopedJobFinder interface {
	GetEmbeddingJobByID(
		ctx context.Context,
		scope accessdomain.OwnerScope,
		jobID int64,
	) (Job, error)
}

// ScopedLatestJobFinder 定义按照一批文档 ID 查询各自最新向量任务的能力。
//
// 实现只能返回当前 OwnerScope 可见文档关联的任务。文档不存在、没有任务或
// 属于其他用户都通过“结果中没有该 document_id”表达，避免泄露资源存在性。
type ScopedLatestJobFinder interface {
	FindLatestEmbeddingJobsByDocumentIDs(
		ctx context.Context,
		scope accessdomain.OwnerScope,
		documentIDs []int64,
	) ([]Job, error)
}

// ScopedJobCanceler 定义在所有者边界内取消向量任务的能力。
// waiting_document 和 queued 可以取消；canceled 重复取消保持幂等；
// processing、succeeded 和 failed 必须返回对应的领域错误。
type ScopedJobCanceler interface {
	CancelEmbeddingJob(
		ctx context.Context,
		scope accessdomain.OwnerScope,
		jobID int64,
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
