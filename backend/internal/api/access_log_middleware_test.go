package api

import (
	"bytes"
	"encoding/json"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/gin-gonic/gin"
)

// TestAccessLogMiddlewareWritesStructuredEntry 验证访问日志使用 JSON 字段记录
// 请求 ID、路由模板、状态码和日志级别，不把具体资源 ID 当成路由名。
func TestAccessLogMiddlewareWritesStructuredEntry(t *testing.T) {
	gin.SetMode(gin.TestMode)

	var output bytes.Buffer
	logger := slog.New(slog.NewJSONHandler(&output, nil))

	router := gin.New()
	router.Use(RequestIDMiddleware(), AccessLogMiddleware(logger))
	router.GET("/documents/:id", func(c *gin.Context) {
		c.Status(http.StatusNotFound)
	})

	request := httptest.NewRequest(http.MethodGet, "/documents/42", nil)
	request.Header.Set(RequestIDHeader, "test-request-42")
	response := httptest.NewRecorder()

	router.ServeHTTP(response, request)

	var entry map[string]any
	if err := json.Unmarshal(bytes.TrimSpace(output.Bytes()), &entry); err != nil {
		t.Fatalf("decode structured log: %v; output = %q", err, output.String())
	}

	assertLogField(t, entry, "event", "http_request_completed")
	assertLogField(t, entry, "request_id", "test-request-42")
	assertLogField(t, entry, "method", http.MethodGet)
	assertLogField(t, entry, "route", "/documents/:id")
	assertLogField(t, entry, "level", "WARN")

	statusCode, ok := entry["status_code"].(float64)
	if !ok || int(statusCode) != http.StatusNotFound {
		t.Fatalf("status_code = %#v, want %d", entry["status_code"], http.StatusNotFound)
	}
	if _, ok := entry["duration_ms"]; !ok {
		t.Fatal("duration_ms field is missing")
	}
}

// TestAccessLogLevel 验证 HTTP 状态码会映射到便于告警筛选的日志级别。
func TestAccessLogLevel(t *testing.T) {
	tests := []struct {
		name       string
		statusCode int
		wantLevel  slog.Level
	}{
		{name: "success", statusCode: http.StatusOK, wantLevel: slog.LevelInfo},
		{name: "redirect", statusCode: http.StatusTemporaryRedirect, wantLevel: slog.LevelInfo},
		{name: "client error", statusCode: http.StatusBadRequest, wantLevel: slog.LevelWarn},
		{name: "server error", statusCode: http.StatusInternalServerError, wantLevel: slog.LevelError},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			if actual := accessLogLevel(test.statusCode); actual != test.wantLevel {
				t.Fatalf(
					"accessLogLevel(%d) = %s, want %s",
					test.statusCode,
					actual,
					test.wantLevel,
				)
			}
		})
	}
}

func assertLogField(
	t *testing.T,
	entry map[string]any,
	field string,
	want string,
) {
	t.Helper()

	if actual, ok := entry[field].(string); !ok || actual != want {
		t.Fatalf("%s = %#v, want %q", field, entry[field], want)
	}
}
