package api

import (
	"context"
	"errors"
	"log/slog"
	"net/http"
	"strconv"
	"time"

	"github.com/gin-gonic/gin"

	applicationdocument "rag-reasoning-platform/backend/internal/application/document"
	documentdomain "rag-reasoning-platform/backend/internal/domain/document"
)

// documentQueryService 定义 Handler 查询文档时需要的最小能力。
//
// 这里不直接依赖具体的 *applicationdocument.Service，
// 而是依赖一个只有 GetByID 方法的小接口，方便以后使用假服务进行测试。
type documentQueryService interface {
	GetByID(
		ctx context.Context,
		id int64,
	) (documentdomain.Document, error)
}

// DocumentHandler 负责接收文档相关的 HTTP 请求。
//
// service 保存应用服务依赖。
// Handler 只处理 HTTP 参数和响应，不直接查询 PostgreSQL。
type DocumentHandler struct {
	service documentQueryService
	logger  *slog.Logger
}

// NewDocumentHandler 创建文档 HTTP Handler。
func NewDocumentHandler(
	service documentQueryService,
	logger *slog.Logger,
) *DocumentHandler {
	if logger == nil {
		panic("NewDocumentHandler requires a non-nil logger")
	}

	return &DocumentHandler{
		service: service,
		logger:  logger,
	}
}

// RegisterRoutes 把文档接口注册到 Gin 路由。
func (h *DocumentHandler) RegisterRoutes(router *gin.Engine) {
	router.GET("/documents/:id", h.GetByID)
}

// documentResponse 定义返回给浏览器的文档 JSON。
//
// json:"..." 称为 struct tag，用来指定 JSON 字段名称。
// storage_path 是服务器内部路径，因此不放入 HTTP 响应。
type documentResponse struct {
	ID           int64                 `json:"id"`
	Title        *string               `json:"title"`
	OriginalName string                `json:"original_name"`
	MIMEType     string                `json:"mime_type"`
	SizeBytes    int64                 `json:"size_bytes"`
	SHA256       string                `json:"sha256"`
	Status       documentdomain.Status `json:"status"`
	ErrorMessage *string               `json:"error_message"`
	CreatedAt    time.Time             `json:"created_at"`
	UpdatedAt    time.Time             `json:"updated_at"`
}

// newDocumentResponse 把领域模型转换成 HTTP 响应模型。
func newDocumentResponse(
	source documentdomain.Document,
) documentResponse {
	return documentResponse{
		ID:           source.ID,
		Title:        source.Title,
		OriginalName: source.OriginalName,
		MIMEType:     source.MIMEType,
		SizeBytes:    source.SizeBytes,
		SHA256:       source.SHA256,
		Status:       source.Status,
		ErrorMessage: source.ErrorMessage,
		CreatedAt:    source.CreatedAt,
		UpdatedAt:    source.UpdatedAt,
	}
}

// GetByID 处理 GET /documents/:id 请求。
func (h *DocumentHandler) GetByID(c *gin.Context) {
	// Param 读取路由中的 :id，但得到的是字符串。
	rawID := c.Param("id")

	// ParseInt 把十进制字符串转换成 int64。
	// 第二个参数 10 表示十进制，第三个参数 64 表示结果为 int64。
	id, err := strconv.ParseInt(rawID, 10, 64)
	if err != nil || id <= 0 {
		writeErrorResponse(
			c,
			http.StatusBadRequest,
			errorCodeInvalidDocumentID,
			"document ID must be a positive integer",
		)
		return
	}

	// Request.Context 会在客户端取消请求或连接断开时收到取消信号。
	foundDocument, err := h.service.GetByID(
		c.Request.Context(),
		id,
	)
	if errors.Is(err, applicationdocument.ErrInvalidID) {
		writeErrorResponse(
			c,
			http.StatusBadRequest,
			errorCodeInvalidDocumentID,
			"document ID must be a positive integer",
		)
		return
	}

	if errors.Is(err, documentdomain.ErrNotFound) {
		writeErrorResponse(
			c,
			http.StatusNotFound,
			errorCodeDocumentNotFound,
			"document not found",
		)
		return
	}

	if err != nil {
		writeInternalErrorResponse(
			c,
			h.logger,
			"document_get_failed",
			err,
			slog.Int64("document_id", id),
		)
		return
	}

	c.JSON(
		http.StatusOK,
		newDocumentResponse(foundDocument),
	)
}
