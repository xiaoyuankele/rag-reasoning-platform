package api

import (
	"context"
	"errors"
	"net/http"

	"github.com/gin-gonic/gin"

	embeddingapplication "rag-reasoning-platform/backend/internal/application/embedding"
	documentdomain "rag-reasoning-platform/backend/internal/domain/document"
	embeddingdomain "rag-reasoning-platform/backend/internal/domain/embedding"
)

const defaultSemanticSearchTopK = 5

// semanticSearchService 定义语义检索 Handler 实际需要的最小应用能力。
//
// 测试可以传入 Fake，生产环境则由 main.go 注入 *SemanticSearchService。
type semanticSearchService interface {
	Search(
		ctx context.Context,
		input embeddingapplication.SemanticSearchInput,
	) (embeddingapplication.SemanticSearchOutput, error)
}

// SemanticSearchHandler 负责 POST /semantic-search 的 HTTP 边界转换。
type SemanticSearchHandler struct {
	service semanticSearchService
}

// NewSemanticSearchHandler 创建语义检索 Handler，并注入应用服务。
func NewSemanticSearchHandler(
	service semanticSearchService,
) *SemanticSearchHandler {
	return &SemanticSearchHandler{service: service}
}

// RegisterRoutes 注册语义检索路由。
func (h *SemanticSearchHandler) RegisterRoutes(router *gin.Engine) {
	router.POST("/semantic-search", h.Search)
}

// semanticSearchRequest 是客户端提交的 JSON 契约。
//
// TopK 使用指针是为了区分“没有提供”和“明确提供 0”：前者使用默认值 5，
// 后者必须交给 Application 判定为非法参数。
type semanticSearchRequest struct {
	Query      string `json:"query"`
	DocumentID *int64 `json:"document_id"`
	TopK       *int   `json:"top_k"`
}

// semanticSearchHitResponse 是单条语义检索命中的 HTTP DTO。
type semanticSearchHitResponse struct {
	ChunkID      int64   `json:"chunk_id"`
	DocumentID   int64   `json:"document_id"`
	ChunkIndex   int     `json:"chunk_index"`
	Title        *string `json:"title"`
	OriginalName string  `json:"original_name"`
	MIMEType     string  `json:"mime_type"`
	Content      string  `json:"content"`
	PageStart    *int    `json:"page_start"`
	PageEnd      *int    `json:"page_end"`
	Similarity   float64 `json:"similarity"`
}

// semanticSearchResponse 是语义检索成功时的完整 HTTP 响应。
type semanticSearchResponse struct {
	Query string                      `json:"query"`
	Hits  []semanticSearchHitResponse `json:"hits"`
}

// Search 读取 JSON 请求，调用 Application，并把结果转换为 HTTP 响应。
func (h *SemanticSearchHandler) Search(c *gin.Context) {
	var request semanticSearchRequest

	// ShouldBindJSON 读取请求体并按照 json 标签绑定字段。必须传 &request，
	// 因为 Gin 需要修改这个变量，而不是接收它的一份副本。
	if err := c.ShouldBindJSON(&request); err != nil {
		c.JSON(http.StatusBadRequest, errorResponse{
			Error: "request body must be valid JSON",
		})
		return
	}

	topK := defaultSemanticSearchTopK
	if request.TopK != nil {
		topK = *request.TopK
	}

	result, err := h.service.Search(
		c.Request.Context(),
		embeddingapplication.SemanticSearchInput{
			Query:      request.Query,
			DocumentID: request.DocumentID,
			TopK:       topK,
		},
	)
	if errors.Is(err, documentdomain.ErrNotFound) {
		c.JSON(http.StatusNotFound, errorResponse{
			Error: "document not found",
		})
		return
	}
	if errors.Is(err, embeddingapplication.ErrDocumentEmbeddingsNotReady) {
		c.JSON(http.StatusConflict, errorResponse{
			Error: "document embeddings are not ready",
		})
		return
	}
	if errors.Is(err, embeddingapplication.ErrInvalidDocumentID) {
		c.JSON(http.StatusBadRequest, errorResponse{
			Error: "document_id must be a positive integer",
		})
		return
	}
	if errors.Is(err, embeddingapplication.ErrSemanticSearchQueryRequired) {
		c.JSON(http.StatusBadRequest, errorResponse{
			Error: "query is required",
		})
		return
	}
	if errors.Is(err, embeddingapplication.ErrSemanticSearchQueryInvalidUTF8) {
		c.JSON(http.StatusBadRequest, errorResponse{
			Error: "query must be valid UTF-8",
		})
		return
	}
	if errors.Is(err, embeddingapplication.ErrSemanticSearchQueryTooLong) {
		c.JSON(http.StatusBadRequest, errorResponse{
			Error: "query must not exceed 1000 characters",
		})
		return
	}
	if errors.Is(err, embeddingapplication.ErrInvalidSemanticSearchTopK) {
		c.JSON(http.StatusBadRequest, errorResponse{
			Error: "top_k must be between 1 and 20",
		})
		return
	}

	// 远程服务拒绝了本项目构造的请求，或返回了不符合契约的数据。
	// 对客户端隐藏提供方响应细节，并用 502 表示上游响应不可用。
	if errors.Is(err, embeddingdomain.ErrEmbeddingRequestRejected) ||
		errors.Is(err, embeddingdomain.ErrInvalidEmbeddingResponse) {
		_ = c.Error(err)
		c.JSON(http.StatusBadGateway, errorResponse{
			Error: "embedding provider returned an invalid response",
		})
		return
	}

	// 鉴权、额度、限流和服务不可用都意味着当前请求暂时无法完成。
	if errors.Is(err, embeddingdomain.ErrEmbeddingAuthentication) ||
		errors.Is(err, embeddingdomain.ErrEmbeddingRateLimited) ||
		errors.Is(err, embeddingdomain.ErrEmbeddingQuotaExceeded) ||
		errors.Is(err, embeddingdomain.ErrEmbeddingUnavailable) {
		_ = c.Error(err)
		c.JSON(http.StatusServiceUnavailable, errorResponse{
			Error: "semantic search is temporarily unavailable",
		})
		return
	}

	// 其余错误可能来自数据库或未预料到的内部故障。
	if err != nil {
		_ = c.Error(err)
		c.JSON(http.StatusInternalServerError, errorResponse{
			Error: "internal server error",
		})
		return
	}

	hits := make([]semanticSearchHitResponse, 0, len(result.Hits))
	for _, sourceHit := range result.Hits {
		hits = append(hits, newSemanticSearchHitResponse(sourceHit))
	}

	c.JSON(http.StatusOK, semanticSearchResponse{
		Query: result.Query,
		Hits:  hits,
	})
}

// newSemanticSearchHitResponse 把 Domain 命中结果转换为对外 HTTP DTO。
func newSemanticSearchHitResponse(
	source documentdomain.SemanticSearchHit,
) semanticSearchHitResponse {
	return semanticSearchHitResponse{
		ChunkID:      source.ChunkID,
		DocumentID:   source.DocumentID,
		ChunkIndex:   source.ChunkIndex,
		Title:        source.Title,
		OriginalName: source.OriginalName,
		MIMEType:     source.MIMEType,
		Content:      source.Content,
		PageStart:    source.PageStart,
		PageEnd:      source.PageEnd,
		Similarity:   source.Similarity,
	}
}
