package api

import (
	"context"
	"errors"
	"log/slog"
	"net/http"

	"github.com/gin-gonic/gin"

	authapplication "rag-reasoning-platform/backend/internal/application/auth"
)

const authenticatedIdentityContextKey = "authenticated_identity"

// sessionAuthenticationService 是 Middleware 对认证 Application 的最小需求。
type sessionAuthenticationService interface {
	Authenticate(
		ctx context.Context,
		rawToken string,
	) (authapplication.AuthenticatedIdentity, error)
}

// AuthMiddleware 把有效 Cookie 恢复成可信身份，并阻止未认证请求继续执行。
type AuthMiddleware struct {
	service sessionAuthenticationService
	logger  *slog.Logger
}

// NewAuthMiddleware 创建 Session 鉴权中间件。
func NewAuthMiddleware(
	service sessionAuthenticationService,
	logger *slog.Logger,
) *AuthMiddleware {
	if service == nil {
		panic("NewAuthMiddleware requires a non-nil service")
	}
	if logger == nil {
		panic("NewAuthMiddleware requires a non-nil logger")
	}
	return &AuthMiddleware{service: service, logger: logger}
}

// Require 验证 rag_session；成功后把 Identity 放入 Gin Context。
func (m *AuthMiddleware) Require(c *gin.Context) {
	rawToken, err := c.Cookie(sessionCookieName)
	if err != nil {
		writeAuthenticationRequired(c)
		c.Abort()
		return
	}

	identity, err := m.service.Authenticate(c.Request.Context(), rawToken)
	if errors.Is(err, authapplication.ErrAuthenticationRequired) {
		writeAuthenticationRequired(c)
		c.Abort()
		return
	}
	if err != nil {
		writeInternalErrorResponse(c, m.logger, "session_authentication_failed", err)
		c.Abort()
		return
	}

	c.Set(authenticatedIdentityContextKey, identity)
	c.Next()
}

func writeAuthenticationRequired(c *gin.Context) {
	writeErrorResponse(
		c,
		http.StatusUnauthorized,
		errorCodeAuthenticationRequired,
		"authentication is required",
	)
}

func authenticatedIdentityFromContext(
	c *gin.Context,
) (authapplication.AuthenticatedIdentity, bool) {
	value, found := c.Get(authenticatedIdentityContextKey)
	if !found {
		return authapplication.AuthenticatedIdentity{}, false
	}
	identity, ok := value.(authapplication.AuthenticatedIdentity)
	return identity, ok
}
