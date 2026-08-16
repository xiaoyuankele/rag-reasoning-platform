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
	"time"

	"github.com/gin-gonic/gin"

	authapplication "rag-reasoning-platform/backend/internal/application/auth"
	userdomain "rag-reasoning-platform/backend/internal/domain/user"
)

func TestAuthLoginHandlerReturnsUserAndSessionCookie(t *testing.T) {
	expiresAt := time.Now().UTC().Add(7 * 24 * time.Hour)
	service := &fakeAuthLoginService{
		output: authapplication.LoginOutput{
			User: userdomain.User{
				ID:          42,
				DisplayName: "Example User",
				Status:      userdomain.StatusActive,
				CreatedAt:   time.Now().UTC(),
			},
			SessionToken:     "login-session-token",
			SessionExpiresAt: expiresAt,
		},
	}
	router := newAuthLoginTestRouter(t, service, &fakeAuthLimiter{allowed: true}, false)

	response := performAuthLoginRequest(
		router,
		`{"identifier":"user@example.com","password":"Example123"}`,
	)
	if response.Code != http.StatusOK {
		t.Fatalf("status=%d want=200 body=%s", response.Code, response.Body.String())
	}
	cookies := response.Result().Cookies()
	if len(cookies) != 1 || cookies[0].Name != sessionCookieName ||
		cookies[0].Value != "login-session-token" || !cookies[0].HttpOnly {
		t.Fatalf("cookies=%+v, want HttpOnly login session", cookies)
	}
	if strings.Contains(response.Body.String(), "login-session-token") {
		t.Fatal("login JSON leaked raw session token")
	}
}

func TestAuthLoginHandlerMapsCredentialAndInternalErrors(t *testing.T) {
	tests := []struct {
		name       string
		serviceErr error
		wantStatus int
		wantCode   string
	}{
		{name: "invalid credentials", serviceErr: authapplication.ErrInvalidCredentials, wantStatus: http.StatusUnauthorized, wantCode: errorCodeInvalidCredentials},
		{name: "internal error", serviceErr: errors.New("database unavailable"), wantStatus: http.StatusInternalServerError, wantCode: errorCodeInternal},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			router := newAuthLoginTestRouter(
				t,
				&fakeAuthLoginService{err: test.serviceErr},
				&fakeAuthLimiter{allowed: true},
				false,
			)
			response := performAuthLoginRequest(
				router,
				`{"identifier":"user@example.com","password":"Example123"}`,
			)
			if response.Code != test.wantStatus {
				t.Fatalf("status=%d want=%d body=%s", response.Code, test.wantStatus, response.Body.String())
			}
			var body errorResponse
			if err := json.Unmarshal(response.Body.Bytes(), &body); err != nil {
				t.Fatalf("decode error response: %v", err)
			}
			if body.Code != test.wantCode {
				t.Fatalf("code=%q want=%q", body.Code, test.wantCode)
			}
			if response.Header().Get("Set-Cookie") != "" {
				t.Fatal("failed login must not set a session cookie")
			}
		})
	}
}

func newAuthLoginTestRouter(
	t *testing.T,
	service authLoginService,
	limiter authRequestLimiter,
	cookieSecure bool,
) http.Handler {
	t.Helper()
	gin.SetMode(gin.TestMode)
	logger := slog.New(slog.NewTextHandler(io.Discard, nil))
	router := NewRouter(logger)
	NewAuthLoginHandler(service, limiter, logger, cookieSecure).RegisterRoutes(router)
	return router
}

func performAuthLoginRequest(router http.Handler, body string) *httptest.ResponseRecorder {
	request := httptest.NewRequest(http.MethodPost, "/auth/login", strings.NewReader(body))
	request.Header.Set("Content-Type", "application/json")
	request.RemoteAddr = "198.51.100.31:54321"
	response := httptest.NewRecorder()
	router.ServeHTTP(response, request)
	return response
}

// fakeAuthLoginService 让 Handler 测试控制登录 Application 的结果。
type fakeAuthLoginService struct {
	output authapplication.LoginOutput
	err    error
}

func (s *fakeAuthLoginService) Login(
	_ context.Context,
	_ authapplication.LoginInput,
) (authapplication.LoginOutput, error) {
	return s.output, s.err
}
