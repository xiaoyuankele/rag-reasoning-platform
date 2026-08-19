package api

import (
	"context"
	"errors"
	"log/slog"
	"net/http"
	"strconv"

	"github.com/gin-gonic/gin"

	embeddingapplication "rag-reasoning-platform/backend/internal/application/embedding"
	accessdomain "rag-reasoning-platform/backend/internal/domain/access"
	embeddingdomain "rag-reasoning-platform/backend/internal/domain/embedding"
)

// embeddingJobCancelService 是取消 Handler 与 Application 之间的最小契约。
type embeddingJobCancelService interface {
	Cancel(
		ctx context.Context,
		scope accessdomain.OwnerScope,
		jobID int64,
	) (embeddingdomain.Job, error)
}

// EmbeddingJobCancelHandler 负责向量任务取消请求的 HTTP 边界。
type EmbeddingJobCancelHandler struct {
	service embeddingJobCancelService
	logger  *slog.Logger
}

// NewEmbeddingJobCancelHandler 创建向量任务取消 Handler。
func NewEmbeddingJobCancelHandler(
	service embeddingJobCancelService,
	logger *slog.Logger,
) *EmbeddingJobCancelHandler {
	if logger == nil {
		panic("NewEmbeddingJobCancelHandler requires a non-nil logger")
	}
	return &EmbeddingJobCancelHandler{service: service, logger: logger}
}

// RegisterRoutes 注册取消向量任务的命令式路由。
func (h *EmbeddingJobCancelHandler) RegisterRoutes(router gin.IRoutes) {
	router.POST("/embedding-jobs/:id/cancel", h.Cancel)
}

// Cancel 处理 POST /embedding-jobs/:id/cancel。
func (h *EmbeddingJobCancelHandler) Cancel(c *gin.Context) {
	scope, authenticated := ownerScopeFromContext(c)
	if !authenticated {
		writeAuthenticationRequired(c)
		return
	}

	jobID, err := strconv.ParseInt(c.Param("id"), 10, 64)
	if err != nil || jobID <= 0 {
		writeErrorResponse(
			c,
			http.StatusBadRequest,
			errorCodeInvalidEmbeddingJobID,
			"embedding job ID must be a positive integer",
		)
		return
	}

	canceledJob, err := h.service.Cancel(c.Request.Context(), scope, jobID)
	switch {
	case errors.Is(err, embeddingapplication.ErrInvalidEmbeddingJobID):
		writeErrorResponse(c, http.StatusBadRequest, errorCodeInvalidEmbeddingJobID, "embedding job ID must be a positive integer")
		return
	case errors.Is(err, embeddingdomain.ErrJobNotFound):
		writeErrorResponse(c, http.StatusNotFound, errorCodeEmbeddingJobNotFound, "embedding job not found")
		return
	case errors.Is(err, embeddingdomain.ErrJobProcessingCannotCancel):
		writeErrorResponse(c, http.StatusConflict, errorCodeEmbeddingJobProcessing, "processing embedding job cannot be canceled")
		return
	case errors.Is(err, embeddingdomain.ErrJobTerminalCannotCancel):
		writeErrorResponse(c, http.StatusConflict, errorCodeEmbeddingJobTerminal, "completed embedding job cannot be canceled")
		return
	case err != nil:
		writeInternalErrorResponse(
			c,
			h.logger,
			"embedding_job_cancel_failed",
			err,
			slog.Int64("embedding_job_id", jobID),
		)
		return
	}

	c.JSON(http.StatusOK, newEmbeddingJobResponse(canceledJob))
}
