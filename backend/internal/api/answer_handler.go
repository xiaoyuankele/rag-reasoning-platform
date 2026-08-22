package api

import (
	"context"
	"errors"
	"net/http"

	"github.com/gin-gonic/gin"

	answerapplication "rag-reasoning-platform/backend/internal/application/answer"
	embeddingapplication "rag-reasoning-platform/backend/internal/application/embedding"
	accessdomain "rag-reasoning-platform/backend/internal/domain/access"
	documentdomain "rag-reasoning-platform/backend/internal/domain/document"
	embeddingdomain "rag-reasoning-platform/backend/internal/domain/embedding"
	generationdomain "rag-reasoning-platform/backend/internal/domain/generation"
)

const defaultAnswerTopK = 5

// answerService 是 AnswerHandler 与 Application 之间的最小插口契约。
//
// 生产环境由 *answer.Service 实现；Handler 单元测试可以传入 Fake，
// 因此测试不需要连接 PostgreSQL，也不会调用远程模型。
type answerService interface {
	Answer(
		ctx context.Context,
		scope accessdomain.OwnerScope,
		input answerapplication.Input,
	) (answerapplication.Output, error)
}

// AnswerHandler 负责 POST /answers 的 HTTP 边界转换。
type AnswerHandler struct {
	service answerService
}

// NewAnswerHandler 创建带来源问答 Handler，并注入 Application 服务。
func NewAnswerHandler(service answerService) *AnswerHandler {
	return &AnswerHandler{service: service}
}

// RegisterRoutes 注册带来源问答路由。
func (h *AnswerHandler) RegisterRoutes(router gin.IRoutes) {
	router.POST("/answers", h.Answer)
}

// answerRequest 是客户端提交的 JSON DTO。
//
// TopK 使用指针是为了区分“没有提供”和“明确提供 0”：省略时使用默认值 5，
// 显式传 0 时保留原值，让 Application 返回稳定的参数错误。
type answerRequest struct {
	Query            string `json:"query"`
	DocumentID       *int64 `json:"document_id"`
	TopK             *int   `json:"top_k"`
	ResponseLanguage string `json:"response_language"`
}

// answerSourceResponse 是一条可供前端展示和定位的引用来源。
type answerSourceResponse struct {
	Citation     int     `json:"citation"`
	ChunkID      int64   `json:"chunk_id"`
	DocumentID   int64   `json:"document_id"`
	ChunkIndex   int     `json:"chunk_index"`
	Title        *string `json:"title"`
	OriginalName string  `json:"original_name"`
	PageStart    *int    `json:"page_start"`
	PageEnd      *int    `json:"page_end"`
	Similarity   float64 `json:"similarity"`
}

// answerUsageResponse 暴露本次生成调用的 Token 用量，便于观察成本。
// 无证据降级时不会调用模型，因此三个字段都为 0。
type answerUsageResponse struct {
	PromptTokens     int `json:"prompt_tokens"`
	CompletionTokens int `json:"completion_tokens"`
	TotalTokens      int `json:"total_tokens"`
}

// answerResponse 是问答成功时返回给客户端的完整 JSON DTO。
type answerResponse struct {
	Query            string                 `json:"query"`
	Answer           string                 `json:"answer"`
	ResponseLanguage string                 `json:"response_language"`
	Sources          []answerSourceResponse `json:"sources"`
	Usage            answerUsageResponse    `json:"usage"`
}

// Answer 绑定 JSON、调用 Application，并把统一结果转换成 HTTP 响应。
func (h *AnswerHandler) Answer(c *gin.Context) {
	scope, authenticated := ownerScopeFromContext(c)
	if !authenticated {
		writeAuthenticationRequired(c)
		return
	}

	var request answerRequest
	if err := c.ShouldBindJSON(&request); err != nil {
		c.JSON(http.StatusBadRequest, errorResponse{
			Error: "request body must be valid JSON",
		})
		return
	}

	topK := defaultAnswerTopK
	if request.TopK != nil {
		topK = *request.TopK
	}

	result, err := h.service.Answer(
		c.Request.Context(),
		scope,
		answerapplication.Input{
			Query:            request.Query,
			DocumentID:       request.DocumentID,
			TopK:             topK,
			ResponseLanguage: answerapplication.ResponseLanguage(request.ResponseLanguage),
		},
	)
	if writeAnswerError(c, err) {
		return
	}

	sources := make([]answerSourceResponse, 0, len(result.Sources))
	for _, source := range result.Sources {
		sources = append(sources, newAnswerSourceResponse(source))
	}

	c.JSON(http.StatusOK, answerResponse{
		Query:            result.Query,
		Answer:           result.Answer,
		ResponseLanguage: string(result.ResponseLanguage),
		Sources:          sources,
		Usage: answerUsageResponse{
			PromptTokens:     result.PromptTokens,
			CompletionTokens: result.CompletionTokens,
			TotalTokens:      result.TotalTokens,
		},
	})
}

// writeAnswerError 把跨层稳定错误映射成安全的 HTTP 状态和公开说明。
// 返回 true 表示已经写入错误响应，调用方必须立即结束当前 Handler。
func writeAnswerError(c *gin.Context, err error) bool {
	if err == nil {
		return false
	}

	switch {
	case errors.Is(err, embeddingapplication.ErrInvalidDocumentID):
		c.JSON(http.StatusBadRequest, errorResponse{
			Error: "document_id must be a positive integer",
		})
	case errors.Is(err, embeddingapplication.ErrSemanticSearchQueryRequired):
		c.JSON(http.StatusBadRequest, errorResponse{Error: "query is required"})
	case errors.Is(err, embeddingapplication.ErrSemanticSearchQueryInvalidUTF8):
		c.JSON(http.StatusBadRequest, errorResponse{
			Error: "query must be valid UTF-8",
		})
	case errors.Is(err, embeddingapplication.ErrSemanticSearchQueryTooLong):
		c.JSON(http.StatusBadRequest, errorResponse{
			Error: "query must not exceed 1000 characters",
		})
	case errors.Is(err, embeddingapplication.ErrInvalidSemanticSearchTopK):
		c.JSON(http.StatusBadRequest, errorResponse{
			Error: "top_k must be between 1 and 20",
		})
	case errors.Is(err, documentdomain.ErrNotFound):
		c.JSON(http.StatusNotFound, errorResponse{
			Error: "document not found",
		})
	case errors.Is(err, embeddingapplication.ErrDocumentEmbeddingsNotReady):
		c.JSON(http.StatusConflict, errorResponse{
			Error: "document embeddings are not ready",
		})

	case errors.Is(err, embeddingdomain.ErrEmbeddingRequestRejected),
		errors.Is(err, embeddingdomain.ErrInvalidEmbeddingResponse):
		_ = c.Error(err)
		c.JSON(http.StatusBadGateway, errorResponse{
			Error: "embedding provider returned an invalid response",
		})

	case errors.Is(err, generationdomain.ErrGenerationRequestRejected),
		errors.Is(err, generationdomain.ErrInvalidGenerationResponse):
		_ = c.Error(err)
		c.JSON(http.StatusBadGateway, errorResponse{
			Error: "generation provider returned an invalid response",
		})

	case errors.Is(err, answerapplication.ErrInvalidResponseLanguage):
		c.JSON(http.StatusBadRequest, errorResponse{
			Error: "response_language must be auto, zh, or en",
		})
	case errors.Is(err, answerapplication.ErrAnswerCapacityExhausted):
		// Retry-After 使用秒数，提示客户端短暂退避后再提交，而不是立即重试。
		c.Header("Retry-After", "2")
		writeErrorResponse(
			c,
			http.StatusServiceUnavailable,
			errorCodeAnswerCapacityExhausted,
			"answer service is busy; try again later",
		)
	case errors.Is(err, embeddingapplication.ErrEmbeddingProviderCapacityExhausted):
		// 问答已经进入整体并发槽位，但内部语义检索仍可能因共享的远程
		// Embedding 容量已满而超时。该错误与问答整体容量错误分开编码。
		c.Header("Retry-After", "2")
		writeErrorResponse(
			c,
			http.StatusServiceUnavailable,
			errorCodeEmbeddingProviderCapacity,
			"embedding service is busy; try again later",
		)

	case errors.Is(err, embeddingdomain.ErrEmbeddingAuthentication),
		errors.Is(err, embeddingdomain.ErrEmbeddingRateLimited),
		errors.Is(err, embeddingdomain.ErrEmbeddingQuotaExceeded),
		errors.Is(err, embeddingdomain.ErrEmbeddingUnavailable),
		errors.Is(err, generationdomain.ErrGenerationAuthentication),
		errors.Is(err, generationdomain.ErrGenerationRateLimited),
		errors.Is(err, generationdomain.ErrGenerationQuotaExceeded),
		errors.Is(err, generationdomain.ErrGenerationUnavailable):
		_ = c.Error(err)
		c.JSON(http.StatusServiceUnavailable, errorResponse{
			Error: "answer service is temporarily unavailable",
		})

	default:
		_ = c.Error(err)
		c.JSON(http.StatusInternalServerError, errorResponse{
			Error: "internal server error",
		})
	}

	return true
}

// newAnswerSourceResponse 把 Application 来源转换为对外 HTTP DTO。
func newAnswerSourceResponse(
	source answerapplication.Source,
) answerSourceResponse {
	return answerSourceResponse{
		Citation:     source.Citation,
		ChunkID:      source.ChunkID,
		DocumentID:   source.DocumentID,
		ChunkIndex:   source.ChunkIndex,
		Title:        source.Title,
		OriginalName: source.OriginalName,
		PageStart:    source.PageStart,
		PageEnd:      source.PageEnd,
		Similarity:   source.Similarity,
	}
}
