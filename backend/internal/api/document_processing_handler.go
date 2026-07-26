package api

import (
	"context"
	"errors"
	"net/http"
	"strconv"
	"time"

	"github.com/gin-gonic/gin"

	applicationdocument "rag-reasoning-platform/backend/internal/application/document"
	documentdomain "rag-reasoning-platform/backend/internal/domain/document"
)

// documentProcessingQueueService 定义 Handler 创建解析任务所需的最小能力。
type documentProcessingQueueService interface {
	Queue(
		ctx context.Context,
		documentID int64,
	) (documentdomain.ProcessingJob, error)
}

// DocumentProcessingHandler 负责文档解析任务相关的 HTTP 请求。
type DocumentProcessingHandler struct {
	service documentProcessingQueueService
}

// NewDocumentProcessingHandler 创建文档解析任务 Handler。
func NewDocumentProcessingHandler(
	service documentProcessingQueueService,
) *DocumentProcessingHandler {
	return &DocumentProcessingHandler{
		service: service,
	}
}

// RegisterRoutes 注册创建文档解析任务的路由。
func (h *DocumentProcessingHandler) RegisterRoutes(
	router *gin.Engine,
) {
	router.POST("/documents/:id/process", h.Queue)
}

// processingJobResponse 是创建解析任务后返回给客户端的 JSON。
type processingJobResponse struct {
	ID           int64                              `json:"id"`
	DocumentID   int64                              `json:"document_id"`
	Status       documentdomain.ProcessingJobStatus `json:"status"`
	AttemptCount int                                `json:"attempt_count"`
	CreatedAt    time.Time                          `json:"created_at"`
}

func newProcessingJobResponse(
	job documentdomain.ProcessingJob,
) processingJobResponse {
	return processingJobResponse{
		ID:           job.ID,
		DocumentID:   job.DocumentID,
		Status:       job.Status,
		AttemptCount: job.AttemptCount,
		CreatedAt:    job.CreatedAt,
	}
}

// Queue 处理 POST /documents/:id/process。
func (h *DocumentProcessingHandler) Queue(c *gin.Context) {
	documentID, err := strconv.ParseInt(c.Param("id"), 10, 64)
	if err != nil || documentID <= 0 {
		c.JSON(http.StatusBadRequest, errorResponse{
			Error: "document ID must be a positive integer",
		})
		return
	}

	processingJob, err := h.service.Queue(
		c.Request.Context(),
		documentID,
	)
	if errors.Is(err, applicationdocument.ErrInvalidID) {
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

	if errors.Is(err, applicationdocument.ErrDocumentNotProcessable) {
		c.JSON(http.StatusConflict, errorResponse{
			Error: "document is not available for processing",
		})
		return
	}

	if errors.Is(err, documentdomain.ErrActiveProcessingJobExists) {
		c.JSON(http.StatusConflict, errorResponse{
			Error: "document processing is already queued",
		})
		return
	}

	if err != nil {
		c.JSON(http.StatusInternalServerError, errorResponse{
			Error: "internal server error",
		})
		return
	}

	c.JSON(
		http.StatusAccepted,
		newProcessingJobResponse(processingJob),
	)
}
