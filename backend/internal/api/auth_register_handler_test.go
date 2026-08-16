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

func TestAuthRegisterHandlerCreatesSessionCookie(t *testing.T) {
	createdAt := time.Now().UTC().Add(-time.Minute)
	expiresAt := time.Now().UTC().Add(7 * 24 * time.Hour)
	email := "user@example.com"
	service := &fakeAuthRegisterService{
		output: authapplication.RegisterOutput{
			User: userdomain.User{
				ID:          42,
				Email:       &email,
				DisplayName: "Example User",
				Status:      userdomain.StatusActive,
				CreatedAt:   createdAt,
			},
			SessionToken:     "private-session-token",
			SessionExpiresAt: expiresAt,
		},
	}
	router := newAuthRegisterTestRouter(t, service, &fakeAuthLimiter{allowed: true}, true)

	response := performAuthRegisterRequest(
		router,
		`{"verification_id":12,"verification_code":"483921","display_name":"Example User","password":"Example123"}`,
	)
	if response.Code != http.StatusCreated {
		t.Fatalf("status = %d, want %d; body=%s", response.Code, http.StatusCreated, response.Body.String())
	}

	cookies := response.Result().Cookies()
	if len(cookies) != 1 {
		t.Fatalf("cookies = %+v, want one session cookie", cookies)
	}
	cookie := cookies[0]
	if cookie.Name != sessionCookieName || cookie.Value != "private-session-token" ||
		!cookie.HttpOnly || !cookie.Secure || cookie.Path != "/" ||
		cookie.SameSite != http.SameSiteLaxMode || !cookie.Expires.Equal(expiresAt.Truncate(time.Second)) {
		t.Fatalf("session cookie = %+v, want secure HttpOnly Lax cookie", cookie)
	}
	if strings.Contains(response.Body.String(), "private-session-token") {
		t.Fatalf("response leaked raw session token: %s", response.Body.String())
	}

	var body struct {
		User struct {
			ID    int64   `json:"id"`
			Email *string `json:"email"`
		} `json:"user"`
		SessionExpiresAt time.Time `json:"session_expires_at"`
	}
	if err := json.Unmarshal(response.Body.Bytes(), &body); err != nil {
		t.Fatalf("decode response: %v", err)
	}
	if body.User.ID != 42 || body.User.Email == nil || *body.User.Email != email ||
		!body.SessionExpiresAt.Equal(expiresAt) {
		t.Fatalf("response = %+v, want public user and expiry", body)
	}
}

func TestAuthRegisterHandlerMapsStableErrors(t *testing.T) {
	tests := []struct {
		name       string
		serviceErr error
		wantStatus int
		wantCode   string
	}{
		{name: "invalid request", serviceErr: authapplication.ErrInvalidRegistrationRequest, wantStatus: http.StatusBadRequest, wantCode: errorCodeInvalidAuthRequest},
		{name: "invalid code", serviceErr: authapplication.ErrVerificationCodeInvalid, wantStatus: http.StatusBadRequest, wantCode: errorCodeVerificationCodeInvalid},
		{name: "expired code", serviceErr: authapplication.ErrVerificationCodeExpired, wantStatus: http.StatusBadRequest, wantCode: errorCodeVerificationCodeExpired},
		{name: "attempts exceeded", serviceErr: authapplication.ErrVerificationAttemptsExceeded, wantStatus: http.StatusTooManyRequests, wantCode: errorCodeVerificationAttemptsExceeded},
		{name: "duplicate contact", serviceErr: authapplication.ErrContactAlreadyRegistered, wantStatus: http.StatusConflict, wantCode: errorCodeContactAlreadyRegistered},
		{name: "internal error", serviceErr: errors.New("database unavailable"), wantStatus: http.StatusInternalServerError, wantCode: errorCodeInternal},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			router := newAuthRegisterTestRouter(
				t,
				&fakeAuthRegisterService{err: test.serviceErr},
				&fakeAuthLimiter{allowed: true},
				false,
			)
			response := performAuthRegisterRequest(
				router,
				`{"verification_id":12,"verification_code":"483921","display_name":"Example User","password":"Example123"}`,
			)
			if response.Code != test.wantStatus {
				t.Fatalf("status = %d, want %d; body=%s", response.Code, test.wantStatus, response.Body.String())
			}
			var body errorResponse
			if err := json.Unmarshal(response.Body.Bytes(), &body); err != nil {
				t.Fatalf("decode error response: %v", err)
			}
			if body.Code != test.wantCode {
				t.Fatalf("error code = %q, want %q", body.Code, test.wantCode)
			}
			if response.Header().Get("Set-Cookie") != "" {
				t.Fatal("failed registration must not set a session cookie")
			}
		})
	}
}

func TestAuthRegisterHandlerRejectsMalformedJSONBeforeService(t *testing.T) {
	service := &fakeAuthRegisterService{}
	router := newAuthRegisterTestRouter(t, service, &fakeAuthLimiter{allowed: true}, false)

	response := performAuthRegisterRequest(router, `{`)
	if response.Code != http.StatusBadRequest || service.calls != 0 {
		t.Fatalf("status=%d service calls=%d, want 400 and 0", response.Code, service.calls)
	}
}

func newAuthRegisterTestRouter(
	t *testing.T,
	service authRegisterService,
	limiter authRequestLimiter,
	cookieSecure bool,
) http.Handler {
	t.Helper()
	gin.SetMode(gin.TestMode)
	logger := slog.New(slog.NewTextHandler(io.Discard, nil))
	router := NewRouter(logger)
	NewAuthRegisterHandler(service, limiter, logger, cookieSecure).RegisterRoutes(router)
	return router
}

func performAuthRegisterRequest(router http.Handler, body string) *httptest.ResponseRecorder {
	request := httptest.NewRequest(http.MethodPost, "/auth/register", strings.NewReader(body))
	request.Header.Set("Content-Type", "application/json")
	request.RemoteAddr = "198.51.100.30:54321"
	response := httptest.NewRecorder()
	router.ServeHTTP(response, request)
	return response
}

// fakeAuthRegisterService 让 Handler 测试控制 Application 的输出和错误。
type fakeAuthRegisterService struct {
	output authapplication.RegisterOutput
	err    error
	calls  int
}

func (s *fakeAuthRegisterService) Register(
	_ context.Context,
	_ authapplication.RegisterInput,
) (authapplication.RegisterOutput, error) {
	s.calls++
	return s.output, s.err
}

// fakeAuthLimiter 让 Handler 测试决定当前请求是否通过限流。
type fakeAuthLimiter struct {
	allowed bool
}

func (l *fakeAuthLimiter) Allow(_ string, now time.Time) (time.Time, bool) {
	return now.Add(time.Minute), l.allowed
}
