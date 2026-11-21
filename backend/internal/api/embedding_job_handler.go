package api

import (
	"context"
	"errors"
	"net/http"
	"strconv"

	"github.com/gin-gonic/gin"

	embeddingapplication "rag-reasoning-platform/backend/internal/application/embedding"
	accessdomain "rag-reasoning-platform/backend/internal/domain/access"
	embeddingdomain "rag-reasoning-platform/backend/internal/domain/embedding"
)

// embeddingJobQueryService 定义 Handler 查询向量任务所需的最小应用能力。
// 生产环境可以传入 JobQueryService，测试则可以传入 fake 实现。
type embeddingJobQueryService interface {
	GetByID(
		ctx context.Context,
		scope accessdomain.OwnerScope,
		jobID int64,
	) (embeddingdomain.Job, error)
	GetLatestByDocumentIDs(
		ctx context.Context,
		scope accessdomain.OwnerScope,
		documentIDs []int64,
	) (embeddingapplication.LatestJobsOutput, error)
}

// EmbeddingJobHandler 负责向量任务查询的 HTTP 输入和输出。
type EmbeddingJobHandler struct {
	service embeddingJobQueryService
}

// NewEmbeddingJobHandler 创建向量任务查询 Handler。
func NewEmbeddingJobHandler(
	service embeddingJobQueryService,
) *EmbeddingJobHandler {
	return &EmbeddingJobHandler{service: service}
}

// RegisterRoutes 注册按照任务 ID 查询向量任务的路由。
func (h *EmbeddingJobHandler) RegisterRoutes(router gin.IRoutes) {
	router.GET("/embedding-jobs/:id", h.GetByID)
	router.POST("/embedding-jobs/latest", h.GetLatestByDocumentIDs)
}

type embeddingJobsLatestRequest struct {
	DocumentIDs []int64 `json:"document_ids"`
}

type embeddingJobLatestItemResponse struct {
	DocumentID int64                 `json:"document_id"`
	Job        *embeddingJobResponse `json:"job"`
}

type embeddingJobsLatestResponse struct {
	Items []embeddingJobLatestItemResponse `json:"items"`
}

// GetByID 处理 GET /embedding-jobs/:id。
func (h *EmbeddingJobHandler) GetByID(c *gin.Context) {
	scope, authenticated := ownerScopeFromContext(c)
	if !authenticated {
		writeAuthenticationRequired(c)
		return
	}

	// :id 是路径参数，因此使用 Param；Query 用于 ?id=23 这种查询参数。
	jobID, err := strconv.ParseInt(c.Param("id"), 10, 64)
	if err != nil || jobID <= 0 {
		c.JSON(http.StatusBadRequest, errorResponse{
			Error: "embedding job ID must be a positive integer",
		})
		return
	}

	foundJob, err := h.service.GetByID(
		c.Request.Context(),
		scope,
		jobID,
	)
	if errors.Is(err, embeddingapplication.ErrInvalidEmbeddingJobID) {
		c.JSON(http.StatusBadRequest, errorResponse{
			Error: "embedding job ID must be a positive integer",
		})
		return
	}

	if errors.Is(err, embeddingdomain.ErrJobNotFound) {
		c.JSON(http.StatusNotFound, errorResponse{
			Error: "embedding job not found",
		})
		return
	}

	if err != nil {
		c.JSON(http.StatusInternalServerError, errorResponse{
			Error: "internal server error",
		})
		return
	}

	c.JSON(http.StatusOK, newEmbeddingJobResponse(foundJob))
}

// GetLatestByDocumentIDs 处理 POST /embedding-jobs/latest。
func (h *EmbeddingJobHandler) GetLatestByDocumentIDs(c *gin.Context) {
	scope, authenticated := ownerScopeFromContext(c)
	if !authenticated {
		writeAuthenticationRequired(c)
		return
	}

	var request embeddingJobsLatestRequest
	if err := c.ShouldBindJSON(&request); err != nil {
		writeErrorResponse(c, http.StatusBadRequest, errorCodeInvalidEmbeddingJobLookup, "request body must contain a valid document_ids array")
		return
	}

	output, err := h.service.GetLatestByDocumentIDs(c.Request.Context(), scope, request.DocumentIDs)
	switch {
	case errors.Is(err, embeddingapplication.ErrEmptyEmbeddingJobLookup):
		writeErrorResponse(c, http.StatusBadRequest, errorCodeInvalidEmbeddingJobLookup, "document_ids must contain at least one document ID")
		return
	case errors.Is(err, embeddingapplication.ErrEmbeddingJobLookupTooLarge):
		writeErrorResponse(c, http.StatusBadRequest, errorCodeInvalidEmbeddingJobLookup, "document_ids must contain at most 100 document IDs")
		return
	case errors.Is(err, embeddingapplication.ErrInvalidDocumentID):
		writeErrorResponse(c, http.StatusBadRequest, errorCodeInvalidEmbeddingJobLookup, "every document ID must be a positive integer")
		return
	case err != nil:
		c.JSON(http.StatusInternalServerError, errorResponse{Error: "internal server error", Code: errorCodeInternal})
		return
	}

	items := make([]embeddingJobLatestItemResponse, 0, len(output.Items))
	for _, outputItem := range output.Items {
		item := embeddingJobLatestItemResponse{DocumentID: outputItem.DocumentID}
		if outputItem.Job != nil {
			jobResponse := newEmbeddingJobResponse(*outputItem.Job)
			item.Job = &jobResponse
		}
		items = append(items, item)
	}

	c.JSON(http.StatusOK, embeddingJobsLatestResponse{Items: items})
}
