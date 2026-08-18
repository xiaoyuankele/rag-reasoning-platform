package api

import (
	"context"
	"errors"
	"io"
	"net/http"

	"github.com/gin-gonic/gin"

	applicationdocument "rag-reasoning-platform/backend/internal/application/document"
	accessdomain "rag-reasoning-platform/backend/internal/domain/access"
)

// multipartOverheadAllowanceBytes 为 multipart 边界和请求头预留空间。
//
// 文件自身仍由 LocalStorage 按 MaxFileSizeBytes 精确限制。
const multipartOverheadAllowanceBytes int64 = 1024 * 1024

// documentUploadService 定义上传 Handler 需要的最小应用能力
type documentUploadService interface {
	Upload(
		ctx context.Context,
		scope accessdomain.OwnerScope,
		input applicationdocument.UploadInput,
	) (applicationdocument.UploadResult, error)
}

// documentUploadResponse 在通用文档响应上增加本次是否命中重复内容。
// 嵌入 documentResponse 会把文档字段平铺到同一层 JSON，保持原接口兼容。
type documentUploadResponse struct {
	documentResponse
	Duplicate bool `json:"duplicate"`
}

// DocumentUploadHandler 负责接收文档上传 HTTP 请求。
type DocumentUploadHandler struct {
	service          documentUploadService
	maxFileSizeBytes int64
}

// NewDocumentUploadHandler 创建文档上传 Handler。
func NewDocumentUploadHandler(
	service documentUploadService,
	maxFileSizeBytes int64,
) *DocumentUploadHandler {
	return &DocumentUploadHandler{
		service:          service,
		maxFileSizeBytes: maxFileSizeBytes,
	}
}

// RegisterRoutes 注册文档上传路由。
func (h *DocumentUploadHandler) RegisterRoutes(router gin.IRoutes) {
	router.POST("/documents", h.Upload)
}

// Upload 处理 POST /documents multipart 上传请求。
func (h *DocumentUploadHandler) Upload(c *gin.Context) {
	scope, authenticated := ownerScopeFromContext(c)
	if !authenticated {
		writeAuthenticationRequired(c)
		return
	}

	maxRequestBodyBytes := h.maxFileSizeBytes + multipartOverheadAllowanceBytes

	// MaxBytesReader 限制整个 HTTP 请求体，
	// 防止客户端发送无限大的 multipart 请求。
	c.Request.Body = http.MaxBytesReader(
		c.Writer,
		c.Request.Body,
		maxRequestBodyBytes,
	)

	// MultipartReader 返回流式 multipart 读取器，
	// 不会先把整个 200 MiB 文件读入 []byte。
	multipartReader, err := c.Request.MultipartReader()
	if err != nil {
		c.JSON(http.StatusBadRequest, errorResponse{
			Error: "invalid multipart request",
		})
		return
	}

	for {
		part, err := multipartReader.NextPart()
		if errors.Is(err, io.EOF) {
			c.JSON(http.StatusBadRequest, errorResponse{
				Error: "file field is required",
			})
			return
		}
		if err != nil {
			c.JSON(http.StatusBadRequest, errorResponse{
				Error: "invalid multipart request",
			})
			return
		}

		// 跳过普通表单字段，只处理名称为 file 的文件字段。
		if part.FormName() != "file" {
			_ = part.Close()
			continue
		}

		originalName := part.FileName()
		if originalName == "" {
			_ = part.Close()

			c.JSON(http.StatusBadRequest, errorResponse{
				Error: "file name is required",
			})
			return
		}

		// Upload 会在返回前同步读取 part，因此可以在方法结束时关闭。
		defer part.Close()

		uploadResult, err := h.service.Upload(
			c.Request.Context(),
			scope,
			applicationdocument.UploadInput{
				OriginalName: originalName,
				Content:      part,
			},
		)

		if err != nil {
			var maxBytesError *http.MaxBytesError

			switch {
			case errors.As(err, &maxBytesError):
				c.JSON(http.StatusRequestEntityTooLarge, errorResponse{
					Error: "request body exceeds maximum allowed size",
				})
			case errors.Is(err, applicationdocument.ErrOriginalNameRequired):
				c.JSON(http.StatusBadRequest, errorResponse{
					Error: "file name is required",
				})
			case errors.Is(err, applicationdocument.ErrFileContentRequired):
				c.JSON(http.StatusBadRequest, errorResponse{
					Error: "file content is required",
				})
			case errors.Is(err, applicationdocument.ErrInvalidPDFContent):
				c.JSON(http.StatusUnsupportedMediaType, errorResponse{
					Error: "file content must be a PDF",
				})
			case errors.Is(err, applicationdocument.ErrUnsupportedFileType):
				c.JSON(http.StatusUnsupportedMediaType, errorResponse{
					Error: "file type must be PDF, Markdown, or plain text",
				})
			case errors.Is(err, applicationdocument.ErrInvalidTextContent):
				c.JSON(http.StatusUnsupportedMediaType, errorResponse{
					Error: "text file content must be valid UTF-8",
				})
			case errors.Is(err, applicationdocument.ErrFileTooLarge):
				c.JSON(http.StatusRequestEntityTooLarge, errorResponse{
					Error: "file exceeds maximum allowed size",
				})
			default:
				c.JSON(http.StatusInternalServerError, errorResponse{
					Error: "internal server error",
				})
			}

			return
		}

		statusCode := http.StatusCreated
		if uploadResult.Duplicate {
			statusCode = http.StatusOK
		}

		c.JSON(statusCode, documentUploadResponse{
			documentResponse: newDocumentResponse(uploadResult.Document),
			Duplicate:        uploadResult.Duplicate,
		})
		return
	}
}
