package api

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/gin-gonic/gin"

	applicationdocument "rag-reasoning-platform/backend/internal/application/document"
	documentdomain "rag-reasoning-platform/backend/internal/domain/document"
)

// fakeDocumentListService 是列表 Handler 测试使用的应用服务替身。
// 它只实现 List，避免 HTTP 测试依赖真实 PostgreSQL。
type fakeDocumentListService struct {
	listFunc  func(context.Context, applicationdocument.ListInput) (applicationdocument.ListOutput, error)
	listCalls int
}

func (f *fakeDocumentListService) List(
	ctx context.Context,
	input applicationdocument.ListInput,
) (applicationdocument.ListOutput, error) {
	f.listCalls++
	return f.listFunc(ctx, input)
}

// newTestDocumentListRouter 创建只注册列表接口的测试路由。
func newTestDocumentListRouter(service documentListService) *gin.Engine {
	gin.SetMode(gin.TestMode)

	router := gin.New()
	handler := NewDocumentListHandler(service)
	handler.RegisterRoutes(router)

	return router
}

// TestDocumentListHandlerUsesDefaultPagination 验证没有查询参数时，
// Handler 会使用应用层公开的默认分页值，并稳定返回空 JSON 数组。
func TestDocumentListHandlerUsesDefaultPagination(t *testing.T) {
	service := &fakeDocumentListService{
		listFunc: func(
			_ context.Context,
			input applicationdocument.ListInput,
		) (applicationdocument.ListOutput, error) {
			expectedInput := applicationdocument.ListInput{
				Page:     applicationdocument.DefaultPage,
				PageSize: applicationdocument.DefaultPageSize,
			}
			if input != expectedInput {
				t.Fatalf("expected input %+v, got %+v", expectedInput, input)
			}

			return applicationdocument.ListOutput{
				Documents:  make([]documentdomain.Document, 0),
				Page:       applicationdocument.DefaultPage,
				PageSize:   applicationdocument.DefaultPageSize,
				Total:      0,
				TotalPages: 0,
			}, nil
		},
	}

	request := httptest.NewRequest(
		http.MethodGet,
		"/documents",
		nil,
	)
	response := httptest.NewRecorder()

	newTestDocumentListRouter(service).ServeHTTP(response, request)

	if response.Code != http.StatusOK {
		t.Fatalf(
			"expected status %d, got %d: %s",
			http.StatusOK,
			response.Code,
			response.Body.String(),
		)
	}

	expectedBody := `{"documents":[],"pagination":{"page":1,"page_size":20,"total":0,"total_pages":0}}`
	if response.Body.String() != expectedBody {
		t.Fatalf(
			"expected body %s, got %s",
			expectedBody,
			response.Body.String(),
		)
	}

	if service.listCalls != 1 {
		t.Fatalf("expected one List call, got %d", service.listCalls)
	}
}

// TestDocumentListHandlerUsesCustomPagination 验证查询参数会被转换成
// ListInput，并且领域文档会复用统一的 documentResponse 转换逻辑。
func TestDocumentListHandlerUsesCustomPagination(t *testing.T) {
	createdAt := time.Date(2026, 7, 21, 10, 0, 0, 0, time.UTC)
	expectedDocument := documentdomain.Document{
		ID:           7,
		OriginalName: "pagination.pdf",
		StoragePath:  "documents/internal-only.pdf",
		MIMEType:     "application/pdf",
		SizeBytes:    2048,
		SHA256:       strings.Repeat("a", 64),
		Status:       documentdomain.StatusUploaded,
		CreatedAt:    createdAt,
		UpdatedAt:    createdAt,
	}

	service := &fakeDocumentListService{
		listFunc: func(
			_ context.Context,
			input applicationdocument.ListInput,
		) (applicationdocument.ListOutput, error) {
			expectedInput := applicationdocument.ListInput{
				Page:     2,
				PageSize: 5,
			}
			if input != expectedInput {
				t.Fatalf("expected input %+v, got %+v", expectedInput, input)
			}

			return applicationdocument.ListOutput{
				Documents:  []documentdomain.Document{expectedDocument},
				Page:       2,
				PageSize:   5,
				Total:      12,
				TotalPages: 3,
			}, nil
		},
	}

	request := httptest.NewRequest(
		http.MethodGet,
		"/documents?page=2&page_size=5",
		nil,
	)
	response := httptest.NewRecorder()
	newTestDocumentListRouter(service).ServeHTTP(response, request)

	if response.Code != http.StatusOK {
		t.Fatalf(
			"expected status %d, got %d: %s",
			http.StatusOK,
			response.Code,
			response.Body.String(),
		)
	}

	var responseBody documentListResponse
	if err := json.Unmarshal(response.Body.Bytes(), &responseBody); err != nil {
		t.Fatalf("decode response JSON: %v", err)
	}

	if len(responseBody.Documents) != 1 {
		t.Fatalf("expected one document, got %d", len(responseBody.Documents))
	}

	if responseBody.Documents[0].ID != expectedDocument.ID ||
		responseBody.Documents[0].OriginalName != expectedDocument.OriginalName {
		t.Fatalf("unexpected document response: %+v", responseBody.Documents[0])
	}

	if responseBody.Pagination.Page != 2 ||
		responseBody.Pagination.PageSize != 5 ||
		responseBody.Pagination.Total != 12 ||
		responseBody.Pagination.TotalPages != 3 {
		t.Fatalf("unexpected pagination response: %+v", responseBody.Pagination)
	}

	if strings.Contains(response.Body.String(), "storage_path") {
		t.Fatal("response must not expose internal storage_path")
	}
}

// TestDocumentListHandlerRejectsInvalidQuery 验证无法转换或不是正整数的
// 查询参数会在 HTTP 层被拒绝，并且不会调用应用服务。
func TestDocumentListHandlerRejectsInvalidQuery(t *testing.T) {
	tests := []struct {
		name         string
		target       string
		expectedBody string
	}{
		{
			name:         "non-numeric page",
			target:       "/documents?page=abc",
			expectedBody: `{"error":"page must be a positive integer"}`,
		},
		{
			name:         "zero page",
			target:       "/documents?page=0",
			expectedBody: `{"error":"page must be a positive integer"}`,
		},
		{
			name:         "negative page",
			target:       "/documents?page=-1",
			expectedBody: `{"error":"page must be a positive integer"}`,
		},
		{
			name:         "non-numeric page size",
			target:       "/documents?page_size=abc",
			expectedBody: `{"error":"page_size must be a positive integer"}`,
		},
		{
			name:         "zero page size",
			target:       "/documents?page_size=0",
			expectedBody: `{"error":"page_size must be a positive integer"}`,
		},
		{
			name:         "negative page size",
			target:       "/documents?page_size=-1",
			expectedBody: `{"error":"page_size must be a positive integer"}`,
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			service := &fakeDocumentListService{
				listFunc: func(
					context.Context,
					applicationdocument.ListInput,
				) (applicationdocument.ListOutput, error) {
					t.Fatal("List must not be called for invalid query parameters")
					return applicationdocument.ListOutput{}, nil
				},
			}

			request := httptest.NewRequest(http.MethodGet, test.target, nil)
			response := httptest.NewRecorder()
			newTestDocumentListRouter(service).ServeHTTP(response, request)

			if response.Code != http.StatusBadRequest {
				t.Fatalf(
					"expected status %d, got %d",
					http.StatusBadRequest,
					response.Code,
				)
			}

			if response.Body.String() != test.expectedBody {
				t.Fatalf(
					"expected body %s, got %s",
					test.expectedBody,
					response.Body.String(),
				)
			}

			if service.listCalls != 0 {
				t.Fatalf("expected no List calls, got %d", service.listCalls)
			}
		})
	}
}

// TestDocumentListHandlerMapsApplicationErrors 验证应用层错误会被转换成
// 稳定的 HTTP 状态码，并且未知内部错误不会原样暴露。
func TestDocumentListHandlerMapsApplicationErrors(t *testing.T) {
	tests := []struct {
		name           string
		serviceError   error
		expectedStatus int
		expectedBody   string
	}{
		{
			name:           "invalid page",
			serviceError:   applicationdocument.ErrInvalidPage,
			expectedStatus: http.StatusBadRequest,
			expectedBody:   `{"error":"page must be a positive integer"}`,
		},
		{
			name:           "invalid page size",
			serviceError:   applicationdocument.ErrInvalidPageSize,
			expectedStatus: http.StatusBadRequest,
			expectedBody:   `{"error":"page_size must be between 1 and 100"}`,
		},
		{
			name:           "unknown internal error",
			serviceError:   errors.New("database unavailable"),
			expectedStatus: http.StatusInternalServerError,
			expectedBody:   `{"error":"internal server error"}`,
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			service := &fakeDocumentListService{
				listFunc: func(
					context.Context,
					applicationdocument.ListInput,
				) (applicationdocument.ListOutput, error) {
					return applicationdocument.ListOutput{}, fmt.Errorf(
						"list documents: %w",
						test.serviceError,
					)
				},
			}

			request := httptest.NewRequest(
				http.MethodGet,
				"/documents?page=1&page_size=20",
				nil,
			)
			response := httptest.NewRecorder()
			newTestDocumentListRouter(service).ServeHTTP(response, request)

			if response.Code != test.expectedStatus {
				t.Fatalf(
					"expected status %d, got %d",
					test.expectedStatus,
					response.Code,
				)
			}

			if response.Body.String() != test.expectedBody {
				t.Fatalf(
					"expected body %s, got %s",
					test.expectedBody,
					response.Body.String(),
				)
			}
		})
	}
}
