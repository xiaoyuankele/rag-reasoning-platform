package api

import (
	"context"
	"errors"
	"log/slog"
	"net/http"
	"time"

	"github.com/gin-gonic/gin"

	authapplication "rag-reasoning-platform/backend/internal/application/auth"
)

// authLoginService 是 Handler 对登录 Application 的最小需求。
type authLoginService interface {
	Login(
		ctx context.Context,
		input authapplication.LoginInput,
	) (authapplication.LoginOutput, error)
}

// AuthLoginHandler 负责登录请求、统一凭据错误和 Session Cookie。
type AuthLoginHandler struct {
	service      authLoginService
	limiter      authRequestLimiter
	logger       *slog.Logger
	cookieSecure bool
}

// NewAuthLoginHandler 创建登录 Handler。
func NewAuthLoginHandler(
	service authLoginService,
	limiter authRequestLimiter,
	logger *slog.Logger,
	cookieSecure bool,
) *AuthLoginHandler {
	if service == nil {
		panic("NewAuthLoginHandler requires a non-nil service")
	}
	if limiter == nil {
		panic("NewAuthLoginHandler requires a non-nil limiter")
	}
	if logger == nil {
		panic("NewAuthLoginHandler requires a non-nil logger")
	}
	return &AuthLoginHandler{
		service:      service,
		limiter:      limiter,
		logger:       logger,
		cookieSecure: cookieSecure,
	}
}

// RegisterRoutes 注册公开但受限流保护的登录路由。
func (h *AuthLoginHandler) RegisterRoutes(router *gin.Engine) {
	router.POST("/auth/login", h.Login)
}

type authLoginRequest struct {
	Identifier string `json:"identifier"`
	Password   string `json:"password"`
}

// Login 处理 POST /auth/login。
func (h *AuthLoginHandler) Login(c *gin.Context) {
	now := time.Now().UTC()
	if retryAt, allowed := h.limiter.Allow(
		verificationClientKey(c.Request),
		now,
	); !allowed {
		writeVerificationRetryAfter(c, now, retryAt)
		writeErrorResponse(
			c,
			http.StatusTooManyRequests,
			errorCodeAuthRequestThrottled,
			"authentication requests are temporarily limited",
		)
		return
	}

	var request authLoginRequest
	if err := c.ShouldBindJSON(&request); err != nil {
		writeErrorResponse(
			c,
			http.StatusBadRequest,
			errorCodeInvalidAuthRequest,
			"request body must be valid JSON",
		)
		return
	}

	result, err := h.service.Login(
		c.Request.Context(),
		authapplication.LoginInput{
			Identifier: request.Identifier,
			Password:   request.Password,
		},
	)
	if errors.Is(err, authapplication.ErrInvalidCredentials) {
		writeErrorResponse(
			c,
			http.StatusUnauthorized,
			errorCodeInvalidCredentials,
			"identifier or password is incorrect",
		)
		return
	}
	if err != nil {
		writeInternalErrorResponse(c, h.logger, "auth_login_failed", err)
		return
	}

	writeAuthSessionResponse(
		c,
		http.StatusOK,
		result.User,
		result.SessionToken,
		result.SessionExpiresAt,
		h.cookieSecure,
	)
}
