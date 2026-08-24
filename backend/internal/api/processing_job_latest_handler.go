package api

import (
	"context"
	"errors"
	"log/slog"
	"net/http"

	"github.com/gin-gonic/gin"

	applicationdocument "rag-reasoning-platform/backend/internal/application/document"
	accessdomain "rag-reasoning-platform/backend/internal/domain/access"
)

// processingJobLatestService 是批量状态恢复 Handler 使用的最小应用契约。
type processingJobLatestService interface {
	GetLatestByDocumentIDs(
		ctx context.Context,
		scope accessdomain.OwnerScope,
		documentIDs []int64,
	) (applicationdocument.LatestProcessingJobsOutput, error)
}

// ProcessingJobLatestHandler 负责批量恢复解析任务状态的 HTTP 边界。
type ProcessingJobLatestHandler struct {
	service processingJobLatestService
	logger  *slog.Logger
}

// NewProcessingJobLatestHandler 创建批量解析任务状态查询 Handler。
func NewProcessingJobLatestHandler(
	service processingJobLatestService,
	logger *slog.Logger,
) *ProcessingJobLatestHandler {
	if logger == nil {
		panic("NewProcessingJobLatestHandler requires a non-nil logger")
	}
	return &ProcessingJobLatestHandler{service: service, logger: logger}
}

// RegisterRoutes 注册批量恢复解析任务状态的路由。
func (h *ProcessingJobLatestHandler) RegisterRoutes(router gin.IRoutes) {
	router.POST("/processing-jobs/latest", h.GetLatestByDocumentIDs)
}

type processingJobsLatestRequest struct {
	DocumentIDs []int64 `json:"document_ids"`
}

type processingJobLatestItemResponse struct {
	DocumentID int64                  `json:"document_id"`
	Job        *processingJobResponse `json:"job"`
}

type processingJobsLatestResponse struct {
	Items []processingJobLatestItemResponse `json:"items"`
}

// GetLatestByDocumentIDs 处理 POST /processing-jobs/latest。
func (h *ProcessingJobLatestHandler) GetLatestByDocumentIDs(c *gin.Context) {
	scope, authenticated := ownerScopeFromContext(c)
	if !authenticated {
		writeAuthenticationRequired(c)
		return
	}

	var request processingJobsLatestRequest
	if err := c.ShouldBindJSON(&request); err != nil {
		writeErrorResponse(c, http.StatusBadRequest, errorCodeInvalidProcessingJobLookup, "request body must contain a valid document_ids array")
		return
	}

	output, err := h.service.GetLatestByDocumentIDs(
		c.Request.Context(),
		scope,
		request.DocumentIDs,
	)
	switch {
	case errors.Is(err, applicationdocument.ErrEmptyProcessingJobLookup):
		writeErrorResponse(c, http.StatusBadRequest, errorCodeInvalidProcessingJobLookup, "document_ids must contain at least one document ID")
		return
	case errors.Is(err, applicationdocument.ErrProcessingJobLookupTooLarge):
		writeErrorResponse(c, http.StatusBadRequest, errorCodeInvalidProcessingJobLookup, "document_ids must contain at most 100 document IDs")
		return
	case errors.Is(err, applicationdocument.ErrInvalidProcessingJobDocumentID):
		writeErrorResponse(c, http.StatusBadRequest, errorCodeInvalidProcessingJobLookup, "every document ID must be a positive integer")
		return
	case err != nil:
		writeInternalErrorResponse(
			c,
			h.logger,
			"processing_jobs_latest_failed",
			err,
			slog.Int("document_id_count", len(request.DocumentIDs)),
		)
		return
	}

	items := make([]processingJobLatestItemResponse, 0, len(output.Items))
	for _, outputItem := range output.Items {
		item := processingJobLatestItemResponse{DocumentID: outputItem.DocumentID}
		if outputItem.Job != nil {
			jobResponse := newProcessingJobResponse(*outputItem.Job)
			item.Job = &jobResponse
		}
		items = append(items, item)
	}

	c.JSON(http.StatusOK, processingJobsLatestResponse{Items: items})
}
