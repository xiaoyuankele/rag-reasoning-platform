package api

import (
	"context"
	"errors"
	"log/slog"
	"net/http"
	"strconv"

	"github.com/gin-gonic/gin"

	applicationdocument "rag-reasoning-platform/backend/internal/application/document"
	documentdomain "rag-reasoning-platform/backend/internal/domain/document"
)

// processingJobQueryService 定义任务查询 Handler 使用的最小能力。
type processingJobQueryService interface {
	GetByID(
		ctx context.Context,
		jobID int64,
	) (documentdomain.ProcessingJob, error)
}

// ProcessingJobHandler 负责解析任务查询 HTTP 请求。
type ProcessingJobHandler struct {
	service processingJobQueryService
	logger  *slog.Logger
}

// NewProcessingJobHandler 创建解析任务查询 Handler。
func NewProcessingJobHandler(
	service processingJobQueryService,
	logger *slog.Logger,
) *ProcessingJobHandler {
	if logger == nil {
		panic("NewProcessingJobHandler requires a non-nil logger")
	}

	return &ProcessingJobHandler{
		service: service,
		logger:  logger,
	}
}

// RegisterRoutes 注册解析任务查询路由。
func (h *ProcessingJobHandler) RegisterRoutes(
	router *gin.Engine,
) {
	router.GET("/processing-jobs/:id", h.GetByID)
}

// GetByID 处理 GET /processing-jobs/:id。
func (h *ProcessingJobHandler) GetByID(c *gin.Context) {
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

	foundJob, err := h.service.GetByID(
		c.Request.Context(),
		jobID,
	)
	if errors.Is(err, applicationdocument.ErrInvalidProcessingJobID) {
		writeErrorResponse(
			c,
			http.StatusBadRequest,
			errorCodeInvalidProcessingJobID,
			"processing job ID must be a positive integer",
		)
		return
	}

	if errors.Is(err, documentdomain.ErrProcessingJobNotFound) {
		writeErrorResponse(
			c,
			http.StatusNotFound,
			errorCodeProcessingJobNotFound,
			"processing job not found",
		)
		return
	}

	if err != nil {
		writeInternalErrorResponse(
			c,
			h.logger,
			"processing_job_get_failed",
			err,
			slog.Int64("processing_job_id", jobID),
		)
		return
	}

	c.JSON(
		http.StatusOK,
		newProcessingJobResponse(foundJob),
	)
}
