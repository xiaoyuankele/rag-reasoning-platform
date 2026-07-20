package api

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/gin-gonic/gin"

	documentdomain "rag-reasoning-platform/backend/internal/domain/document"
)

// fakeDocumentQueryService 是测试专用的假服务。
// 它让测试可以控制 GetByID 返回什么，不需要连接数据库。
type fakeDocumentQueryService struct {
	getByIDFunc  func(context.Context, int64) (documentdomain.Document, error)
	getByIDCalls int
}

// GetByID 记录调用次数，并执行测试提供的函数。
func (f *fakeDocumentQueryService) GetByID(
	ctx context.Context,
	id int64,
) (documentdomain.Document, error) {
	f.getByIDCalls++

	return f.getByIDFunc(ctx, id)
}

// newTestDocumentRouter 创建只用于 Handler 测试的 Gin 路由。
func newTestDocumentRouter(
	service documentQueryService,
) *gin.Engine {
	gin.SetMode(gin.TestMode)

	router := gin.New()
	handler := NewDocumentHandler(service)

	// :id 是动态路径参数，例如 /documents/42 中的 42。
	handler.RegisterRoutes(router)

	return router
}

// TestDocumentHandlerGetByIDRejectsInvalidID 验证非法 ID 返回 400，
// 并且不会继续调用应用服务。
func TestDocumentHandlerGetByIDRejectsInvalidID(t *testing.T) {
	testCases := []struct {
		name string
		path string
	}{
		{
			name: "non-numeric ID",
			path: "/documents/abc",
		},
		{
			name: "zero ID",
			path: "/documents/0",
		},
		{
			name: "negative ID",
			path: "/documents/-1",
		},
	}

	for _, testCase := range testCases {
		t.Run(testCase.name, func(t *testing.T) {
			service := &fakeDocumentQueryService{
				getByIDFunc: func(
					context.Context,
					int64,
				) (documentdomain.Document, error) {
					return documentdomain.Document{}, nil
				},
			}

			router := newTestDocumentRouter(service)
			request := httptest.NewRequest(
				http.MethodGet,
				testCase.path,
				nil,
			)
			response := httptest.NewRecorder()

			router.ServeHTTP(response, request)

			if response.Code != http.StatusBadRequest {
				t.Fatalf(
					"expected status %d, got %d",
					http.StatusBadRequest,
					response.Code,
				)
			}

			expectedBody := `{"error":"document ID must be a positive integer"}`
			if response.Body.String() != expectedBody {
				t.Fatalf(
					"expected body %s, got %s",
					expectedBody,
					response.Body.String(),
				)
			}

			if service.getByIDCalls != 0 {
				t.Fatalf(
					"expected service not to be called, got %d calls",
					service.getByIDCalls,
				)
			}
		})
	}
}

// TestDocumentHandlerGetByIDReturnsNotFound 验证文档不存在时返回 404。
func TestDocumentHandlerGetByIDReturnsNotFound(t *testing.T) {
	const expectedID int64 = 999

	service := &fakeDocumentQueryService{
		getByIDFunc: func(
			_ context.Context,
			id int64,
		) (documentdomain.Document, error) {
			if id != expectedID {
				t.Fatalf(
					"expected service ID %d, got %d",
					expectedID,
					id,
				)
			}

			return documentdomain.Document{}, documentdomain.ErrNotFound
		},
	}

	router := newTestDocumentRouter(service)
	request := httptest.NewRequest(
		http.MethodGet,
		"/documents/999",
		nil,
	)
	response := httptest.NewRecorder()

	router.ServeHTTP(response, request)

	if response.Code != http.StatusNotFound {
		t.Fatalf(
			"expected status %d, got %d",
			http.StatusNotFound,
			response.Code,
		)
	}

	expectedBody := `{"error":"document not found"}`
	if response.Body.String() != expectedBody {
		t.Fatalf(
			"expected body %s, got %s",
			expectedBody,
			response.Body.String(),
		)
	}

	if service.getByIDCalls != 1 {
		t.Fatalf(
			"expected one service call, got %d",
			service.getByIDCalls,
		)
	}
}

// TestDocumentHandlerGetByIDReturnsInternalServerError 验证未知错误返回 500，
// 并且不会把底层错误详情泄露给客户端。
func TestDocumentHandlerGetByIDReturnsInternalServerError(t *testing.T) {
	internalError := errors.New("database connection failed")

	service := &fakeDocumentQueryService{
		getByIDFunc: func(
			_ context.Context,
			_ int64,
		) (documentdomain.Document, error) {
			return documentdomain.Document{}, internalError
		},
	}

	router := newTestDocumentRouter(service)
	request := httptest.NewRequest(
		http.MethodGet,
		"/documents/42",
		nil,
	)
	response := httptest.NewRecorder()

	router.ServeHTTP(response, request)

	if response.Code != http.StatusInternalServerError {
		t.Fatalf(
			"expected status %d, got %d",
			http.StatusInternalServerError,
			response.Code,
		)
	}

	expectedBody := `{"error":"internal server error"}`
	if response.Body.String() != expectedBody {
		t.Fatalf(
			"expected body %s, got %s",
			expectedBody,
			response.Body.String(),
		)
	}

	if service.getByIDCalls != 1 {
		t.Fatalf(
			"expected one service call, got %d",
			service.getByIDCalls,
		)
	}
}

// TestDocumentHandlerGetByIDReturnsDocument 验证查询成功时返回 200
// 和正确的文档 JSON，并确保内部存储路径不会暴露。
func TestDocumentHandlerGetByIDReturnsDocument(t *testing.T) {
	expectedDocument := documentdomain.Document{
		ID:           42,
		OriginalName: "example.pdf",
		StoragePath:  "private/documents/example.pdf",
		MIMEType:     "application/pdf",
		SizeBytes:    1024,
		SHA256:       strings.Repeat("a", 64),
		Status:       documentdomain.StatusReady,
		ErrorMessage: nil,
		CreatedAt: time.Date(
			2026, time.July, 20,
			10, 30, 0, 0,
			time.UTC,
		),
		UpdatedAt: time.Date(
			2026, time.July, 20,
			10, 35, 0, 0,
			time.UTC,
		),
	}

	service := &fakeDocumentQueryService{
		getByIDFunc: func(
			_ context.Context,
			id int64,
		) (documentdomain.Document, error) {
			if id != expectedDocument.ID {
				t.Fatalf(
					"expected service ID %d, got %d",
					expectedDocument.ID,
					id,
				)
			}

			return expectedDocument, nil
		},
	}

	router := newTestDocumentRouter(service)
	request := httptest.NewRequest(
		http.MethodGet,
		"/documents/42",
		nil,
	)
	response := httptest.NewRecorder()

	router.ServeHTTP(response, request)

	if response.Code != http.StatusOK {
		t.Fatalf(
			"expected status %d, got %d",
			http.StatusOK,
			response.Code,
		)
	}

	responseBody := response.Body.Bytes()

	// storage_path 是服务器内部路径，不应出现在对外 JSON 中。
	if strings.Contains(string(responseBody), "storage_path") {
		t.Fatalf(
			"response must not expose storage_path: %s",
			responseBody,
		)
	}

	var actualResponse documentResponse

	// Unmarshal 把 JSON 字节转换回 Go 结构体，方便逐字段验证。
	if err := json.Unmarshal(responseBody, &actualResponse); err != nil {
		t.Fatalf("decode response JSON: %v", err)
	}

	expectedResponse := newDocumentResponse(expectedDocument)
	if actualResponse != expectedResponse {
		t.Fatalf(
			"expected response %+v, got %+v",
			expectedResponse,
			actualResponse,
		)
	}

	if service.getByIDCalls != 1 {
		t.Fatalf(
			"expected one service call, got %d",
			service.getByIDCalls,
		)
	}
}
