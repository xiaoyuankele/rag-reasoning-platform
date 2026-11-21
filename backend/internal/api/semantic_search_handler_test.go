package api

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/gin-gonic/gin"

	embeddingapplication "rag-reasoning-platform/backend/internal/application/embedding"
	accessdomain "rag-reasoning-platform/backend/internal/domain/access"
	documentdomain "rag-reasoning-platform/backend/internal/domain/document"
	embeddingdomain "rag-reasoning-platform/backend/internal/domain/embedding"
)

// fakeSemanticSearchService 只记录 Handler 交给 Application 的数据，
// 并返回测试预先安排的结果，不调用远程 API 或真实数据库。
type fakeSemanticSearchService struct {
	output        embeddingapplication.SemanticSearchOutput
	err           error
	receivedInput embeddingapplication.SemanticSearchInput
	receivedScope accessdomain.OwnerScope
	callCount     int
}

func (f *fakeSemanticSearchService) Search(
	_ context.Context,
	scope accessdomain.OwnerScope,
	input embeddingapplication.SemanticSearchInput,
) (embeddingapplication.SemanticSearchOutput, error) {
	f.callCount++
	f.receivedScope = scope
	f.receivedInput = input
	return f.output, f.err
}

func newSemanticSearchTestRouter(
	service semanticSearchService,
) *gin.Engine {
	gin.SetMode(gin.TestMode)
	router := gin.New()
	useTestAuthenticatedIdentity(router)
	handler := NewSemanticSearchHandler(service)
	handler.RegisterRoutes(router)
	return router
}

// TestSemanticSearchHandlerReturnsResults 验证完整成功边界：
// JSON 请求被转换为 Application 输入，Domain 结果被转换为 HTTP DTO。
func TestSemanticSearchHandlerReturnsResults(t *testing.T) {
	title := "磁悬浮系统稳定性研究"
	pageStart := 3
	pageEnd := 4
	documentID := int64(20)
	service := &fakeSemanticSearchService{
		output: embeddingapplication.SemanticSearchOutput{
			Query: "磁悬浮振动",
			Hits: []documentdomain.SemanticSearchHit{
				{
					ChunkID:      101,
					DocumentID:   documentID,
					ChunkIndex:   2,
					Title:        &title,
					OriginalName: "maglev.pdf",
					MIMEType:     "application/pdf",
					Content:      "车辆与轨道梁之间存在动力耦合。",
					PageStart:    &pageStart,
					PageEnd:      &pageEnd,
					Similarity:   0.93,
				},
			},
		},
	}

	recorder := httptest.NewRecorder()
	request := httptest.NewRequest(
		http.MethodPost,
		"/semantic-search",
		strings.NewReader(`{
			"query":"  磁悬浮振动  ",
			"document_id":20,
			"top_k":3
		}`),
	)
	request.Header.Set("Content-Type", "application/json")

	newSemanticSearchTestRouter(service).ServeHTTP(recorder, request)

	if recorder.Code != http.StatusOK {
		t.Fatalf("status = %d, want %d; body = %s", recorder.Code, http.StatusOK, recorder.Body.String())
	}
	if service.callCount != 1 {
		t.Fatalf("service call count = %d, want 1", service.callCount)
	}
	if service.receivedScope.OwnerUserID() != testAPIOwnerUserID {
		t.Fatalf(
			"scope owner = %d, want %d",
			service.receivedScope.OwnerUserID(),
			testAPIOwnerUserID,
		)
	}
	if service.receivedInput.Query != "  磁悬浮振动  " {
		t.Fatalf("query = %q, want raw request query", service.receivedInput.Query)
	}
	if service.receivedInput.DocumentID == nil ||
		*service.receivedInput.DocumentID != documentID {
		t.Fatalf("document ID = %v, want %d", service.receivedInput.DocumentID, documentID)
	}
	if service.receivedInput.TopK != 3 {
		t.Fatalf("top_k = %d, want 3", service.receivedInput.TopK)
	}

	var response semanticSearchResponse
	if err := json.Unmarshal(recorder.Body.Bytes(), &response); err != nil {
		t.Fatalf("decode response: %v", err)
	}
	if response.Query != "磁悬浮振动" {
		t.Fatalf("response query = %q, want normalized query", response.Query)
	}
	if len(response.Hits) != 1 {
		t.Fatalf("hit count = %d, want 1", len(response.Hits))
	}
	if response.Hits[0].ChunkID != 101 ||
		response.Hits[0].Similarity != 0.93 ||
		response.Hits[0].Title == nil ||
		*response.Hits[0].Title != title {
		t.Fatalf("unexpected response hit: %+v", response.Hits[0])
	}
}

// TestSemanticSearchHandlerRequiresAuthenticatedIdentity 验证无 Session 身份时
// 不解析业务请求，也不会调用可能产生远程费用的 Application 服务。
func TestSemanticSearchHandlerRequiresAuthenticatedIdentity(t *testing.T) {
	service := &fakeSemanticSearchService{}
	gin.SetMode(gin.TestMode)
	router := gin.New()
	NewSemanticSearchHandler(service).RegisterRoutes(router)
	request := httptest.NewRequest(
		http.MethodPost,
		"/semantic-search",
		strings.NewReader(`{"query":"control"}`),
	)
	request.Header.Set("Content-Type", "application/json")
	response := httptest.NewRecorder()
	router.ServeHTTP(response, request)

	if response.Code != http.StatusUnauthorized || service.callCount != 0 {
		t.Fatalf(
			"status=%d calls=%d want 401/0 body=%s",
			response.Code,
			service.callCount,
			response.Body.String(),
		)
	}
}

// TestSemanticSearchHandlerUsesDefaultTopK 验证 omitted 和 zero 的区别：
// 没有 top_k 时 Handler 填 5；显式传 0 时不能偷偷改成默认值。
func TestSemanticSearchHandlerUsesDefaultTopK(t *testing.T) {
	tests := []struct {
		name     string
		body     string
		wantTopK int
	}{
		{name: "omitted uses default", body: `{"query":"control"}`, wantTopK: 5},
		{name: "explicit zero stays zero", body: `{"query":"control","top_k":0}`, wantTopK: 0},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			service := &fakeSemanticSearchService{
				output: embeddingapplication.SemanticSearchOutput{
					Query: "control",
					Hits:  nil,
				},
			}
			recorder := performSemanticSearchRequest(t, service, test.body)

			if recorder.Code != http.StatusOK {
				t.Fatalf("status = %d, want %d; body = %s", recorder.Code, http.StatusOK, recorder.Body.String())
			}
			if service.receivedInput.TopK != test.wantTopK {
				t.Fatalf("top_k = %d, want %d", service.receivedInput.TopK, test.wantTopK)
			}
			// 成功空结果必须编码为 []，而不是 JSON null。
			if !strings.Contains(recorder.Body.String(), `"hits":[]`) {
				t.Fatalf("body = %s, want non-nil empty hits array", recorder.Body.String())
			}
		})
	}
}

func TestSemanticSearchHandlerRejectsInvalidJSON(t *testing.T) {
	service := &fakeSemanticSearchService{}
	recorder := performSemanticSearchRequest(
		t,
		service,
		`{"query":"control","top_k":"five"}`,
	)

	assertSemanticSearchErrorResponse(
		t,
		recorder,
		http.StatusBadRequest,
		"request body must be valid JSON",
	)
	if service.callCount != 0 {
		t.Fatalf("service call count = %d, want 0", service.callCount)
	}
}

// TestSemanticSearchHandlerMapsServiceErrors 用表驱动测试验证稳定错误分类。
func TestSemanticSearchHandlerMapsServiceErrors(t *testing.T) {
	tests := []struct {
		name       string
		err        error
		wantStatus int
		wantError  string
	}{
		{name: "document ID", err: embeddingapplication.ErrInvalidDocumentID, wantStatus: http.StatusBadRequest, wantError: "document_id must be a positive integer"},
		{name: "query required", err: embeddingapplication.ErrSemanticSearchQueryRequired, wantStatus: http.StatusBadRequest, wantError: "query is required"},
		{name: "query UTF-8", err: embeddingapplication.ErrSemanticSearchQueryInvalidUTF8, wantStatus: http.StatusBadRequest, wantError: "query must be valid UTF-8"},
		{name: "query too long", err: embeddingapplication.ErrSemanticSearchQueryTooLong, wantStatus: http.StatusBadRequest, wantError: "query must not exceed 1000 characters"},
		{name: "top k", err: embeddingapplication.ErrInvalidSemanticSearchTopK, wantStatus: http.StatusBadRequest, wantError: "top_k must be between 1 and 20"},
		{name: "provider rejected", err: embeddingdomain.ErrEmbeddingRequestRejected, wantStatus: http.StatusBadGateway, wantError: "embedding provider returned an invalid response"},
		{name: "invalid provider response", err: embeddingdomain.ErrInvalidEmbeddingResponse, wantStatus: http.StatusBadGateway, wantError: "embedding provider returned an invalid response"},
		{name: "authentication", err: embeddingdomain.ErrEmbeddingAuthentication, wantStatus: http.StatusServiceUnavailable, wantError: "semantic search is temporarily unavailable"},
		{name: "rate limit", err: embeddingdomain.ErrEmbeddingRateLimited, wantStatus: http.StatusServiceUnavailable, wantError: "semantic search is temporarily unavailable"},
		{name: "quota", err: embeddingdomain.ErrEmbeddingQuotaExceeded, wantStatus: http.StatusServiceUnavailable, wantError: "semantic search is temporarily unavailable"},
		{name: "provider unavailable", err: embeddingdomain.ErrEmbeddingUnavailable, wantStatus: http.StatusServiceUnavailable, wantError: "semantic search is temporarily unavailable"},
		{name: "database", err: errors.New("database unavailable"), wantStatus: http.StatusInternalServerError, wantError: "internal server error"},
		{name: "document not found", err: documentdomain.ErrNotFound, wantStatus: http.StatusNotFound, wantError: "document not found"},
		{name: "document embeddings not ready", err: embeddingapplication.ErrDocumentEmbeddingsNotReady, wantStatus: http.StatusConflict, wantError: "document embeddings are not ready"},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			// 包装一层可以验证 errors.Is，而不只是直接相等比较。
			service := &fakeSemanticSearchService{
				err: errors.Join(errors.New("search failed"), test.err),
			}
			recorder := performSemanticSearchRequest(
				t,
				service,
				`{"query":"control","top_k":5}`,
			)

			assertSemanticSearchErrorResponse(
				t,
				recorder,
				test.wantStatus,
				test.wantError,
			)
		})
	}
}

func TestSemanticSearchHandlerReturnsStableProviderCapacityError(t *testing.T) {
	service := &fakeSemanticSearchService{
		err: errors.Join(
			errors.New("semantic search admission failed"),
			embeddingapplication.ErrEmbeddingProviderCapacityExhausted,
		),
	}
	recorder := performSemanticSearchRequest(
		t,
		service,
		`{"query":"control","top_k":5}`,
	)

	if recorder.Code != http.StatusServiceUnavailable {
		t.Fatalf(
			"status = %d, want %d; body = %s",
			recorder.Code,
			http.StatusServiceUnavailable,
			recorder.Body.String(),
		)
	}
	if retryAfter := recorder.Header().Get("Retry-After"); retryAfter != "2" {
		t.Fatalf("Retry-After = %q, want 2", retryAfter)
	}

	var response errorResponse
	if err := json.Unmarshal(recorder.Body.Bytes(), &response); err != nil {
		t.Fatalf("decode capacity response: %v", err)
	}
	if response.Code != errorCodeEmbeddingProviderCapacity ||
		response.Error != "embedding service is busy; try again later" {
		t.Fatalf("capacity response = %+v", response)
	}
}

func performSemanticSearchRequest(
	t *testing.T,
	service semanticSearchService,
	body string,
) *httptest.ResponseRecorder {
	t.Helper()
	recorder := httptest.NewRecorder()
	request := httptest.NewRequest(
		http.MethodPost,
		"/semantic-search",
		strings.NewReader(body),
	)
	request.Header.Set("Content-Type", "application/json")
	newSemanticSearchTestRouter(service).ServeHTTP(recorder, request)
	return recorder
}

func assertSemanticSearchErrorResponse(
	t *testing.T,
	recorder *httptest.ResponseRecorder,
	wantStatus int,
	wantError string,
) {
	t.Helper()
	if recorder.Code != wantStatus {
		t.Fatalf("status = %d, want %d; body = %s", recorder.Code, wantStatus, recorder.Body.String())
	}

	var response errorResponse
	if err := json.Unmarshal(recorder.Body.Bytes(), &response); err != nil {
		t.Fatalf("decode error response: %v", err)
	}
	if response.Error != wantError {
		t.Fatalf("error = %q, want %q", response.Error, wantError)
	}
}
