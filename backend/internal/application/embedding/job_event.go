package embedding

import (
	"context"
	"errors"
	"time"

	embeddingdomain "rag-reasoning-platform/backend/internal/domain/embedding"
)

// JobEventType 是向量任务生命周期中的稳定事件类型。
type JobEventType string

const (
	JobEventStarted     JobEventType = "embedding_job_started"
	JobEventSucceeded   JobEventType = "embedding_job_succeeded"
	JobEventRequeued    JobEventType = "embedding_job_requeued"
	JobEventFailed      JobEventType = "embedding_job_failed"
	JobEventInterrupted JobEventType = "embedding_job_interrupted"
	JobEventUnfinished  JobEventType = "embedding_job_unfinished"
)

// JobErrorCategory 是后端排障和指标统计使用的稳定错误分类。
// 它不会替代原始 error；原始错误仍然只进入后端日志。
type JobErrorCategory string

const (
	JobErrorCategoryProviderAuthentication JobErrorCategory = "provider_authentication"
	JobErrorCategoryProviderQuota          JobErrorCategory = "provider_quota"
	JobErrorCategoryProviderRateLimit      JobErrorCategory = "provider_rate_limit"
	JobErrorCategoryProviderRequest        JobErrorCategory = "provider_request_rejected"
	JobErrorCategoryProviderResponse       JobErrorCategory = "provider_invalid_response"
	JobErrorCategoryTimeout                JobErrorCategory = "timeout"
	JobErrorCategoryCanceled               JobErrorCategory = "canceled"
	JobErrorCategoryNoChunks               JobErrorCategory = "document_has_no_chunks"
	JobErrorCategoryInternal               JobErrorCategory = "internal"
)

// JobEvent 是 Application 交给可观测性适配器的向量任务事件。
//
// ProviderDuration 只累计远程 Embed 调用时间；Duration 是整条 Worker 执行时间。
// Token 和向量数量也会保留失败前已经发生的部分成本，但只有 succeeded 事件
// 表示这些向量已经成功持久化。
type JobEvent struct {
	Type                 JobEventType
	JobID                int64
	DocumentID           int64
	ModelName            string
	Dimensions           int
	AttemptCount         int
	Status               embeddingdomain.JobStatus
	Duration             time.Duration
	ProviderDuration     time.Duration
	ProviderCallCount    int
	PromptTokens         int
	TotalTokens          int
	GeneratedVectorCount int
	NextAttemptAt        *time.Time
	ErrorCategory        JobErrorCategory
	Err                  error
}

// JobEventObserver 隔离 Application 与 slog、监控平台等具体实现。
// 观察器只记录已经发生的事实，不能改变任务处理结果。
type JobEventObserver interface {
	ObserveEmbeddingJobEvent(ctx context.Context, event JobEvent)
}

func classifyJobError(err error) JobErrorCategory {
	switch {
	case err == nil:
		return ""
	case errors.Is(err, embeddingdomain.ErrEmbeddingAuthentication):
		return JobErrorCategoryProviderAuthentication
	case errors.Is(err, embeddingdomain.ErrEmbeddingQuotaExceeded):
		return JobErrorCategoryProviderQuota
	case errors.Is(err, embeddingdomain.ErrEmbeddingRateLimited):
		return JobErrorCategoryProviderRateLimit
	case errors.Is(err, embeddingdomain.ErrEmbeddingRequestRejected):
		return JobErrorCategoryProviderRequest
	case errors.Is(err, embeddingdomain.ErrInvalidEmbeddingResponse):
		return JobErrorCategoryProviderResponse
	case errors.Is(err, context.DeadlineExceeded):
		return JobErrorCategoryTimeout
	case errors.Is(err, context.Canceled):
		return JobErrorCategoryCanceled
	case errors.Is(err, ErrDocumentHasNoChunks):
		return JobErrorCategoryNoChunks
	default:
		return JobErrorCategoryInternal
	}
}
