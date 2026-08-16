package api

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"strconv"

	"github.com/gin-gonic/gin"

	applicationdocument "rag-reasoning-platform/backend/internal/application/document"
	accessdomain "rag-reasoning-platform/backend/internal/domain/access"
)

// documentListService 定义列表 Handler 使用的最小应用能力。
type documentListService interface {
	List(
		ctx context.Context,
		scope accessdomain.OwnerScope,
		input applicationdocument.ListInput,
	) (applicationdocument.ListOutput, error)
}

// DocumentListHandler 负责接收文档列表 HTTP 请求。
type DocumentListHandler struct {
	service documentListService
}

// NewDocumentListHandler 创建文档列表 Handler。
func NewDocumentListHandler(
	service documentListService,
) *DocumentListHandler {
	return &DocumentListHandler{
		service: service,
	}
}

// RegisterRoutes 注册文档列表路由。
func (h *DocumentListHandler) RegisterRoutes(router gin.IRoutes) {
	router.GET("/documents", h.List)
}

// paginationResponse 定义分页信息的 JSON 结构。
type paginationResponse struct {
	Page       int64 `json:"page"`
	PageSize   int64 `json:"page_size"`
	Total      int64 `json:"total"`
	TotalPages int64 `json:"total_pages"`
}

// documentListResponse 定义列表接口的完整 JSON 结构。
type documentListResponse struct {
	Documents  []documentResponse `json:"documents"`
	Pagination paginationResponse `json:"pagination"`
}

// List 处理 GET /documents 请求。
func (h *DocumentListHandler) List(c *gin.Context) {
	scope, authenticated := ownerScopeFromContext(c)
	if !authenticated {
		writeAuthenticationRequired(c)
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
		applicationdocument.ListInput{
			Page:     page,
			PageSize: pageSize,
		},
	)
	if errors.Is(err, applicationdocument.ErrInvalidPage) {
		c.JSON(http.StatusBadRequest, errorResponse{
			Error: "page must be a positive integer",
		})
		return
	}

	if errors.Is(err, applicationdocument.ErrInvalidPageSize) {
		c.JSON(http.StatusBadRequest, errorResponse{
			Error: "page_size must be between 1 and 100",
		})
		return
	}

	if err != nil {
		c.JSON(http.StatusInternalServerError, errorResponse{
			Error: "internal server error",
		})
		return
	}

	// 即使应用层返回 nil，这里也初始化为空切片，
	// 确保 JSON 中 documents 是 [] 而不是 null。
	documents := make(
		[]documentResponse,
		0,
		len(result.Documents),
	)

	for _, sourceDocument := range result.Documents {
		documents = append(
			documents,
			newDocumentResponse(sourceDocument),
		)
	}

	c.JSON(
		http.StatusOK,
		documentListResponse{
			Documents: documents,
			Pagination: paginationResponse{
				Page:       result.Page,
				PageSize:   result.PageSize,
				Total:      result.Total,
				TotalPages: result.TotalPages,
			},
		},
	)
}

// parsePositiveQueryInt 读取一个正整数查询参数。
//
// 如果参数不存在则返回 defaultValue；如果存在但不是正整数则返回错误。
func parsePositiveQueryInt(
	c *gin.Context,
	name string,
	defaultValue int64,
) (int64, error) {
	rawValue, provided := c.GetQuery(name)
	if !provided {
		return defaultValue, nil
	}

	value, err := strconv.ParseInt(rawValue, 10, 64)
	if err != nil || value <= 0 {
		return 0, fmt.Errorf(
			"%s must be a positive integer",
			name,
		)
	}

	return value, nil
}
