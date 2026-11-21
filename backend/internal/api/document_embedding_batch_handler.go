package api

import (
	"context"
	"errors"
	"log/slog"
	"net/http"

	"github.com/gin-gonic/gin"

	embeddingapplication "rag-reasoning-platform/backend/internal/application/embedding"
	accessdomain "rag-reasoning-platform/backend/internal/domain/access"
	documentdomain "rag-reasoning-platform/backend/internal/domain/document"
	embeddingdomain "rag-reasoning-platform/backend/internal/domain/embedding"
)

const (
	batchEmbeddingOutcomeCreated       = "created"
	batchEmbeddingOutcomeAlreadyActive = "already_active"
	batchEmbeddingOutcomeNotFound      = "not_found"
	batchEmbeddingOutcomeRateLimited   = "rate_limited"
	batchEmbeddingOutcomeCapacityFull  = "capacity_exhausted"
	batchEmbeddingOutcomeFailed        = "failed"
)

// embeddingBatchQueueService 是批量 Handler 与 Application 之间的最小契约。
type embeddingBatchQueueService interface {
	QueueBatch(
		ctx context.Context,
		scope accessdomain.OwnerScope,
		documentIDs []int64,
	) (embeddingapplication.BatchQueueOutput, error)
}

// DocumentEmbeddingBatchHandler 负责批量申请文档向量化的 HTTP 边界。
type DocumentEmbeddingBatchHandler struct {
	service embeddingBatchQueueService
	logger  *slog.Logger
}

// NewDocumentEmbeddingBatchHandler 创建批量向量化申请 Handler。
func NewDocumentEmbeddingBatchHandler(
	service embeddingBatchQueueService,
	logger *slog.Logger,
) *DocumentEmbeddingBatchHandler {
	if logger == nil {
		panic("NewDocumentEmbeddingBatchHandler requires a non-nil logger")
	}
	return &DocumentEmbeddingBatchHandler{service: service, logger: logger}
}

// RegisterRoutes 注册批量申请路由。
func (h *DocumentEmbeddingBatchHandler) RegisterRoutes(router gin.IRoutes) {
	router.POST("/embedding-jobs/batch", h.QueueBatch)
}

type embeddingBatchRequest struct {
	DocumentIDs []int64 `json:"document_ids"`
}

type embeddingBatchItemResponse struct {
	DocumentID int64                 `json:"document_id"`
	Outcome    string                `json:"outcome"`
	Job        *embeddingJobResponse `json:"job,omitempty"`
	Error      *errorResponse        `json:"error,omitempty"`
}

type embeddingBatchResponse struct {
	Items []embeddingBatchItemResponse `json:"items"`
}

// QueueBatch 处理 POST /embedding-jobs/batch。
func (h *DocumentEmbeddingBatchHandler) QueueBatch(c *gin.Context) {
	scope, authenticated := ownerScopeFromContext(c)
	if !authenticated {
		writeAuthenticationRequired(c)
		return
	}

	var request embeddingBatchRequest
	if err := c.ShouldBindJSON(&request); err != nil {
		writeErrorResponse(c, http.StatusBadRequest, errorCodeInvalidEmbeddingBatch, "request body must contain a valid document_ids array")
		return
	}

	output, err := h.service.QueueBatch(c.Request.Context(), scope, request.DocumentIDs)
	switch {
	case errors.Is(err, embeddingapplication.ErrEmptyEmbeddingBatch):
		writeErrorResponse(c, http.StatusBadRequest, errorCodeInvalidEmbeddingBatch, "document_ids must contain at least one document ID")
		return
	case errors.Is(err, embeddingapplication.ErrEmbeddingBatchTooLarge):
		writeErrorResponse(c, http.StatusBadRequest, errorCodeInvalidEmbeddingBatch, "document_ids must contain at most 100 document IDs")
		return
	case errors.Is(err, embeddingapplication.ErrInvalidDocumentID):
		writeErrorResponse(c, http.StatusBadRequest, errorCodeInvalidEmbeddingBatch, "every document ID must be a positive integer")
		return
	case err != nil:
		writeInternalErrorResponse(c, h.logger, "embedding_batch_queue_failed", err)
		return
	}

	items := make([]embeddingBatchItemResponse, 0, len(output.Items))
	for _, item := range output.Items {
		responseItem := embeddingBatchItemResponse{DocumentID: item.DocumentID}
		switch {
		case item.Err == nil:
			jobResponse := newEmbeddingJobResponse(item.Result.Job)
			responseItem.Job = &jobResponse
			responseItem.Outcome = batchEmbeddingOutcomeCreated
			if !item.Result.Created {
				responseItem.Outcome = batchEmbeddingOutcomeAlreadyActive
			}
		case errors.Is(item.Err, documentdomain.ErrNotFound):
			responseItem.Outcome = batchEmbeddingOutcomeNotFound
			responseItem.Error = &errorResponse{Error: "document not found", Code: errorCodeDocumentNotFound}
		case errors.Is(item.Err, embeddingdomain.ErrOwnerActiveJobLimitExceeded):
			responseItem.Outcome = batchEmbeddingOutcomeRateLimited
			responseItem.Error = &errorResponse{
				Error: "too many active embedding jobs for this user",
				Code:  errorCodeEmbeddingOwnerJobLimit,
			}
			c.Header("Retry-After", "5")
		case errors.Is(item.Err, embeddingdomain.ErrGlobalActiveJobLimitExceeded):
			responseItem.Outcome = batchEmbeddingOutcomeCapacityFull
			responseItem.Error = &errorResponse{
				Error: "embedding queue is temporarily full",
				Code:  errorCodeEmbeddingQueueCapacity,
			}
			c.Header("Retry-After", "5")
		default:
			responseItem.Outcome = batchEmbeddingOutcomeFailed
			responseItem.Error = &errorResponse{Error: "internal server error", Code: errorCodeInternal}
			h.logger.ErrorContext(
				c.Request.Context(),
				"Embedding batch item failed",
				"event", "embedding_batch_item_failed",
				"document_id", item.DocumentID,
				"error", item.Err,
			)
		}
		items = append(items, responseItem)
	}

	c.JSON(http.StatusOK, embeddingBatchResponse{Items: items})
}
