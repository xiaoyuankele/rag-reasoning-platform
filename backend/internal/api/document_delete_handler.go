package api

import (
	"context"
	"errors"
	"net/http"
	"strconv"

	"github.com/gin-gonic/gin"

	applicationdocument "rag-reasoning-platform/backend/internal/application/document"
	accessdomain "rag-reasoning-platform/backend/internal/domain/access"
	documentdomain "rag-reasoning-platform/backend/internal/domain/document"
)

// documentDeleteService 定义删除 Handler 需要的最小应用能力。
type documentDeleteService interface {
	Delete(ctx context.Context, scope accessdomain.OwnerScope, id int64) error
}

// DocumentDeleteHandler 负责把 HTTP 删除请求转换成应用服务调用。
type DocumentDeleteHandler struct {
	service documentDeleteService
}

// NewDocumentDeleteHandler 创建文档删除 Handler。
func NewDocumentDeleteHandler(
	service documentDeleteService,
) *DocumentDeleteHandler {
	return &DocumentDeleteHandler{
		service: service,
	}
}

// RegisterRoutes 注册文档删除路由。
func (h *DocumentDeleteHandler) RegisterRoutes(router gin.IRoutes) {
	router.DELETE("/documents/:id", h.Delete)
}

// Delete 处理 DELETE /documents/:id。
func (h *DocumentDeleteHandler) Delete(c *gin.Context) {
	scope, authenticated := ownerScopeFromContext(c)
	if !authenticated {
		writeAuthenticationRequired(c)
		return
	}

	rawID := c.Param("id")
	id, err := strconv.ParseInt(rawID, 10, 64)
	if err != nil || id <= 0 {
		c.JSON(http.StatusBadRequest, errorResponse{
			Error: "document ID must be a positive integer",
		})
		return
	}

	err = h.service.Delete(c.Request.Context(), scope, id)
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

	if err != nil {
		c.JSON(http.StatusInternalServerError, errorResponse{
			Error: "internal server error",
		})
		return
	}

	// 204 表示删除成功，响应体必须为空。
	c.Status(http.StatusNoContent)
}
