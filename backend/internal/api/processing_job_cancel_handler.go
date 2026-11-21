package api

import (
	"context"
	"errors"
	"log/slog"
	"net/http"
	"strconv"

	"github.com/gin-gonic/gin"

	applicationdocument "rag-reasoning-platform/backend/internal/application/document"
	accessdomain "rag-reasoning-platform/backend/internal/domain/access"
	documentdomain "rag-reasoning-platform/backend/internal/domain/document"
)

// processingJobCancelService 是取消 Handler 与 Application 之间的最小契约。
type processingJobCancelService interface {
	Cancel(
		ctx context.Context,
		scope accessdomain.OwnerScope,
		jobID int64,
	) (documentdomain.ProcessingJob, error)
}

// ProcessingJobCancelHandler 负责解析任务取消请求的 HTTP 边界。
type ProcessingJobCancelHandler struct {
	service processingJobCancelService
	logger  *slog.Logger
}

// NewProcessingJobCancelHandler 创建解析任务取消 Handler。
func NewProcessingJobCancelHandler(
	service processingJobCancelService,
	logger *slog.Logger,
) *ProcessingJobCancelHandler {
	if logger == nil {
		panic("NewProcessingJobCancelHandler requires a non-nil logger")
	}
	return &ProcessingJobCancelHandler{service: service, logger: logger}
}

// RegisterRoutes 注册取消解析任务的命令式路由。
func (h *ProcessingJobCancelHandler) RegisterRoutes(router gin.IRoutes) {
	router.POST("/processing-jobs/:id/cancel", h.Cancel)
}

// Cancel 处理 POST /processing-jobs/:id/cancel。
func (h *ProcessingJobCancelHandler) Cancel(c *gin.Context) {
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
			errorCodeInvalidProcessingJobID,
			"processing job ID must be a positive integer",
		)
		return
	}

	canceledJob, err := h.service.Cancel(c.Request.Context(), scope, jobID)
	switch {
	case errors.Is(err, applicationdocument.ErrInvalidProcessingJobID):
		writeErrorResponse(c, http.StatusBadRequest, errorCodeInvalidProcessingJobID, "processing job ID must be a positive integer")
		return
	case errors.Is(err, documentdomain.ErrProcessingJobNotFound):
		writeErrorResponse(c, http.StatusNotFound, errorCodeProcessingJobNotFound, "processing job not found")
		return
	case errors.Is(err, documentdomain.ErrProcessingJobProcessingCannotCancel):
		writeErrorResponse(c, http.StatusConflict, errorCodeProcessingJobProcessing, "processing document job cannot be canceled")
		return
	case errors.Is(err, documentdomain.ErrProcessingJobTerminalCannotCancel):
		writeErrorResponse(c, http.StatusConflict, errorCodeProcessingJobTerminal, "completed document processing job cannot be canceled")
		return
	case err != nil:
		writeInternalErrorResponse(
			c,
			h.logger,
			"processing_job_cancel_failed",
			err,
			slog.Int64("processing_job_id", jobID),
		)
		return
	}

	c.JSON(http.StatusOK, newProcessingJobResponse(canceledJob))
}
