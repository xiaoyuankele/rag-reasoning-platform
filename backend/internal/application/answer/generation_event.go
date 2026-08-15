package answer

import (
	"context"
	"errors"
	"time"

	generationdomain "rag-reasoning-platform/backend/internal/domain/generation"
)

// GenerationEventType 是在线问答中远程生成调用的稳定事件类型。
type GenerationEventType string

const (
	GenerationEventSkipped   GenerationEventType = "answer_generation_skipped"
	GenerationEventStarted   GenerationEventType = "answer_generation_started"
	GenerationEventSucceeded GenerationEventType = "answer_generation_succeeded"
	GenerationEventFailed    GenerationEventType = "answer_generation_failed"
)

// GenerationSkipReason 说明为什么问答没有调用远程生成模型。
type GenerationSkipReason string

const (
	// GenerationSkipReasonInsufficientEvidence 表示检索结果为空，Service 已安全拒答。
	GenerationSkipReasonInsufficientEvidence GenerationSkipReason = "insufficient_evidence"
)

// GenerationErrorCategory 是后端排障和指标统计使用的稳定错误分类。
// 原始 error 仍会保留在后端日志，但不能直接返回给前端。
type GenerationErrorCategory string

const (
	GenerationErrorCategoryProviderAuthentication GenerationErrorCategory = "provider_authentication"
	GenerationErrorCategoryProviderQuota          GenerationErrorCategory = "provider_quota"
	GenerationErrorCategoryProviderRateLimit      GenerationErrorCategory = "provider_rate_limit"
	GenerationErrorCategoryProviderRequest        GenerationErrorCategory = "provider_request_rejected"
	GenerationErrorCategoryProviderUnavailable    GenerationErrorCategory = "provider_unavailable"
	GenerationErrorCategoryProviderResponse       GenerationErrorCategory = "provider_invalid_response"
	GenerationErrorCategoryTimeout                GenerationErrorCategory = "timeout"
	GenerationErrorCategoryCanceled               GenerationErrorCategory = "canceled"
	GenerationErrorCategoryInternal               GenerationErrorCategory = "internal"
)

// GenerationEvent 是 Answer Service 交给可观测性适配器的在线生成事件。
//
// 事件只包含排障和成本统计需要的元数据，禁止放入用户问题、Prompt、证据正文和答案。
// ProviderDuration 只计算 Generator.Generate 的远程调用耗时，不等于整次 HTTP 请求耗时。
type GenerationEvent struct {
	Type             GenerationEventType
	ModelName        string
	ResponseLanguage ResponseLanguage
	DocumentID       *int64
	RequestedTopK    int
	EvidenceCount    int
	ProviderDuration time.Duration
	PromptTokens     int
	CompletionTokens int
	TotalTokens      int
	SkipReason       GenerationSkipReason
	ErrorCategory    GenerationErrorCategory
	Err              error
}

// GenerationEventObserver 隔离 Answer Service 与 slog、监控平台等具体实现。
// 观察器只能记录已经发生的事实，不能改变问答结果。
type GenerationEventObserver interface {
	ObserveGenerationEvent(ctx context.Context, event GenerationEvent)
}

func classifyGenerationError(err error) GenerationErrorCategory {
	switch {
	case err == nil:
		return ""
	case errors.Is(err, generationdomain.ErrGenerationAuthentication):
		return GenerationErrorCategoryProviderAuthentication
	case errors.Is(err, generationdomain.ErrGenerationQuotaExceeded):
		return GenerationErrorCategoryProviderQuota
	case errors.Is(err, generationdomain.ErrGenerationRateLimited):
		return GenerationErrorCategoryProviderRateLimit
	case errors.Is(err, generationdomain.ErrGenerationRequestRejected):
		return GenerationErrorCategoryProviderRequest
	case errors.Is(err, generationdomain.ErrGenerationUnavailable):
		return GenerationErrorCategoryProviderUnavailable
	case errors.Is(err, generationdomain.ErrInvalidGenerationResponse):
		return GenerationErrorCategoryProviderResponse
	case errors.Is(err, context.DeadlineExceeded):
		return GenerationErrorCategoryTimeout
	case errors.Is(err, context.Canceled):
		return GenerationErrorCategoryCanceled
	default:
		return GenerationErrorCategoryInternal
	}
}
