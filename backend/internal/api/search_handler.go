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

// documentSearchService 定义搜索 Handler 所需的最小应用能力。
type documentSearchService interface {
	Search(
		ctx context.Context,
		scope accessdomain.OwnerScope,
		input applicationdocument.SearchInput,
	) (applicationdocument.SearchOutput, error)
}

// DocumentSearchHandler 负责接收文档关键词搜索 HTTP 请求。
type DocumentSearchHandler struct {
	service documentSearchService
}

// NewDocumentSearchHandler 创建文档搜索 Handler，并注入应用服务。
func NewDocumentSearchHandler(
	service documentSearchService,
) *DocumentSearchHandler {
	return &DocumentSearchHandler{
		service: service,
	}
}

// RegisterRoutes 注册文档搜索路由。
func (h *DocumentSearchHandler) RegisterRoutes(router gin.IRoutes) {
	router.GET("/search", h.Search)
}

// searchHitResponse 是单个文本块搜索命中的 HTTP 响应结构。
type searchHitResponse struct {
	ChunkID      int64   `json:"chunk_id"`
	DocumentID   int64   `json:"document_id"`
	ChunkIndex   int     `json:"chunk_index"`
	Title        *string `json:"title"`
	OriginalName string  `json:"original_name"`
	MIMEType     string  `json:"mime_type"`
	Content      string  `json:"content"`
	PageStart    *int    `json:"page_start"`
	PageEnd      *int    `json:"page_end"`
}

// documentSearchResponse 是文档关键词搜索成功时的 HTTP 响应结构。
type documentSearchResponse struct {
	Query      string                        `json:"query"`
	Terms      []string                      `json:"terms,omitempty"`
	Operator   documentdomain.SearchOperator `json:"operator,omitempty"`
	Within     documentdomain.SearchWithin   `json:"within,omitempty"`
	Results    []searchHitResponse           `json:"results"`
	Pagination paginationResponse            `json:"pagination"`
}

// Search 读取 HTTP 查询参数，调用应用服务，并把结果转换成 HTTP 响应。
func (h *DocumentSearchHandler) Search(c *gin.Context) {
	scope, authenticated := ownerScopeFromContext(c)
	if !authenticated {
		writeAuthenticationRequired(c)
		return
	}

	// Handler 只负责解析 HTTP 形状；词项数量、长度和匹配模式由 Application 校验。
	rawQuery, queryProvided := c.GetQuery("q")
	rawTerms, termsProvided := c.GetQueryArray("term")
	if queryProvided && termsProvided {
		c.JSON(http.StatusBadRequest, errorResponse{Error: "q and term cannot be used together"})
		return
	}

	// GetQuery 能区分“没有提供过滤条件”和“显式提供了空值”。
	var documentID *int64
	if rawDocumentID, provided := c.GetQuery("document_id"); provided {
		parsedDocumentID, err := strconv.ParseInt(rawDocumentID, 10, 64)
		if err != nil || parsedDocumentID <= 0 {
			c.JSON(http.StatusBadRequest, errorResponse{
				Error: "document_id must be a positive integer",
			})
			return
		}
		documentID = &parsedDocumentID
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

	result, err := h.service.Search(
		c.Request.Context(),
		scope,
		applicationdocument.SearchInput{
			Query:      rawQuery,
			Terms:      rawTerms,
			Operator:   documentdomain.SearchOperator(c.Query("operator")),
			Within:     documentdomain.SearchWithin(c.Query("within")),
			DocumentID: documentID,
			Page:       page,
			PageSize:   pageSize,
		},
	)
	if errors.Is(err, applicationdocument.ErrInvalidID) {
		c.JSON(http.StatusBadRequest, errorResponse{
			Error: "document_id must be a positive integer",
		})
		return
	}
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
	if errors.Is(err, applicationdocument.ErrSearchQueryInvalidUTF8) {
		c.JSON(http.StatusBadRequest, errorResponse{
			Error: "query must be valid UTF-8",
		})
		return
	}
	if errors.Is(err, applicationdocument.ErrSearchQueryRequired) {
		c.JSON(http.StatusBadRequest, errorResponse{
			Error: "query is required",
		})
		return
	}
	if errors.Is(err, applicationdocument.ErrSearchQueryTooLong) {
		c.JSON(http.StatusBadRequest, errorResponse{
			Error: "query must not exceed 200 characters",
		})
		return
	}
	if errors.Is(err, applicationdocument.ErrSearchInputConflict) {
		c.JSON(http.StatusBadRequest, errorResponse{Error: "q and term cannot be used together"})
		return
	}
	if errors.Is(err, applicationdocument.ErrInvalidSearchTermCount) {
		c.JSON(http.StatusBadRequest, errorResponse{Error: "term must contain between 2 and 8 unique keywords"})
		return
	}
	if errors.Is(err, applicationdocument.ErrSearchTermInvalidUTF8) {
		c.JSON(http.StatusBadRequest, errorResponse{Error: "every term must be valid UTF-8"})
		return
	}
	if errors.Is(err, applicationdocument.ErrSearchTermTooLong) {
		c.JSON(http.StatusBadRequest, errorResponse{Error: "each term must not exceed 100 characters"})
		return
	}
	if errors.Is(err, applicationdocument.ErrSearchTermsTooLong) {
		c.JSON(http.StatusBadRequest, errorResponse{Error: "all terms together must not exceed 200 characters"})
		return
	}
	if errors.Is(err, applicationdocument.ErrInvalidSearchOperator) {
		c.JSON(http.StatusBadRequest, errorResponse{Error: "operator must be all or any"})
		return
	}
	if errors.Is(err, applicationdocument.ErrInvalidSearchWithin) {
		c.JSON(http.StatusBadRequest, errorResponse{Error: "within must be chunk"})
		return
	}
	if err != nil {
		c.JSON(http.StatusInternalServerError, errorResponse{
			Error: "internal server error",
		})
		return
	}

	hits := make([]searchHitResponse, 0, len(result.Hits))
	for _, sourceHit := range result.Hits {
		hits = append(hits, newSearchHitResponse(sourceHit))
	}

	c.JSON(http.StatusOK, documentSearchResponse{
		Query:    result.Query,
		Terms:    result.Terms,
		Operator: result.Operator,
		Within:   result.Within,
		Results:  hits,
		Pagination: paginationResponse{
			Page:       result.Page,
			PageSize:   result.PageSize,
			Total:      result.Total,
			TotalPages: result.TotalPages,
		},
	})
}

// newSearchHitResponse 把领域搜索结果转换成对外 HTTP DTO。
func newSearchHitResponse(source documentdomain.SearchHit) searchHitResponse {
	return searchHitResponse{
		ChunkID:      source.ChunkID,
		DocumentID:   source.DocumentID,
		ChunkIndex:   source.ChunkIndex,
		Title:        source.Title,
		OriginalName: source.OriginalName,
		MIMEType:     source.MIMEType,
		Content:      source.Content,
		PageStart:    source.PageStart,
		PageEnd:      source.PageEnd,
	}
}
