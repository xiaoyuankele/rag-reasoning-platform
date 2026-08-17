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

// authPasswordResetService 是 Handler 对密码重置 Application 的最小需求。
type authPasswordResetService interface {
	ResetPassword(
		ctx context.Context,
		input authapplication.PasswordResetInput,
	) error
}

// AuthPasswordResetHandler 负责密码重置请求、错误映射和旧 Cookie 清理。
type AuthPasswordResetHandler struct {
	service      authPasswordResetService
	limiter      authRequestLimiter
	logger       *slog.Logger
	cookieSecure bool
}

// NewAuthPasswordResetHandler 创建公开但受限流保护的密码重置 Handler。
func NewAuthPasswordResetHandler(
	service authPasswordResetService,
	limiter authRequestLimiter,
	logger *slog.Logger,
	cookieSecure bool,
) *AuthPasswordResetHandler {
	if service == nil {
		panic("NewAuthPasswordResetHandler requires a non-nil service")
	}
	if limiter == nil {
		panic("NewAuthPasswordResetHandler requires a non-nil limiter")
	}
	if logger == nil {
		panic("NewAuthPasswordResetHandler requires a non-nil logger")
	}
	return &AuthPasswordResetHandler{
		service:      service,
		limiter:      limiter,
		logger:       logger,
		cookieSecure: cookieSecure,
	}
}

// RegisterRoutes 注册无需现有 Session 的密码重置路由。
func (h *AuthPasswordResetHandler) RegisterRoutes(router gin.IRoutes) {
	router.POST("/auth/password-reset", h.ResetPassword)
}

type authPasswordResetRequest struct {
	VerificationID   int64  `json:"verification_id"`
	VerificationCode string `json:"verification_code"`
	NewPassword      string `json:"new_password"`
}

// ResetPassword 处理 POST /auth/password-reset。
func (h *AuthPasswordResetHandler) ResetPassword(c *gin.Context) {
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

	var request authPasswordResetRequest
	if err := c.ShouldBindJSON(&request); err != nil {
		writeErrorResponse(
			c,
			http.StatusBadRequest,
			errorCodeInvalidPasswordResetRequest,
			"request body must be valid JSON",
		)
		return
	}

	err := h.service.ResetPassword(
		c.Request.Context(),
		authapplication.PasswordResetInput{
			VerificationID:   request.VerificationID,
			VerificationCode: request.VerificationCode,
			NewPassword:      request.NewPassword,
		},
	)
	if h.writePasswordResetError(c, err) {
		return
	}

	// 数据库已经撤销该用户的全部 Session；同时删除当前浏览器可能仍持有的旧 Cookie。
	clearSessionCookie(c, h.cookieSecure)
	c.Status(http.StatusNoContent)
}

func (h *AuthPasswordResetHandler) writePasswordResetError(
	c *gin.Context,
	err error,
) bool {
	if err == nil {
		return false
	}

	switch {
	case errors.Is(err, authapplication.ErrInvalidPasswordResetRequest) ||
		isInvalidPasswordInput(err):
		writeErrorResponse(c, http.StatusBadRequest, errorCodeInvalidPasswordResetRequest, "password reset request is invalid")
	case errors.Is(err, authapplication.ErrVerificationCodeInvalid):
		writeErrorResponse(c, http.StatusBadRequest, errorCodeVerificationCodeInvalid, "verification code is invalid")
	case errors.Is(err, authapplication.ErrVerificationCodeExpired):
		writeErrorResponse(c, http.StatusBadRequest, errorCodeVerificationCodeExpired, "verification code has expired")
	case errors.Is(err, authapplication.ErrVerificationAttemptsExceeded):
		writeErrorResponse(c, http.StatusTooManyRequests, errorCodeVerificationAttemptsExceeded, "verification attempts exceeded")
	default:
		writeInternalErrorResponse(c, h.logger, "auth_password_reset_failed", err)
	}
	return true
}
