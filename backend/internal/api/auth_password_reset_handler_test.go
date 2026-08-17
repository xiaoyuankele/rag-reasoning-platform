package api

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/gin-gonic/gin"

	authapplication "rag-reasoning-platform/backend/internal/application/auth"
)

func TestAuthPasswordResetHandlerClearsOldCookie(t *testing.T) {
	service := &fakeAuthPasswordResetService{}
	router := newAuthPasswordResetTestRouter(
		t,
		service,
		&fakeAuthLimiter{allowed: true},
		true,
	)

	response := performAuthPasswordResetRequest(
		router,
		`{"verification_id":21,"verification_code":"483921","new_password":"Changed123"}`,
	)
	if response.Code != http.StatusNoContent {
		t.Fatalf("status = %d, want 204; body=%s", response.Code, response.Body.String())
	}
	if service.input.VerificationID != 21 ||
		service.input.VerificationCode != "483921" ||
		service.input.NewPassword != "Changed123" {
		t.Fatalf("service input = %+v", service.input)
	}
	cookies := response.Result().Cookies()
	if len(cookies) != 1 || cookies[0].Name != sessionCookieName ||
		cookies[0].MaxAge != -1 || !cookies[0].HttpOnly || !cookies[0].Secure {
		t.Fatalf("reset cookie = %+v, want deleted secure session cookie", cookies)
	}
}

func TestAuthPasswordResetHandlerMapsStableErrors(t *testing.T) {
	tests := []struct {
		name       string
		serviceErr error
		wantStatus int
		wantCode   string
	}{
		{name: "invalid request", serviceErr: authapplication.ErrInvalidPasswordResetRequest, wantStatus: http.StatusBadRequest, wantCode: errorCodeInvalidPasswordResetRequest},
		{name: "invalid code", serviceErr: authapplication.ErrVerificationCodeInvalid, wantStatus: http.StatusBadRequest, wantCode: errorCodeVerificationCodeInvalid},
		{name: "expired code", serviceErr: authapplication.ErrVerificationCodeExpired, wantStatus: http.StatusBadRequest, wantCode: errorCodeVerificationCodeExpired},
		{name: "attempts exceeded", serviceErr: authapplication.ErrVerificationAttemptsExceeded, wantStatus: http.StatusTooManyRequests, wantCode: errorCodeVerificationAttemptsExceeded},
		{name: "internal error", serviceErr: errors.New("database unavailable"), wantStatus: http.StatusInternalServerError, wantCode: errorCodeInternal},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			router := newAuthPasswordResetTestRouter(
				t,
				&fakeAuthPasswordResetService{err: test.serviceErr},
				&fakeAuthLimiter{allowed: true},
				false,
			)
			response := performAuthPasswordResetRequest(
				router,
				`{"verification_id":21,"verification_code":"483921","new_password":"Changed123"}`,
			)
			if response.Code != test.wantStatus {
				t.Fatalf("status = %d, want %d; body=%s", response.Code, test.wantStatus, response.Body.String())
			}
			var body errorResponse
			if err := json.Unmarshal(response.Body.Bytes(), &body); err != nil {
				t.Fatalf("decode response: %v", err)
			}
			if body.Code != test.wantCode {
				t.Fatalf("code = %q, want %q", body.Code, test.wantCode)
			}
			if response.Header().Get("Set-Cookie") != "" {
				t.Fatal("failed reset must not modify the session cookie")
			}
		})
	}
}

func TestAuthPasswordResetHandlerRejectsMalformedJSON(t *testing.T) {
	service := &fakeAuthPasswordResetService{}
	router := newAuthPasswordResetTestRouter(t, service, &fakeAuthLimiter{allowed: true}, false)
	response := performAuthPasswordResetRequest(router, `{`)
	if response.Code != http.StatusBadRequest || service.calls != 0 {
		t.Fatalf("status=%d service calls=%d, want 400/0", response.Code, service.calls)
	}
}

func newAuthPasswordResetTestRouter(
	t *testing.T,
	service authPasswordResetService,
	limiter authRequestLimiter,
	cookieSecure bool,
) http.Handler {
	t.Helper()
	gin.SetMode(gin.TestMode)
	logger := slog.New(slog.NewTextHandler(io.Discard, nil))
	router := NewRouter(logger)
	NewAuthPasswordResetHandler(service, limiter, logger, cookieSecure).RegisterRoutes(router)
	return router
}

func performAuthPasswordResetRequest(
	router http.Handler,
	body string,
) *httptest.ResponseRecorder {
	request := httptest.NewRequest(
		http.MethodPost,
		"/auth/password-reset",
		strings.NewReader(body),
	)
	request.Header.Set("Content-Type", "application/json")
	request.RemoteAddr = "198.51.100.31:54321"
	response := httptest.NewRecorder()
	router.ServeHTTP(response, request)
	return response
}

// fakeAuthPasswordResetService 让 Handler 测试控制 Application 的结果。
type fakeAuthPasswordResetService struct {
	input authapplication.PasswordResetInput
	err   error
	calls int
}

func (s *fakeAuthPasswordResetService) ResetPassword(
	_ context.Context,
	input authapplication.PasswordResetInput,
) error {
	s.calls++
	s.input = input
	return s.err
}
