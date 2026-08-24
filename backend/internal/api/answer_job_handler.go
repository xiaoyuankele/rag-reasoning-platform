package api

import (
	"context"
	"errors"
	"log/slog"
	"net/http"
	"strconv"
	"time"

	"github.com/gin-gonic/gin"

	answerapplication "rag-reasoning-platform/backend/internal/application/answer"
	embeddingapplication "rag-reasoning-platform/backend/internal/application/embedding"
	accessdomain "rag-reasoning-platform/backend/internal/domain/access"
	documentdomain "rag-reasoning-platform/backend/internal/domain/document"
)

// answerJobService 是 Handler 使用的异步问答应用契约。
type answerJobService interface {
	Queue(
		context.Context,
		accessdomain.OwnerScope,
		answerapplication.Input,
	) (answerapplication.Job, error)
	GetByID(
		context.Context,
		accessdomain.OwnerScope,
		int64,
	) (answerapplication.Job, error)
	Cancel(
		context.Context,
		accessdomain.OwnerScope,
		int64,
	) (answerapplication.Job, error)
}

// AnswerJobHandler 提供异步问答的创建、查询和取消 HTTP 边界。
type AnswerJobHandler struct {
	service answerJobService
	logger  *slog.Logger
}

// NewAnswerJobHandler 创建异步问答 Handler。
func NewAnswerJobHandler(
	service answerJobService,
	logger *slog.Logger,
) *AnswerJobHandler {
	if logger == nil {
		panic("NewAnswerJobHandler requires a non-nil logger")
	}
	return &AnswerJobHandler{service: service, logger: logger}
}

// RegisterRoutes 注册受认证中间件保护的异步问答路由。
func (h *AnswerJobHandler) RegisterRoutes(router gin.IRoutes) {
	router.POST("/answer-jobs", h.Queue)
	router.GET("/answer-jobs/:id", h.GetByID)
	router.POST("/answer-jobs/:id/cancel", h.Cancel)
}

type answerJobResultResponse struct {
	Answer           string                 `json:"answer"`
	ResponseLanguage string                 `json:"response_language"`
	Sources          []answerSourceResponse `json:"sources"`
	Usage            answerUsageResponse    `json:"usage"`
}

type answerJobResponse struct {
	ID                        int64                          `json:"id"`
	DocumentID                *int64                         `json:"document_id"`
	Query                     string                         `json:"query"`
	TopK                      int                            `json:"top_k"`
	RequestedResponseLanguage string                         `json:"requested_response_language"`
	Status                    answerapplication.JobStatus    `json:"status"`
	Cancelable                bool                           `json:"cancelable"`
	AttemptCount              int                            `json:"attempt_count"`
	ErrorCode                 answerapplication.JobErrorCode `json:"error_code,omitempty"`
	ErrorMessage              *string                        `json:"error_message"`
	Result                    *answerJobResultResponse       `json:"result"`
	NextAttemptAt             time.Time                      `json:"next_attempt_at"`
	CreatedAt                 time.Time                      `json:"created_at"`
	UpdatedAt                 time.Time                      `json:"updated_at"`
	StartedAt                 *time.Time                     `json:"started_at"`
	CompletedAt               *time.Time                     `json:"completed_at"`
}

func newAnswerJobResponse(job answerapplication.Job) answerJobResponse {
	response := answerJobResponse{
		ID:                        job.ID,
		DocumentID:                job.DocumentID,
		Query:                     job.Query,
		TopK:                      job.TopK,
		RequestedResponseLanguage: string(job.RequestedResponseLanguage),
		Status:                    job.Status,
		Cancelable:                job.Status == answerapplication.JobStatusQueued,
		AttemptCount:              job.AttemptCount,
		ErrorCode:                 job.ErrorCode,
		ErrorMessage:              job.ErrorMessage,
		NextAttemptAt:             job.NextAttemptAt,
		CreatedAt:                 job.CreatedAt,
		UpdatedAt:                 job.UpdatedAt,
		StartedAt:                 job.StartedAt,
		CompletedAt:               job.CompletedAt,
	}
	if job.Result != nil {
		sources := make([]answerSourceResponse, 0, len(job.Result.Sources))
		for _, source := range job.Result.Sources {
			sources = append(sources, newAnswerSourceResponse(source))
		}
		response.Result = &answerJobResultResponse{
			Answer:           job.Result.Answer,
			ResponseLanguage: string(job.Result.ResponseLanguage),
			Sources:          sources,
			Usage: answerUsageResponse{
				PromptTokens:     job.Result.PromptTokens,
				CompletionTokens: job.Result.CompletionTokens,
				TotalTokens:      job.Result.TotalTokens,
			},
		}
	}
	return response
}

// Queue 创建持久化异步问答任务。
func (h *AnswerJobHandler) Queue(c *gin.Context) {
	scope, authenticated := ownerScopeFromContext(c)
	if !authenticated {
		writeAuthenticationRequired(c)
		return
	}

	var request answerRequest
	if err := c.ShouldBindJSON(&request); err != nil {
		writeErrorResponse(
			c,
			http.StatusBadRequest,
			errorCodeInvalidAnswerJobRequest,
			"request body must be valid JSON",
		)
		return
	}
	topK := defaultAnswerTopK
	if request.TopK != nil {
		topK = *request.TopK
	}

	job, err := h.service.Queue(
		c.Request.Context(),
		scope,
		answerapplication.Input{
			Query:            request.Query,
			DocumentID:       request.DocumentID,
			TopK:             topK,
			ResponseLanguage: answerapplication.ResponseLanguage(request.ResponseLanguage),
		},
	)
	if h.writeQueueError(c, err) {
		return
	}
	c.JSON(http.StatusAccepted, newAnswerJobResponse(job))
}

// GetByID 返回当前用户可见的任务状态和可选结果。
func (h *AnswerJobHandler) GetByID(c *gin.Context) {
	scope, authenticated := ownerScopeFromContext(c)
	if !authenticated {
		writeAuthenticationRequired(c)
		return
	}
	jobID, ok := parseAnswerJobID(c)
	if !ok {
		return
	}

	job, err := h.service.GetByID(c.Request.Context(), scope, jobID)
	if h.writeLookupError(c, jobID, err) {
		return
	}
	c.JSON(http.StatusOK, newAnswerJobResponse(job))
}

// Cancel 取消当前用户的一条 queued 任务。
func (h *AnswerJobHandler) Cancel(c *gin.Context) {
	scope, authenticated := ownerScopeFromContext(c)
	if !authenticated {
		writeAuthenticationRequired(c)
		return
	}
	jobID, ok := parseAnswerJobID(c)
	if !ok {
		return
	}

	job, err := h.service.Cancel(c.Request.Context(), scope, jobID)
	switch {
	case errors.Is(err, answerapplication.ErrInvalidAnswerJobID):
		writeErrorResponse(c, http.StatusBadRequest, errorCodeInvalidAnswerJobID, "answer job ID must be a positive integer")
		return
	case errors.Is(err, answerapplication.ErrAnswerJobNotFound):
		writeErrorResponse(c, http.StatusNotFound, errorCodeAnswerJobNotFound, "answer job not found")
		return
	case errors.Is(err, answerapplication.ErrAnswerJobProcessingCannotCancel):
		writeErrorResponse(c, http.StatusConflict, errorCodeAnswerJobProcessing, "processing answer job cannot be canceled")
		return
	case errors.Is(err, answerapplication.ErrAnswerJobTerminalCannotCancel):
		writeErrorResponse(c, http.StatusConflict, errorCodeAnswerJobTerminal, "completed answer job cannot be canceled")
		return
	case err != nil:
		writeInternalErrorResponse(c, h.logger, "answer_job_cancel_failed", err, slog.Int64("answer_job_id", jobID))
		return
	}
	c.JSON(http.StatusOK, newAnswerJobResponse(job))
}

func parseAnswerJobID(c *gin.Context) (int64, bool) {
	jobID, err := strconv.ParseInt(c.Param("id"), 10, 64)
	if err != nil || jobID <= 0 {
		writeErrorResponse(c, http.StatusBadRequest, errorCodeInvalidAnswerJobID, "answer job ID must be a positive integer")
		return 0, false
	}
	return jobID, true
}

func (h *AnswerJobHandler) writeQueueError(c *gin.Context, err error) bool {
	if err == nil {
		return false
	}
	switch {
	case errors.Is(err, embeddingapplication.ErrInvalidDocumentID):
		writeErrorResponse(c, http.StatusBadRequest, errorCodeInvalidAnswerJobRequest, "document_id must be a positive integer")
	case errors.Is(err, embeddingapplication.ErrSemanticSearchQueryRequired):
		writeErrorResponse(c, http.StatusBadRequest, errorCodeInvalidAnswerJobRequest, "query is required")
	case errors.Is(err, embeddingapplication.ErrSemanticSearchQueryInvalidUTF8):
		writeErrorResponse(c, http.StatusBadRequest, errorCodeInvalidAnswerJobRequest, "query must be valid UTF-8")
	case errors.Is(err, embeddingapplication.ErrSemanticSearchQueryTooLong):
		writeErrorResponse(c, http.StatusBadRequest, errorCodeInvalidAnswerJobRequest, "query must not exceed 1000 characters")
	case errors.Is(err, embeddingapplication.ErrInvalidSemanticSearchTopK):
		writeErrorResponse(c, http.StatusBadRequest, errorCodeInvalidAnswerJobRequest, "top_k must be between 1 and 20")
	case errors.Is(err, answerapplication.ErrInvalidResponseLanguage):
		writeErrorResponse(c, http.StatusBadRequest, errorCodeInvalidAnswerJobRequest, "response_language must be auto, zh, or en")
	case errors.Is(err, documentdomain.ErrNotFound):
		writeErrorResponse(c, http.StatusNotFound, errorCodeDocumentNotFound, "document not found")
	case errors.Is(err, answerapplication.ErrAnswerOwnerQueueCapacity):
		c.Header("Retry-After", "5")
		writeErrorResponse(c, http.StatusTooManyRequests, errorCodeAnswerJobOwnerQueueCapacity, "too many queued answer jobs for this user")
	case errors.Is(err, answerapplication.ErrAnswerGlobalQueueCapacity):
		c.Header("Retry-After", "5")
		writeErrorResponse(c, http.StatusServiceUnavailable, errorCodeAnswerJobGlobalQueueCapacity, "answer job queue is temporarily full")
	default:
		writeInternalErrorResponse(c, h.logger, "answer_job_queue_failed", err)
	}
	return true
}

func (h *AnswerJobHandler) writeLookupError(
	c *gin.Context,
	jobID int64,
	err error,
) bool {
	if err == nil {
		return false
	}
	if errors.Is(err, answerapplication.ErrInvalidAnswerJobID) {
		writeErrorResponse(c, http.StatusBadRequest, errorCodeInvalidAnswerJobID, "answer job ID must be a positive integer")
		return true
	}
	if errors.Is(err, answerapplication.ErrAnswerJobNotFound) {
		writeErrorResponse(c, http.StatusNotFound, errorCodeAnswerJobNotFound, "answer job not found")
		return true
	}
	writeInternalErrorResponse(c, h.logger, "answer_job_lookup_failed", err, slog.Int64("answer_job_id", jobID))
	return true
}
