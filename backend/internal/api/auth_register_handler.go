package api

import (
	"context"
	"errors"
	"log/slog"
	"net/http"
	"time"

	"github.com/gin-gonic/gin"

	authapplication "rag-reasoning-platform/backend/internal/application/auth"
	userdomain "rag-reasoning-platform/backend/internal/domain/user"
)

const sessionCookieName = "rag_session"

// authRegisterService 是 Handler 对注册 Application 的最小需求。
type authRegisterService interface {
	Register(
		ctx context.Context,
		input authapplication.RegisterInput,
	) (authapplication.RegisterOutput, error)
}

// authRequestLimiter 是公开认证接口的单实例限流端口。
type authRequestLimiter interface {
	Allow(clientKey string, now time.Time) (retryAt time.Time, allowed bool)
}

// AuthRegisterHandler 负责注册请求、HTTP 错误映射和 Session Cookie。
type AuthRegisterHandler struct {
	service      authRegisterService
	limiter      authRequestLimiter
	logger       *slog.Logger
	cookieSecure bool
}

// NewAuthRegisterHandler 创建注册 Handler。
func NewAuthRegisterHandler(
	service authRegisterService,
	limiter authRequestLimiter,
	logger *slog.Logger,
	cookieSecure bool,
) *AuthRegisterHandler {
	if service == nil {
		panic("NewAuthRegisterHandler requires a non-nil service")
	}
	if limiter == nil {
		panic("NewAuthRegisterHandler requires a non-nil limiter")
	}
	if logger == nil {
		panic("NewAuthRegisterHandler requires a non-nil logger")
	}
	return &AuthRegisterHandler{
		service:      service,
		limiter:      limiter,
		logger:       logger,
		cookieSecure: cookieSecure,
	}
}

// RegisterRoutes 注册公开但受限流保护的注册路由。
func (h *AuthRegisterHandler) RegisterRoutes(router *gin.Engine) {
	router.POST("/auth/register", h.Register)
}

type authRegisterRequest struct {
	VerificationID   int64  `json:"verification_id"`
	VerificationCode string `json:"verification_code"`
	DisplayName      string `json:"display_name"`
	Password         string `json:"password"`
}

type authRegisterResponse struct {
	User             publicUserResponse `json:"user"`
	SessionExpiresAt time.Time          `json:"session_expires_at"`
}

// Register 处理 POST /auth/register。
func (h *AuthRegisterHandler) Register(c *gin.Context) {
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

	var request authRegisterRequest
	if err := c.ShouldBindJSON(&request); err != nil {
		writeErrorResponse(
			c,
			http.StatusBadRequest,
			errorCodeInvalidAuthRequest,
			"request body must be valid JSON",
		)
		return
	}

	result, err := h.service.Register(
		c.Request.Context(),
		authapplication.RegisterInput{
			VerificationID:   request.VerificationID,
			VerificationCode: request.VerificationCode,
			DisplayName:      request.DisplayName,
			Password:         request.Password,
		},
	)
	if h.writeRegisterError(c, err) {
		return
	}

	// 只有数据库事务成功提交后才设置 Cookie；浏览器永远接触不到 token_hash，
	// JSON 响应也永远不包含原始 Token。
	http.SetCookie(
		c.Writer,
		&http.Cookie{
			Name:     sessionCookieName,
			Value:    result.SessionToken,
			Path:     "/",
			Expires:  result.SessionExpiresAt.UTC(),
			HttpOnly: true,
			Secure:   h.cookieSecure,
			SameSite: http.SameSiteLaxMode,
		},
	)
	c.JSON(
		http.StatusCreated,
		authRegisterResponse{
			User:             newPublicUserResponse(result.User),
			SessionExpiresAt: result.SessionExpiresAt.UTC(),
		},
	)
}

// writeRegisterError 把 Application 的稳定错误类别映射成公开 HTTP 契约。
// 返回 true 表示已经写入响应，调用方必须立即结束 Handler。
func (h *AuthRegisterHandler) writeRegisterError(c *gin.Context, err error) bool {
	if err == nil {
		return false
	}

	switch {
	case isInvalidRegistrationInput(err):
		writeErrorResponse(c, http.StatusBadRequest, errorCodeInvalidAuthRequest, "registration request is invalid")
	case errors.Is(err, authapplication.ErrVerificationCodeInvalid):
		writeErrorResponse(c, http.StatusBadRequest, errorCodeVerificationCodeInvalid, "verification code is invalid")
	case errors.Is(err, authapplication.ErrVerificationCodeExpired):
		writeErrorResponse(c, http.StatusBadRequest, errorCodeVerificationCodeExpired, "verification code has expired")
	case errors.Is(err, authapplication.ErrVerificationAttemptsExceeded):
		writeErrorResponse(c, http.StatusTooManyRequests, errorCodeVerificationAttemptsExceeded, "verification attempts exceeded")
	case errors.Is(err, authapplication.ErrContactAlreadyRegistered):
		writeErrorResponse(c, http.StatusConflict, errorCodeContactAlreadyRegistered, "contact is already registered")
	default:
		writeInternalErrorResponse(c, h.logger, "auth_register_failed", err)
	}
	return true
}

func isInvalidRegistrationInput(err error) bool {
	return errors.Is(err, authapplication.ErrInvalidRegistrationRequest) ||
		errors.Is(err, userdomain.ErrInvalidDisplayName) ||
		errors.Is(err, userdomain.ErrPasswordTooShort) ||
		errors.Is(err, userdomain.ErrPasswordTooLong) ||
		errors.Is(err, userdomain.ErrPasswordInvalidCharacter) ||
		errors.Is(err, userdomain.ErrPasswordMissingUppercase) ||
		errors.Is(err, userdomain.ErrPasswordMissingLowercase) ||
		errors.Is(err, userdomain.ErrPasswordMissingDigit)
}
