package api

import (
	"context"
	"errors"
	"net/http"
	"strconv"
	"time"

	"github.com/gin-gonic/gin"

	embeddingapplication "rag-reasoning-platform/backend/internal/application/embedding"
	accessdomain "rag-reasoning-platform/backend/internal/domain/access"
	documentdomain "rag-reasoning-platform/backend/internal/domain/document"
	embeddingdomain "rag-reasoning-platform/backend/internal/domain/embedding"
)

// embeddingQueueService 定义 Handler 创建向量任务所需的最小应用能力。
type embeddingQueueService interface {
	Queue(
		ctx context.Context,
		scope accessdomain.OwnerScope,
		documentID int64,
	) (embeddingdomain.JobRequestResult, error)
}

// DocumentEmbeddingHandler 负责申请文档向量化的 HTTP 边界。
type DocumentEmbeddingHandler struct {
	service embeddingQueueService
}

// NewDocumentEmbeddingHandler 创建文档向量任务 Handler。
func NewDocumentEmbeddingHandler(
	service embeddingQueueService,
) *DocumentEmbeddingHandler {
	return &DocumentEmbeddingHandler{service: service}
}

// RegisterRoutes 注册申请向量化的路由。
func (h *DocumentEmbeddingHandler) RegisterRoutes(router gin.IRoutes) {
	router.POST("/documents/:id/embeddings", h.Queue)
}

// embeddingJobResponse 是保存向量化意图后返回给客户端的 JSON 契约。
type embeddingJobResponse struct {
	ID            int64                     `json:"id"`
	DocumentID    int64                     `json:"document_id"`
	ModelName     string                    `json:"model_name"`
	Dimensions    int                       `json:"dimensions"`
	Status        embeddingdomain.JobStatus `json:"status"`
	AttemptCount  int                       `json:"attempt_count"`
	ErrorMessage  *string                   `json:"error_message"`
	NextAttemptAt time.Time                 `json:"next_attempt_at"`
	PromptTokens  *int                      `json:"prompt_tokens"`
	TotalTokens   *int                      `json:"total_tokens"`
	CreatedAt     time.Time                 `json:"created_at"`
	UpdatedAt     time.Time                 `json:"updated_at"`
	StartedAt     *time.Time                `json:"started_at"`
	CompletedAt   *time.Time                `json:"completed_at"`
}

func newEmbeddingJobResponse(job embeddingdomain.Job) embeddingJobResponse {
	return embeddingJobResponse{
		ID:            job.ID,
		DocumentID:    job.DocumentID,
		ModelName:     job.ModelName,
		Dimensions:    job.Dimensions,
		Status:        job.Status,
		AttemptCount:  job.AttemptCount,
		ErrorMessage:  job.ErrorMessage,
		NextAttemptAt: job.NextAttemptAt,
		PromptTokens:  job.PromptTokens,
		TotalTokens:   job.TotalTokens,
		CreatedAt:     job.CreatedAt,
		UpdatedAt:     job.UpdatedAt,
		StartedAt:     job.StartedAt,
		CompletedAt:   job.CompletedAt,
	}
}

// Queue 处理 POST /documents/:id/embeddings。
func (h *DocumentEmbeddingHandler) Queue(c *gin.Context) {
	scope, authenticated := ownerScopeFromContext(c)
	if !authenticated {
		writeAuthenticationRequired(c)
		return
	}

	documentID, err := strconv.ParseInt(c.Param("id"), 10, 64)
	if err != nil || documentID <= 0 {
		c.JSON(http.StatusBadRequest, errorResponse{
			Error: "document ID must be a positive integer",
		})
		return
	}

	result, err := h.service.Queue(c.Request.Context(), scope, documentID)
	if errors.Is(err, embeddingapplication.ErrInvalidDocumentID) {
		c.JSON(http.StatusBadRequest, errorResponse{
			Error: "document ID must be a positive integer",
		})
		return
	}
	if errors.Is(err, documentdomain.ErrNotFound) {
		c.JSON(http.StatusNotFound, errorResponse{
			Error: "document not found",
		})
		return
	}
	if err != nil {
		c.JSON(http.StatusInternalServerError, errorResponse{
			Error: "internal server error",
		})
		return
	}

	statusCode := http.StatusAccepted
	if !result.Created {
		statusCode = http.StatusOK
	}
	c.JSON(statusCode, newEmbeddingJobResponse(result.Job))
}
