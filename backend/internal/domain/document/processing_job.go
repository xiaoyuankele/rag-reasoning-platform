package document

import (
	"context"
	"errors"
	"time"

	accessdomain "rag-reasoning-platform/backend/internal/domain/access"
)

// ErrActiveProcessingJobExists 表示文档已经存在排队中或处理中的任务。
var ErrActiveProcessingJobExists = errors.New(
	"active document processing job already exists",
)

// ErrOwnerActiveProcessingJobLimitExceeded 表示当前用户的活动解析任务已经达到上限。
var ErrOwnerActiveProcessingJobLimitExceeded = errors.New(
	"owner active document processing job limit exceeded",
)

// ErrGlobalProcessingJobLimitExceeded 表示系统的活动解析任务已经达到全局上限。
var ErrGlobalProcessingJobLimitExceeded = errors.New(
	"global active document processing job limit exceeded",
)

// ErrInvalidProcessingJobAdmissionLimits 表示内部组装了无效的解析任务容量配置。
var ErrInvalidProcessingJobAdmissionLimits = errors.New(
	"document processing job admission limits are invalid",
)

// ErrInvalidProcessingJobSchedulingPolicy 表示 Worker 收到了无效的 Owner 调度策略。
var ErrInvalidProcessingJobSchedulingPolicy = errors.New(
	"document processing job scheduling policy is invalid",
)

// ErrProcessingJobNotFound 表示指定解析任务不存在。
var ErrProcessingJobNotFound = errors.New(
	"document processing job not found",
)

// ErrProcessingJobProcessingCannotCancel 表示任务已经被 Worker 领取，
// 第一版不允许用户强制终止正在执行的 Python 文档处理。
var ErrProcessingJobProcessingCannotCancel = errors.New(
	"processing document job cannot be canceled",
)

// ErrProcessingJobTerminalCannotCancel 表示成功或失败的历史任务已经结束，
// 不能再转换为 canceled。
var ErrProcessingJobTerminalCannotCancel = errors.New(
	"terminal document processing job cannot be canceled",
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
	ProcessingJobStatusCanceled   ProcessingJobStatus = "canceled"
)

// IsValid 判断任务状态是否由当前系统支持。
func (s ProcessingJobStatus) IsValid() bool {
	switch s {
	case ProcessingJobStatusQueued,
		ProcessingJobStatusProcessing,
		ProcessingJobStatusSucceeded,
		ProcessingJobStatusFailed,
		ProcessingJobStatusCanceled:
		return true
	default:
		return false
	}
}

// ProcessingErrorCode 是文档处理失败时可持久化、可统计的稳定分类。
//
// 它不包含文件路径、SQL 或第三方响应等底层细节；完整错误只进入后端日志。
type ProcessingErrorCode string

const (
	// ProcessingErrorCodeNone 表示本次处理没有业务失败。
	ProcessingErrorCodeNone ProcessingErrorCode = ""

	// ProcessingErrorCodeDocumentLookup 表示 Worker 领取任务后无法读取文档记录。
	ProcessingErrorCodeDocumentLookup ProcessingErrorCode = "document_lookup_failed"

	// ProcessingErrorCodeProcessor 表示文档处理器执行失败。
	ProcessingErrorCodeProcessor ProcessingErrorCode = "processor_failed"

	// ProcessingErrorCodeProcessorTimeout 表示文档处理器超过执行时限。
	ProcessingErrorCodeProcessorTimeout ProcessingErrorCode = "processor_timeout"

	// ProcessingErrorCodeChunkWrite 表示处理器成功，但文本块写入失败。
	ProcessingErrorCodeChunkWrite ProcessingErrorCode = "chunk_write_failed"

	// ProcessingErrorCodeFinalization 表示处理结果已经产生，但任务终态回写失败。
	ProcessingErrorCodeFinalization ProcessingErrorCode = "finalization_failed"
)

// ProcessingExecutionMetrics 是一次 Worker 执行需要持久化的精简观测数据。
// QueueWait 和 TotalDuration 由 PostgreSQL 根据任务时间戳计算，Application
// 只负责提供它能够准确测量的处理器耗时和输入、输出规模。
type ProcessingExecutionMetrics struct {
	ProcessorDuration  time.Duration
	ChunkWriteDuration *time.Duration
	ProcessorStages    *ProcessorStageMetrics
	FileBytes          int64
	ChunkCount         int
	ErrorCode          ProcessingErrorCode
}

// ProcessorStageMetrics 是具体文档处理器返回的可选内部阶段指标。
//
// 当前 Python PDF 处理器会提供这些数据；Go 文本处理器或旧版 Python
// 响应可以不提供。可选指针用于区分“没有观测”与“已执行且不足 1ms”。
type ProcessorStageMetrics struct {
	TotalDuration        time.Duration
	SourceOpenDuration   time.Duration
	MetadataReadDuration time.Duration
	TextExtractDuration  time.Duration
	TextSplitDuration    time.Duration
	PageCount            int
	SlowestPageNumber    int
	SlowestPageDuration  time.Duration
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

// ProcessingJobAdmissionLimits 是创建解析任务时必须原子执行的容量约束。
// 活动任务只包含 queued 和 processing；成功或失败的历史任务不占用名额。
type ProcessingJobAdmissionLimits struct {
	MaxActiveJobsPerOwner int
	MaxActiveJobsGlobal   int
}

// IsValid 判断用户上限和全局上限能否组成有效的准入策略。
func (l ProcessingJobAdmissionLimits) IsValid() bool {
	return l.MaxActiveJobsPerOwner > 0 &&
		l.MaxActiveJobsGlobal >= l.MaxActiveJobsPerOwner
}

// ProcessingJobSchedulingPolicy 定义 Worker 领取解析任务时的 Owner 公平规则。
//
// MaxInFlightPerOwner 是存在多个等待 Owner 时的基础并发上限；
// MaxBorrowedInFlightPerOwner 是没有其他 Owner 可以获得基础槽位时的绝对上限；
// StarvationThreshold 控制 queued 任务等待多久后进入防饥饿优先级。
type ProcessingJobSchedulingPolicy struct {
	MaxInFlightPerOwner         int
	MaxBorrowedInFlightPerOwner int
	StarvationThreshold         time.Duration
}

// IsValid 判断公平、借用和防饥饿规则能否组成有效策略。
func (p ProcessingJobSchedulingPolicy) IsValid() bool {
	return p.MaxInFlightPerOwner > 0 &&
		p.MaxBorrowedInFlightPerOwner >= p.MaxInFlightPerOwner &&
		p.StarvationThreshold > 0
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

// ScopedProcessingJobCreator 定义只能为当前所有者的文档创建解析任务的能力。
// 文档不存在和属于其他用户都必须返回 ErrNotFound。
type ScopedProcessingJobCreator interface {
	CreateProcessingJob(
		ctx context.Context,
		scope accessdomain.OwnerScope,
		documentID int64,
	) (ProcessingJob, error)
}

// ScopedProcessingJobFinder 定义只能查询当前所有者文档所关联任务的能力。
// 任务不存在和任务属于其他用户都必须返回 ErrProcessingJobNotFound。
type ScopedProcessingJobFinder interface {
	GetProcessingJobByID(
		ctx context.Context,
		scope accessdomain.OwnerScope,
		jobID int64,
	) (ProcessingJob, error)
}

// ScopedLatestProcessingJobFinder 定义按照一批文档 ID 查询各自最新解析任务的能力。
//
// 实现只能返回当前 OwnerScope 可见文档关联的任务。文档不存在、没有任务或
// 属于其他用户都通过“结果中没有该 document_id”表达，避免泄露资源存在性。
type ScopedLatestProcessingJobFinder interface {
	FindLatestProcessingJobsByDocumentIDs(
		ctx context.Context,
		scope accessdomain.OwnerScope,
		documentIDs []int64,
	) ([]ProcessingJob, error)
}

// ScopedProcessingJobCanceler 定义在所有者边界内取消解析任务的能力。
// queued 可以取消；canceled 重复取消保持幂等；processing、succeeded 和
// failed 必须返回对应的稳定领域错误。
type ScopedProcessingJobCanceler interface {
	CancelProcessingJob(
		ctx context.Context,
		scope accessdomain.OwnerScope,
		jobID int64,
	) (ProcessingJob, error)
}

// ProcessingJobClaimer 定义 Worker 原子领取下一条排队任务所需的能力。
type ProcessingJobClaimer interface {
	ClaimNextProcessingJob(
		ctx context.Context,
	) (ProcessingJob, error)
}

// ProcessingCompletion 是处理任务成功收尾时需要持久化的业务结果。
type ProcessingCompletion struct {
	// DetectedTitle 是处理器自动识别的可选标题。
	// Infrastructure 只在文档当前没有标题时采用它，避免覆盖未来的用户标题。
	DetectedTitle *string

	// Metrics 保存本次成功执行的精简性能指标。
	Metrics ProcessingExecutionMetrics
}

// ProcessingFailure 是任务失败收尾需要持久化的安全结果。
type ProcessingFailure struct {
	// Message 可以返回给前端，不包含底层错误细节。
	Message string

	// Metrics 保存本次失败执行已经获得的精简性能指标。
	Metrics ProcessingExecutionMetrics
}

// ProcessingJobFinalizer 定义 Worker 完成一次执行后所需的状态回写能力。
type ProcessingJobFinalizer interface {
	MarkProcessingJobSucceeded(
		ctx context.Context,
		jobID int64,
		completion ProcessingCompletion,
	) error

	MarkProcessingJobFailed(
		ctx context.Context,
		jobID int64,
		failure ProcessingFailure,
	) error
}

// InterruptedProcessingJobRecoverer 定义应用启动时恢复异常中断任务
// 所需的持久化能力。
//
// 当前系统只运行一个 Worker 实例，因此服务启动时仍处于 processing
// 的任务可以确定为上一次进程异常退出留下的中断任务。
type InterruptedProcessingJobRecoverer interface {
	MarkInterruptedProcessingJobsFailed(
		ctx context.Context,
		errorMessage string,
	) (recoveredCount int64, err error)
}
