package api

import (
	"context"
	"errors"
	"net/http"
	"strconv"

	"github.com/gin-gonic/gin"

	embeddingapplication "rag-reasoning-platform/backend/internal/application/embedding"
	embeddingdomain "rag-reasoning-platform/backend/internal/domain/embedding"
)

// embeddingJobQueryService 定义 Handler 查询向量任务所需的最小应用能力。
// 生产环境可以传入 JobQueryService，测试则可以传入 fake 实现。
type embeddingJobQueryService interface {
	GetByID(
		ctx context.Context,
		jobID int64,
	) (embeddingdomain.Job, error)
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
}

// GetByID 处理 GET /embedding-jobs/:id。
func (h *EmbeddingJobHandler) GetByID(c *gin.Context) {
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
