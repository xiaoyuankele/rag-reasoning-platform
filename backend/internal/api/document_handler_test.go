package api

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"log/slog"
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
	logger *slog.Logger,
) *gin.Engine {
	gin.SetMode(gin.TestMode)

	router := gin.New()
	router.Use(RequestIDMiddleware())
	handler := NewDocumentHandler(service, logger)

	// 空前缀 RouterGroup 模拟 B4 后续挂载 AuthMiddleware 的受保护路由组。
	// :id 是动态路径参数，例如 /documents/42 中的 42。
	protectedRoutes := router.Group("")
	handler.RegisterRoutes(protectedRoutes)

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

			var logOutput bytes.Buffer
			logger := slog.New(slog.NewJSONHandler(&logOutput, nil))
			router := newTestDocumentRouter(service, logger)
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

			expectedBody := `{"error":"document ID must be a positive integer","code":"invalid_document_id"}`
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
			if logOutput.Len() != 0 {
				t.Fatalf("invalid ID must not write internal error log: %s", logOutput.String())
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

	var logOutput bytes.Buffer
	logger := slog.New(slog.NewJSONHandler(&logOutput, nil))
	router := newTestDocumentRouter(service, logger)
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

	expectedBody := `{"error":"document not found","code":"document_not_found"}`
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
	if logOutput.Len() != 0 {
		t.Fatalf("not found must not write internal error log: %s", logOutput.String())
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

	var logOutput bytes.Buffer
	logger := slog.New(slog.NewJSONHandler(&logOutput, nil))
	router := newTestDocumentRouter(service, logger)
	request := httptest.NewRequest(
		http.MethodGet,
		"/documents/42",
		nil,
	)
	request.Header.Set(RequestIDHeader, "document-query-error-42")
	response := httptest.NewRecorder()

	router.ServeHTTP(response, request)

	if response.Code != http.StatusInternalServerError {
		t.Fatalf(
			"expected status %d, got %d",
			http.StatusInternalServerError,
			response.Code,
		)
	}

	expectedBody := `{"error":"internal server error","code":"internal_error"}`
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

	// 前端只看到安全响应；原始数据库错误只进入带 request_id 的后端诊断日志。
	var logEntry map[string]any
	if err := json.Unmarshal(bytes.TrimSpace(logOutput.Bytes()), &logEntry); err != nil {
		t.Fatalf("decode internal error log: %v; output = %q", err, logOutput.String())
	}
	assertLogField(t, logEntry, "event", "http_request_failed")
	assertLogField(t, logEntry, "request_id", "document-query-error-42")
	assertLogField(t, logEntry, "public_error_code", "internal_error")
	assertLogField(t, logEntry, "diagnostic_code", "document_get_failed")
	if !strings.Contains(logEntry["error"].(string), internalError.Error()) {
		t.Fatalf("internal log error = %#v, want original error", logEntry["error"])
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

	logger := slog.New(slog.NewJSONHandler(&bytes.Buffer{}, nil))
	router := newTestDocumentRouter(service, logger)
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
