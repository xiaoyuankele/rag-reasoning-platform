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

// documentPreflightService 定义预检 Handler 依赖的最小应用能力。
type documentPreflightService interface {
	Check(
		ctx context.Context,
		scope accessdomain.OwnerScope,
		input applicationdocument.PreflightInput,
	) (applicationdocument.PreflightResult, error)
}

// documentPreflightRequest 是浏览器在上传文件正文前发送的 JSON 契约。
type documentPreflightRequest struct {
	SHA256    string `json:"sha256"`
	SizeBytes int64  `json:"size_bytes"`
}

// documentPreflightResponse 始终返回稳定的 document 字段。
// 未命中时 document 为 null；命中时它是当前用户已有文档的安全 HTTP DTO。
type documentPreflightResponse struct {
	Exists   bool              `json:"exists"`
	Document *documentResponse `json:"document"`
}

// DocumentPreflightHandler 负责上传前查重的 HTTP 适配。
type DocumentPreflightHandler struct {
	service documentPreflightService
	logger  *slog.Logger
}

// NewDocumentPreflightHandler 创建上传前查重 Handler。
func NewDocumentPreflightHandler(
	service documentPreflightService,
	logger *slog.Logger,
) *DocumentPreflightHandler {
	if logger == nil {
		panic("NewDocumentPreflightHandler requires a non-nil logger")
	}

	return &DocumentPreflightHandler{service: service, logger: logger}
}

// RegisterRoutes 注册受 Session 保护的上传前预检路由。
func (h *DocumentPreflightHandler) RegisterRoutes(router gin.IRoutes) {
	router.POST("/documents/preflight", h.Check)
}

// Check 处理 POST /documents/preflight。
func (h *DocumentPreflightHandler) Check(c *gin.Context) {
	scope, authenticated := ownerScopeFromContext(c)
	if !authenticated {
		writeAuthenticationRequired(c)
		return
	}

	var request documentPreflightRequest
	if err := c.ShouldBindJSON(&request); err != nil {
		writeErrorResponse(
			c,
			http.StatusBadRequest,
			errorCodeInvalidDocumentPreflight,
			"document preflight request is invalid",
		)
		return
	}

	result, err := h.service.Check(
		c.Request.Context(),
		scope,
		applicationdocument.PreflightInput{
			SHA256:    request.SHA256,
			SizeBytes: request.SizeBytes,
		},
	)
	switch {
	case errors.Is(err, applicationdocument.ErrInvalidPreflightSHA256),
		errors.Is(err, applicationdocument.ErrInvalidPreflightSize):
		writeErrorResponse(
			c,
			http.StatusBadRequest,
			errorCodeInvalidDocumentPreflight,
			"document preflight request is invalid",
		)
		return

	case errors.Is(err, applicationdocument.ErrFileTooLarge):
		writeErrorResponse(
			c,
			http.StatusRequestEntityTooLarge,
			errorCodeFileTooLarge,
			"file exceeds maximum allowed size",
		)
		return

	case err != nil:
		writeInternalErrorResponse(
			c,
			h.logger,
			"document_preflight_failed",
			err,
			slog.Int64("file_size_bytes", request.SizeBytes),
		)
		return
	}

	response := documentPreflightResponse{Exists: result.Exists}
	if result.Exists {
		document := newDocumentResponse(result.Document)
		response.Document = &document
	}

	c.JSON(http.StatusOK, response)
}
