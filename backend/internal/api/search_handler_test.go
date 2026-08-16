package api

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"reflect"
	"testing"

	"github.com/gin-gonic/gin"

	applicationdocument "rag-reasoning-platform/backend/internal/application/document"
	accessdomain "rag-reasoning-platform/backend/internal/domain/access"
	documentdomain "rag-reasoning-platform/backend/internal/domain/document"
)

// fakeDocumentSearchService 是搜索 Handler 测试使用的应用服务替身。
// 测试可以通过 searchFunc 检查 Handler 传入的参数，并控制返回结果。
type fakeDocumentSearchService struct {
	searchFunc  func(context.Context, accessdomain.OwnerScope, applicationdocument.SearchInput) (applicationdocument.SearchOutput, error)
	searchCalls int
}

func (f *fakeDocumentSearchService) Search(
	ctx context.Context,
	scope accessdomain.OwnerScope,
	input applicationdocument.SearchInput,
) (applicationdocument.SearchOutput, error) {
	f.searchCalls++
	return f.searchFunc(ctx, scope, input)
}

// newTestDocumentSearchRouter 创建只注册搜索接口的 Gin 测试路由。
// 它不会启动真实端口，也不会连接 PostgreSQL。
func newTestDocumentSearchRouter(service documentSearchService) *gin.Engine {
	gin.SetMode(gin.TestMode)

	router := gin.New()
	useTestAuthenticatedIdentity(router)
	handler := NewDocumentSearchHandler(service)
	handler.RegisterRoutes(router)

	return router
}

// TestDocumentSearchHandlerRequiresAuthenticatedIdentity 验证搜索入口没有
// 可信身份时直接返回 401，且不会把请求继续交给 Application。
func TestDocumentSearchHandlerRequiresAuthenticatedIdentity(t *testing.T) {
	service := &fakeDocumentSearchService{
		searchFunc: func(
			context.Context,
			accessdomain.OwnerScope,
			applicationdocument.SearchInput,
		) (applicationdocument.SearchOutput, error) {
			t.Fatal("Search must not be called without authenticated identity")
			return applicationdocument.SearchOutput{}, nil
		},
	}

	gin.SetMode(gin.TestMode)
	router := gin.New()
	NewDocumentSearchHandler(service).RegisterRoutes(router)
	request := httptest.NewRequest(http.MethodGet, "/search?q=bridge", nil)
	response := httptest.NewRecorder()
	router.ServeHTTP(response, request)

	if response.Code != http.StatusUnauthorized {
		t.Fatalf(
			"status=%d want=%d body=%s",
			response.Code,
			http.StatusUnauthorized,
			response.Body.String(),
		)
	}
	if service.searchCalls != 0 {
		t.Fatalf("Search calls=%d want=0", service.searchCalls)
	}
}

// TestDocumentSearchHandlerReturnsResults 验证完整的成功路径：
// HTTP 查询参数 -> SearchInput -> SearchOutput -> JSON 响应。
func TestDocumentSearchHandlerReturnsResults(t *testing.T) {
	title := "Bridge vibration study"
	pageStart := 3
	pageEnd := 4
	expectedHit := documentdomain.SearchHit{
		ChunkID:      11,
		DocumentID:   7,
		ChunkIndex:   2,
		Title:        &title,
		OriginalName: "bridge-study.pdf",
		MIMEType:     "application/pdf",
		Content:      "bridge vibration analysis",
		PageStart:    &pageStart,
		PageEnd:      &pageEnd,
	}

	service := &fakeDocumentSearchService{
		searchFunc: func(
			_ context.Context,
			scope accessdomain.OwnerScope,
			input applicationdocument.SearchInput,
		) (applicationdocument.SearchOutput, error) {
			if scope.OwnerUserID() != testAPIOwnerUserID {
				t.Fatalf(
					"Search() scope owner = %d, want %d",
					scope.OwnerUserID(),
					testAPIOwnerUserID,
				)
			}
			expectedInput := applicationdocument.SearchInput{
				Query:    "bridge",
				Page:     2,
				PageSize: 5,
			}
			if input != expectedInput {
				t.Fatalf("expected input %+v, got %+v", expectedInput, input)
			}

			return applicationdocument.SearchOutput{
				Query:      "bridge",
				Hits:       []documentdomain.SearchHit{expectedHit},
				Page:       2,
				PageSize:   5,
				Total:      6,
				TotalPages: 2,
			}, nil
		},
	}

	request := httptest.NewRequest(
		http.MethodGet,
		"/search?q=bridge&page=2&page_size=5",
		nil,
	)
	response := httptest.NewRecorder()

	newTestDocumentSearchRouter(service).ServeHTTP(response, request)

	if response.Code != http.StatusOK {
		t.Fatalf(
			"expected status %d, got %d: %s",
			http.StatusOK,
			response.Code,
			response.Body.String(),
		)
	}

	var actual documentSearchResponse
	if err := json.Unmarshal(response.Body.Bytes(), &actual); err != nil {
		t.Fatalf("decode response JSON: %v", err)
	}

	if actual.Query != "bridge" {
		t.Fatalf("expected query bridge, got %q", actual.Query)
	}
	if len(actual.Results) != 1 {
		t.Fatalf("expected one result, got %d", len(actual.Results))
	}

	expectedResponseHit := newSearchHitResponse(expectedHit)
	// PageStart/PageEnd 是指针。JSON 解码后指针地址会变化，
	// reflect.DeepEqual 会继续比较指针指向的页码值。
	if !reflect.DeepEqual(actual.Results[0], expectedResponseHit) {
		t.Fatalf(
			"expected hit %+v, got %+v",
			expectedResponseHit,
			actual.Results[0],
		)
	}

	expectedPagination := paginationResponse{
		Page:       2,
		PageSize:   5,
		Total:      6,
		TotalPages: 2,
	}
	if actual.Pagination != expectedPagination {
		t.Fatalf(
			"expected pagination %+v, got %+v",
			expectedPagination,
			actual.Pagination,
		)
	}

	if service.searchCalls != 1 {
		t.Fatalf("expected one Search call, got %d", service.searchCalls)
	}
}

// TestDocumentSearchHandlerPassesDocumentIDFilter 验证可选 document_id
// 会被 Handler 解析成正整数指针并传给 Application。
func TestDocumentSearchHandlerPassesDocumentIDFilter(t *testing.T) {
	service := &fakeDocumentSearchService{
		searchFunc: func(
			_ context.Context,
			_ accessdomain.OwnerScope,
			input applicationdocument.SearchInput,
		) (applicationdocument.SearchOutput, error) {
			if input.DocumentID == nil || *input.DocumentID != 20 {
				t.Fatalf("document ID filter = %v, want 20", input.DocumentID)
			}

			return applicationdocument.SearchOutput{
				Query:      "control",
				Hits:       make([]documentdomain.SearchHit, 0),
				Page:       input.Page,
				PageSize:   input.PageSize,
				Total:      0,
				TotalPages: 0,
			}, nil
		},
	}

	request := httptest.NewRequest(
		http.MethodGet,
		"/search?q=control&document_id=20",
		nil,
	)
	response := httptest.NewRecorder()

	newTestDocumentSearchRouter(service).ServeHTTP(response, request)

	if response.Code != http.StatusOK {
		t.Fatalf(
			"expected status %d, got %d: %s",
			http.StatusOK,
			response.Code,
			response.Body.String(),
		)
	}
}

// TestDocumentSearchHandlerRejectsInvalidDocumentIDFilter 验证前端只要显式
// 提供 document_id，就必须提供一个可以解析的正整数。
func TestDocumentSearchHandlerRejectsInvalidDocumentIDFilter(t *testing.T) {
	tests := []struct {
		name   string
		target string
	}{
		{name: "empty document ID", target: "/search?q=bridge&document_id="},
		{name: "non-numeric document ID", target: "/search?q=bridge&document_id=abc"},
		{name: "zero document ID", target: "/search?q=bridge&document_id=0"},
		{name: "negative document ID", target: "/search?q=bridge&document_id=-1"},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			service := &fakeDocumentSearchService{
				searchFunc: func(
					context.Context,
					accessdomain.OwnerScope,
					applicationdocument.SearchInput,
				) (applicationdocument.SearchOutput, error) {
					t.Fatal("Search must not be called for invalid document ID")
					return applicationdocument.SearchOutput{}, nil
				},
			}

			request := httptest.NewRequest(http.MethodGet, test.target, nil)
			response := httptest.NewRecorder()

			newTestDocumentSearchRouter(service).ServeHTTP(response, request)

			if response.Code != http.StatusBadRequest {
				t.Fatalf(
					"expected status %d, got %d: %s",
					http.StatusBadRequest,
					response.Code,
					response.Body.String(),
				)
			}
			if response.Body.String() != `{"error":"document_id must be a positive integer"}` {
				t.Fatalf("unexpected response body: %s", response.Body.String())
			}
			if service.searchCalls != 0 {
				t.Fatalf("expected no Search calls, got %d", service.searchCalls)
			}
		})
	}
}

// TestDocumentSearchHandlerUsesDefaultPagination 验证请求只提供 q 时，
// Handler 会为 Application 补上默认 page 和 page_size。
func TestDocumentSearchHandlerUsesDefaultPagination(t *testing.T) {
	service := &fakeDocumentSearchService{
		searchFunc: func(
			_ context.Context,
			_ accessdomain.OwnerScope,
			input applicationdocument.SearchInput,
		) (applicationdocument.SearchOutput, error) {
			expectedInput := applicationdocument.SearchInput{
				Query:    "bridge",
				Page:     applicationdocument.DefaultPage,
				PageSize: applicationdocument.DefaultPageSize,
			}
			if input != expectedInput {
				t.Fatalf("expected input %+v, got %+v", expectedInput, input)
			}

			return applicationdocument.SearchOutput{
				Query:      "bridge",
				Hits:       make([]documentdomain.SearchHit, 0),
				Page:       applicationdocument.DefaultPage,
				PageSize:   applicationdocument.DefaultPageSize,
				Total:      0,
				TotalPages: 0,
			}, nil
		},
	}

	// URL 中只传 q，没有传 page 和 page_size。
	request := httptest.NewRequest(
		http.MethodGet,
		"/search?q=bridge",
		nil,
	)
	response := httptest.NewRecorder()

	newTestDocumentSearchRouter(service).ServeHTTP(response, request)

	if response.Code != http.StatusOK {
		t.Fatalf(
			"expected status %d, got %d: %s",
			http.StatusOK,
			response.Code,
			response.Body.String(),
		)
	}

	var actual documentSearchResponse
	if err := json.Unmarshal(response.Body.Bytes(), &actual); err != nil {
		t.Fatalf("decode response JSON: %v", err)
	}

	if actual.Query != "bridge" {
		t.Fatalf("expected query bridge, got %q", actual.Query)
	}
	if len(actual.Results) != 0 {
		t.Fatalf("expected no results, got %d", len(actual.Results))
	}

	expectedPagination := paginationResponse{
		Page:       applicationdocument.DefaultPage,
		PageSize:   applicationdocument.DefaultPageSize,
		Total:      0,
		TotalPages: 0,
	}
	if actual.Pagination != expectedPagination {
		t.Fatalf(
			"expected pagination %+v, got %+v",
			expectedPagination,
			actual.Pagination,
		)
	}

	if service.searchCalls != 1 {
		t.Fatalf("expected one Search call, got %d", service.searchCalls)
	}
}

// TestDocumentSearchHandlerRejectsInvalidPagination 验证 HTTP 分页参数
// 无法转换成正整数时，Handler 直接返回 400，并且不会调用 Application。
func TestDocumentSearchHandlerRejectsInvalidPagination(t *testing.T) {
	tests := []struct {
		name         string
		target       string
		expectedBody string
	}{
		{
			name:         "non-numeric page",
			target:       "/search?q=bridge&page=abc",
			expectedBody: `{"error":"page must be a positive integer"}`,
		},
		{
			name:         "zero page",
			target:       "/search?q=bridge&page=0",
			expectedBody: `{"error":"page must be a positive integer"}`,
		},
		{
			name:         "negative page",
			target:       "/search?q=bridge&page=-1",
			expectedBody: `{"error":"page must be a positive integer"}`,
		},
		{
			name:         "non-numeric page size",
			target:       "/search?q=bridge&page_size=abc",
			expectedBody: `{"error":"page_size must be a positive integer"}`,
		},
		{
			name:         "zero page size",
			target:       "/search?q=bridge&page_size=0",
			expectedBody: `{"error":"page_size must be a positive integer"}`,
		},
		{
			name:         "negative page size",
			target:       "/search?q=bridge&page_size=-1",
			expectedBody: `{"error":"page_size must be a positive integer"}`,
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			service := &fakeDocumentSearchService{
				searchFunc: func(
					context.Context,
					accessdomain.OwnerScope,
					applicationdocument.SearchInput,
				) (applicationdocument.SearchOutput, error) {
					t.Fatal("Search must not be called for invalid HTTP pagination")
					return applicationdocument.SearchOutput{}, nil
				},
			}

			request := httptest.NewRequest(http.MethodGet, test.target, nil)
			response := httptest.NewRecorder()

			newTestDocumentSearchRouter(service).ServeHTTP(response, request)

			if response.Code != http.StatusBadRequest {
				t.Fatalf(
					"expected status %d, got %d: %s",
					http.StatusBadRequest,
					response.Code,
					response.Body.String(),
				)
			}

			if response.Body.String() != test.expectedBody {
				t.Fatalf(
					"expected body %s, got %s",
					test.expectedBody,
					response.Body.String(),
				)
			}

			if service.searchCalls != 0 {
				t.Fatalf(
					"expected no Search calls, got %d",
					service.searchCalls,
				)
			}
		})
	}
}

// TestDocumentSearchHandlerMapsApplicationErrors 验证 Handler 会把
// Application 返回的错误翻译成安全、稳定的 HTTP 状态码和 JSON。
func TestDocumentSearchHandlerMapsApplicationErrors(t *testing.T) {
	// 第一部分只描述“输入错误”和“预期 HTTP 响应”的对应关系。
	tests := []struct {
		name           string
		serviceError   error
		expectedStatus int
		expectedBody   string
	}{
		{
			name:           "query is required",
			serviceError:   applicationdocument.ErrSearchQueryRequired,
			expectedStatus: http.StatusBadRequest,
			expectedBody:   `{"error":"query is required"}`,
		},
		{
			name:           "query is invalid UTF-8",
			serviceError:   applicationdocument.ErrSearchQueryInvalidUTF8,
			expectedStatus: http.StatusBadRequest,
			expectedBody:   `{"error":"query must be valid UTF-8"}`,
		},
		{
			name:           "query is too long",
			serviceError:   applicationdocument.ErrSearchQueryTooLong,
			expectedStatus: http.StatusBadRequest,
			expectedBody:   `{"error":"query must not exceed 200 characters"}`,
		},
		{
			name:           "page is invalid",
			serviceError:   applicationdocument.ErrInvalidPage,
			expectedStatus: http.StatusBadRequest,
			expectedBody:   `{"error":"page must be a positive integer"}`,
		},
		{
			name:           "page size is invalid",
			serviceError:   applicationdocument.ErrInvalidPageSize,
			expectedStatus: http.StatusBadRequest,
			expectedBody:   `{"error":"page_size must be between 1 and 100"}`,
		},
		{
			name:           "document ID is invalid",
			serviceError:   applicationdocument.ErrInvalidID,
			expectedStatus: http.StatusBadRequest,
			expectedBody:   `{"error":"document_id must be a positive integer"}`,
		},
		{
			name:           "unknown internal error",
			serviceError:   errors.New("database unavailable"),
			expectedStatus: http.StatusInternalServerError,
			expectedBody:   `{"error":"internal server error"}`,
		},
	}

	// 第二部分用同一套 HTTP 流程执行表格中的每一种错误。
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			service := &fakeDocumentSearchService{
				searchFunc: func(
					context.Context,
					accessdomain.OwnerScope,
					applicationdocument.SearchInput,
				) (applicationdocument.SearchOutput, error) {
					// %w 模拟错误从 Application 向外包装后再到达 Handler。
					return applicationdocument.SearchOutput{}, fmt.Errorf(
						"search failed: %w",
						test.serviceError,
					)
				},
			}

			request := httptest.NewRequest(
				http.MethodGet,
				"/search?q=bridge",
				nil,
			)
			response := httptest.NewRecorder()

			newTestDocumentSearchRouter(service).ServeHTTP(response, request)

			if response.Code != test.expectedStatus {
				t.Fatalf(
					"expected status %d, got %d: %s",
					test.expectedStatus,
					response.Code,
					response.Body.String(),
				)
			}

			if response.Body.String() != test.expectedBody {
				t.Fatalf(
					"expected body %s, got %s",
					test.expectedBody,
					response.Body.String(),
				)
			}

			if service.searchCalls != 1 {
				t.Fatalf(
					"expected one Search call, got %d",
					service.searchCalls,
				)
			}
		})
	}
}
