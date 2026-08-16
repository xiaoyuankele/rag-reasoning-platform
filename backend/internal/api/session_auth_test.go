package api

import (
	"context"
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/gin-gonic/gin"

	authapplication "rag-reasoning-platform/backend/internal/application/auth"
	userdomain "rag-reasoning-platform/backend/internal/domain/user"
)

func TestAuthMiddlewareAllowsCurrentUserWithValidSession(t *testing.T) {
	service := &fakeSessionAuthService{
		identity: authapplication.AuthenticatedIdentity{
			Actor: authapplication.Actor{UserID: 42, SessionID: 9},
			User: userdomain.User{
				ID:          42,
				DisplayName: "Current User",
				Status:      userdomain.StatusActive,
				CreatedAt:   time.Now().UTC(),
			},
		},
	}
	router := newCurrentUserTestRouter(t, service)
	request := httptest.NewRequest(http.MethodGet, "/users/me", nil)
	request.AddCookie(&http.Cookie{Name: sessionCookieName, Value: "raw-session-token"})
	response := httptest.NewRecorder()

	router.ServeHTTP(response, request)
	if response.Code != http.StatusOK {
		t.Fatalf("status=%d want=200 body=%s", response.Code, response.Body.String())
	}
	if service.authToken != "raw-session-token" {
		t.Fatalf("Authenticate() token=%q, want Cookie token", service.authToken)
	}
	var body struct {
		User publicUserResponse `json:"user"`
	}
	if err := json.Unmarshal(response.Body.Bytes(), &body); err != nil {
		t.Fatalf("decode response: %v", err)
	}
	if body.User.ID != 42 || body.User.DisplayName != "Current User" {
		t.Fatalf("current user response=%+v", body)
	}
}

func TestAuthMiddlewareRejectsMissingAndInvalidSessions(t *testing.T) {
	tests := []struct {
		name       string
		addCookie  bool
		serviceErr error
	}{
		{name: "missing cookie"},
		{name: "invalid session", addCookie: true, serviceErr: authapplication.ErrAuthenticationRequired},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			service := &fakeSessionAuthService{authErr: test.serviceErr}
			router := newCurrentUserTestRouter(t, service)
			request := httptest.NewRequest(http.MethodGet, "/users/me", nil)
			if test.addCookie {
				request.AddCookie(&http.Cookie{Name: sessionCookieName, Value: "invalid-token"})
			}
			response := httptest.NewRecorder()

			router.ServeHTTP(response, request)
			if response.Code != http.StatusUnauthorized {
				t.Fatalf("status=%d want=401 body=%s", response.Code, response.Body.String())
			}
			var body errorResponse
			if err := json.Unmarshal(response.Body.Bytes(), &body); err != nil {
				t.Fatalf("decode response: %v", err)
			}
			if body.Code != errorCodeAuthenticationRequired {
				t.Fatalf("code=%q want=%q", body.Code, errorCodeAuthenticationRequired)
			}
		})
	}
}

func TestAuthLogoutHandlerRevokesAndClearsCookie(t *testing.T) {
	service := &fakeSessionAuthService{}
	router := newLogoutTestRouter(t, service, true)
	request := httptest.NewRequest(http.MethodPost, "/auth/logout", nil)
	request.AddCookie(&http.Cookie{Name: sessionCookieName, Value: "raw-session-token"})
	response := httptest.NewRecorder()

	router.ServeHTTP(response, request)
	if response.Code != http.StatusNoContent {
		t.Fatalf("status=%d want=204 body=%s", response.Code, response.Body.String())
	}
	if service.logoutToken != "raw-session-token" {
		t.Fatalf("Logout() token=%q, want Cookie token", service.logoutToken)
	}
	cookies := response.Result().Cookies()
	if len(cookies) != 1 || cookies[0].Name != sessionCookieName ||
		cookies[0].Value != "" || cookies[0].MaxAge != -1 ||
		!cookies[0].HttpOnly || !cookies[0].Secure {
		t.Fatalf("cleared cookie=%+v", cookies)
	}
}

func TestAuthLogoutHandlerIsIdempotentWithoutCookie(t *testing.T) {
	service := &fakeSessionAuthService{}
	router := newLogoutTestRouter(t, service, false)
	request := httptest.NewRequest(http.MethodPost, "/auth/logout", nil)
	response := httptest.NewRecorder()

	router.ServeHTTP(response, request)
	if response.Code != http.StatusNoContent || service.logoutToken != "" {
		t.Fatalf("status=%d logoutToken=%q, want idempotent 204", response.Code, service.logoutToken)
	}
}

func newCurrentUserTestRouter(t *testing.T, service *fakeSessionAuthService) http.Handler {
	t.Helper()
	gin.SetMode(gin.TestMode)
	logger := slog.New(slog.NewTextHandler(io.Discard, nil))
	router := NewRouter(logger)
	middleware := NewAuthMiddleware(service, logger)
	users := router.Group("/users")
	users.Use(middleware.Require)
	NewCurrentUserHandler().RegisterRoutes(users)
	return router
}

func newLogoutTestRouter(
	t *testing.T,
	service *fakeSessionAuthService,
	cookieSecure bool,
) http.Handler {
	t.Helper()
	gin.SetMode(gin.TestMode)
	logger := slog.New(slog.NewTextHandler(io.Discard, nil))
	router := NewRouter(logger)
	NewAuthLogoutHandler(service, logger, cookieSecure).RegisterRoutes(router)
	return router
}

// fakeSessionAuthService 同时满足 Middleware 认证和退出 Handler 的端口。
type fakeSessionAuthService struct {
	identity    authapplication.AuthenticatedIdentity
	authErr     error
	authToken   string
	logoutErr   error
	logoutToken string
}

func (s *fakeSessionAuthService) Authenticate(
	_ context.Context,
	rawToken string,
) (authapplication.AuthenticatedIdentity, error) {
	s.authToken = rawToken
	return s.identity, s.authErr
}

func (s *fakeSessionAuthService) Logout(
	_ context.Context,
	rawToken string,
) error {
	s.logoutToken = rawToken
	return s.logoutErr
}
