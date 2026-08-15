package api

import (
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/gin-gonic/gin"

	"rag-reasoning-platform/backend/internal/observability"
)

// TestRequestIDMiddleware 验证中间件会沿用合法请求 ID，并替换缺失或非法值。
func TestRequestIDMiddleware(t *testing.T) {
	gin.SetMode(gin.TestMode)

	tests := []struct {
		name              string
		incomingRequestID string
		wantSameID        bool
	}{
		{
			name:              "preserve valid caller ID",
			incomingRequestID: "frontend-request-123",
			wantSameID:        true,
		},
		{
			name: "generate missing ID",
		},
		{
			name:              "replace invalid ID",
			incomingRequestID: "invalid request id",
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			router := gin.New()
			router.Use(RequestIDMiddleware())
			router.GET("/request-id", func(c *gin.Context) {
				requestID, ok := observability.RequestIDFromContext(
					c.Request.Context(),
				)
				if !ok {
					c.Status(http.StatusInternalServerError)
					return
				}
				c.String(http.StatusOK, requestID)
			})

			request := httptest.NewRequest(http.MethodGet, "/request-id", nil)
			if test.incomingRequestID != "" {
				request.Header.Set(RequestIDHeader, test.incomingRequestID)
			}
			response := httptest.NewRecorder()

			router.ServeHTTP(response, request)

			if response.Code != http.StatusOK {
				t.Fatalf("status = %d, want %d", response.Code, http.StatusOK)
			}

			responseRequestID := response.Header().Get(RequestIDHeader)
			if responseRequestID == "" {
				t.Fatal("response request ID is blank")
			}
			if response.Body.String() != responseRequestID {
				t.Fatalf(
					"context request ID = %q, response header = %q",
					response.Body.String(),
					responseRequestID,
				)
			}

			if test.wantSameID && responseRequestID != test.incomingRequestID {
				t.Fatalf(
					"response request ID = %q, want preserved %q",
					responseRequestID,
					test.incomingRequestID,
				)
			}
			if !test.wantSameID && !isValidRequestID(responseRequestID) {
				t.Fatalf("generated request ID %q is invalid", responseRequestID)
			}
			if !test.wantSameID && responseRequestID == test.incomingRequestID {
				t.Fatalf("invalid request ID %q was not replaced", responseRequestID)
			}
		})
	}
}
