package api

import (
	"context"
	"log/slog"
	"net/http"

	"github.com/gin-gonic/gin"
)

// authLogoutService 是 Handler 对幂等退出 Application 的最小需求。
type authLogoutService interface {
	Logout(ctx context.Context, rawToken string) error
}

// AuthLogoutHandler 负责撤销当前 Session 并清除 Cookie。
type AuthLogoutHandler struct {
	service      authLogoutService
	logger       *slog.Logger
	cookieSecure bool
}

// NewAuthLogoutHandler 创建退出 Handler。
func NewAuthLogoutHandler(
	service authLogoutService,
	logger *slog.Logger,
	cookieSecure bool,
) *AuthLogoutHandler {
	if service == nil {
		panic("NewAuthLogoutHandler requires a non-nil service")
	}
	if logger == nil {
		panic("NewAuthLogoutHandler requires a non-nil logger")
	}
	return &AuthLogoutHandler{
		service:      service,
		logger:       logger,
		cookieSecure: cookieSecure,
	}
}

// RegisterRoutes 注册无需有效 Session 也保持幂等的退出路由。
func (h *AuthLogoutHandler) RegisterRoutes(router *gin.Engine) {
	router.POST("/auth/logout", h.Logout)
}

// Logout 处理 POST /auth/logout。
func (h *AuthLogoutHandler) Logout(c *gin.Context) {
	rawToken, _ := c.Cookie(sessionCookieName)
	// 无论数据库撤销是否成功，都先清除当前浏览器持有的 Cookie。
	clearSessionCookie(c, h.cookieSecure)
	if err := h.service.Logout(c.Request.Context(), rawToken); err != nil {
		writeInternalErrorResponse(c, h.logger, "auth_logout_failed", err)
		return
	}
	c.Status(http.StatusNoContent)
}
