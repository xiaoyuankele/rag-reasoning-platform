package api

import (
	"context"
	"errors"
	"net/http"
	"strconv"
	"time"

	"github.com/gin-gonic/gin"

	applicationdocument "rag-reasoning-platform/backend/internal/application/document"
	accessdomain "rag-reasoning-platform/backend/internal/domain/access"
	documentdomain "rag-reasoning-platform/backend/internal/domain/document"
)

// documentChunkListService 定义 Handler 浏览文本块所需的最小应用能力。
type documentChunkListService interface {
	List(
		ctx context.Context,
		scope accessdomain.OwnerScope,
		input applicationdocument.ChunkListInput,
	) (applicationdocument.ChunkListOutput, error)
}

// DocumentChunkHandler 负责文档文本块分页接口的 HTTP 边界。
type DocumentChunkHandler struct {
	service documentChunkListService
}

// NewDocumentChunkHandler 创建文档文本块 Handler。
func NewDocumentChunkHandler(
	service documentChunkListService,
) *DocumentChunkHandler {
	return &DocumentChunkHandler{service: service}
}

// RegisterRoutes 注册文档文本块分页路由。
func (h *DocumentChunkHandler) RegisterRoutes(router gin.IRoutes) {
	router.GET("/documents/:id/chunks", h.List)
}

// documentChunkResponse 是单个文本块的公开 HTTP DTO。
type documentChunkResponse struct {
	ChunkID    int64     `json:"chunk_id"`
	ChunkIndex int       `json:"chunk_index"`
	Content    string    `json:"content"`
	PageStart  *int      `json:"page_start"`
	PageEnd    *int      `json:"page_end"`
	CreatedAt  time.Time `json:"created_at"`
}

// documentChunkListResponse 是文本块分页接口的完整响应。
type documentChunkListResponse struct {
	DocumentID int64                   `json:"document_id"`
	Chunks     []documentChunkResponse `json:"chunks"`
	Pagination paginationResponse      `json:"pagination"`
}

// List 处理 GET /documents/:id/chunks。
func (h *DocumentChunkHandler) List(c *gin.Context) {
	scope, authenticated := ownerScopeFromContext(c)
	if !authenticated {
		writeAuthenticationRequired(c)
		return
	}

	documentID, err := strconv.ParseInt(c.Param("id"), 10, 64)
	if err != nil || documentID <= 0 {
		c.JSON(http.StatusBadRequest, errorResponse{
			Error: "document ID must be a positive integer",
		})
		return
	}

	page, err := parsePositiveQueryInt(
		c,
		"page",
		applicationdocument.DefaultPage,
	)
	if err != nil {
		c.JSON(http.StatusBadRequest, errorResponse{
			Error: "page must be a positive integer",
		})
		return
	}

	pageSize, err := parsePositiveQueryInt(
		c,
		"page_size",
		applicationdocument.DefaultPageSize,
	)
	if err != nil {
		c.JSON(http.StatusBadRequest, errorResponse{
			Error: "page_size must be a positive integer",
		})
		return
	}

	result, err := h.service.List(
		c.Request.Context(),
		scope,
		applicationdocument.ChunkListInput{
			DocumentID: documentID,
			Page:       page,
			PageSize:   pageSize,
		},
	)
	switch {
	case errors.Is(err, applicationdocument.ErrInvalidID):
		c.JSON(http.StatusBadRequest, errorResponse{
			Error: "document ID must be a positive integer",
		})
		return
	case errors.Is(err, applicationdocument.ErrInvalidPage):
		c.JSON(http.StatusBadRequest, errorResponse{
			Error: "page must be a positive integer",
		})
		return
	case errors.Is(err, applicationdocument.ErrInvalidPageSize):
		c.JSON(http.StatusBadRequest, errorResponse{
			Error: "page_size must be between 1 and 100",
		})
		return
	case errors.Is(err, documentdomain.ErrNotFound):
		c.JSON(http.StatusNotFound, errorResponse{
			Error: "document not found",
		})
		return
	case errors.Is(err, applicationdocument.ErrDocumentChunksNotReady):
		c.JSON(http.StatusConflict, errorResponse{
			Error: "document chunks are not ready",
		})
		return
	case err != nil:
		c.JSON(http.StatusInternalServerError, errorResponse{
			Error: "internal server error",
		})
		return
	}

	chunks := make([]documentChunkResponse, 0, len(result.Chunks))
	for _, sourceChunk := range result.Chunks {
		chunks = append(chunks, newDocumentChunkResponse(sourceChunk))
	}

	c.JSON(http.StatusOK, documentChunkListResponse{
		DocumentID: result.DocumentID,
		Chunks:     chunks,
		Pagination: paginationResponse{
			Page:       result.Page,
			PageSize:   result.PageSize,
			Total:      result.Total,
			TotalPages: result.TotalPages,
		},
	})
}

// newDocumentChunkResponse 把领域文本块转换成公开 HTTP DTO。
func newDocumentChunkResponse(
	source documentdomain.TextChunk,
) documentChunkResponse {
	return documentChunkResponse{
		ChunkID:    source.ID,
		ChunkIndex: source.Index,
		Content:    source.Content,
		PageStart:  source.PageStart,
		PageEnd:    source.PageEnd,
		CreatedAt:  source.CreatedAt,
	}
}
