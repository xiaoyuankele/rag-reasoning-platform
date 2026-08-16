package api

import (
	"context"
	"errors"
	"log/slog"
	"math"
	"net"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/gin-gonic/gin"

	verificationapplication "rag-reasoning-platform/backend/internal/application/verification"
	authdomain "rag-reasoning-platform/backend/internal/domain/auth"
	"rag-reasoning-platform/backend/internal/observability"
)

// verificationRequestService 是 Handler 申请验证码时需要的最小 Application 能力。
// 生产环境注入 *verification.Service，Handler 测试可以注入 Fake。
type verificationRequestService interface {
	RequestCode(
		ctx context.Context,
		input verificationapplication.RequestInput,
	) (verificationapplication.RequestOutput, error)
}

// verificationRequestLimiter 是 Handler 对单实例请求限流的最小需求。
// 接口由使用方 API 层声明；Infrastructure 的滑动窗口限流器会自动满足它。
type verificationRequestLimiter interface {
	Allow(clientKey string, now time.Time) (retryAt time.Time, allowed bool)
}

// VerificationHandler 负责申请验证码接口的 HTTP 边界处理。
type VerificationHandler struct {
	service verificationRequestService
	limiter verificationRequestLimiter
	logger  *slog.Logger
}

// NewVerificationHandler 创建验证码 Handler，并注入应用服务、限流器和日志器。
func NewVerificationHandler(
	service verificationRequestService,
	limiter verificationRequestLimiter,
	logger *slog.Logger,
) *VerificationHandler {
	if service == nil {
		panic("NewVerificationHandler requires a non-nil service")
	}
	if limiter == nil {
		panic("NewVerificationHandler requires a non-nil limiter")
	}
	if logger == nil {
		panic("NewVerificationHandler requires a non-nil logger")
	}

	return &VerificationHandler{
		service: service,
		limiter: limiter,
		logger:  logger,
	}
}

// RegisterRoutes 注册不需要 Session 的验证码申请路由。
func (h *VerificationHandler) RegisterRoutes(router *gin.Engine) {
	router.POST("/auth/verification-codes", h.RequestCode)
}

// verificationCodeRequest 是浏览器提交的验证码申请 JSON 契约。
type verificationCodeRequest struct {
	Channel     authdomain.VerificationChannel `json:"channel"`
	Destination string                         `json:"destination"`
	Purpose     authdomain.VerificationPurpose `json:"purpose"`
}

// verificationCodeResponse 只暴露后续注册需要的挑战元数据。
// 明文验证码和 code_hash 都不得进入 HTTP 响应。
type verificationCodeResponse struct {
	VerificationID int64     `json:"verification_id"`
	ExpiresAt      time.Time `json:"expires_at"`
	ResendAfter    time.Time `json:"resend_after"`
}

// RequestCode 处理 POST /auth/verification-codes。
func (h *VerificationHandler) RequestCode(c *gin.Context) {
	now := time.Now().UTC()
	clientKey := verificationClientKey(c.Request)
	if retryAt, allowed := h.limiter.Allow(clientKey, now); !allowed {
		writeVerificationRetryAfter(c, now, retryAt)
		writeErrorResponse(
			c,
			http.StatusTooManyRequests,
			errorCodeVerificationRequestThrottled,
			"verification requests are temporarily limited",
		)
		return
	}

	var request verificationCodeRequest
	// ShouldBindJSON 只负责把 HTTP JSON 转成 DTO；邮箱、手机号和用途是否
	// 合法仍由 Application 调用 Domain 规则统一判断。
	if err := c.ShouldBindJSON(&request); err != nil {
		writeErrorResponse(
			c,
			http.StatusBadRequest,
			errorCodeInvalidVerificationRequest,
			"request body must be valid JSON",
		)
		return
	}

	result, err := h.service.RequestCode(
		c.Request.Context(),
		verificationapplication.RequestInput{
			Channel:     request.Channel,
			Destination: request.Destination,
			Purpose:     request.Purpose,
		},
	)
	if errors.Is(err, authdomain.ErrInvalidVerificationChannel) ||
		errors.Is(err, authdomain.ErrInvalidVerificationDestination) ||
		errors.Is(err, authdomain.ErrInvalidVerificationPurpose) {
		writeErrorResponse(
			c,
			http.StatusBadRequest,
			errorCodeInvalidVerificationRequest,
			"verification request is invalid",
		)
		return
	}

	var cooldownError *verificationapplication.CooldownError
	if errors.As(err, &cooldownError) {
		writeVerificationRetryAfter(c, now, cooldownError.RetryAt)
		writeErrorResponse(
			c,
			http.StatusTooManyRequests,
			errorCodeVerificationRequestThrottled,
			"verification requests are temporarily limited",
		)
		return
	}

	if errors.Is(err, verificationapplication.ErrVerificationDeliveryUnavailable) {
		// 记录稳定诊断类别和底层错误，但不记录联系方式、验证码或摘要。
		requestID, _ := observability.RequestIDFromContext(
			c.Request.Context(),
		)
		h.logger.LogAttrs(
			c.Request.Context(),
			slog.LevelError,
			"Verification delivery failed",
			slog.String("event", "verification_delivery_failed"),
			slog.String("request_id", requestID),
			slog.Any("error", err),
		)
		_ = c.Error(err)
		writeErrorResponse(
			c,
			http.StatusServiceUnavailable,
			errorCodeVerificationChannelUnavailable,
			"verification channel is temporarily unavailable",
		)
		return
	}

	if err != nil {
		writeInternalErrorResponse(
			c,
			h.logger,
			"verification_request_failed",
			err,
		)
		return
	}

	c.JSON(
		http.StatusAccepted,
		verificationCodeResponse{
			VerificationID: result.ChallengeID,
			// PostgreSQL 可能按连接时区恢复 timestamptz，而 Application 新建
			// 时间通常是 UTC。HTTP 边界统一为 UTC，避免同一 DTO 混用偏移格式。
			ExpiresAt:   result.ExpiresAt.UTC(),
			ResendAfter: result.ResendAfter.UTC(),
		},
	)
}

// verificationClientKey 返回单实例限流使用的远端 IP。
// 当前不信任 X-Forwarded-For，避免客户端伪造代理头绕过限流；部署可信反向
// 代理时，需要先在统一网络边界显式配置可信代理。
func verificationClientKey(request *http.Request) string {
	remoteAddress := strings.TrimSpace(request.RemoteAddr)
	host, _, err := net.SplitHostPort(remoteAddress)
	if err == nil && host != "" {
		return host
	}
	if remoteAddress == "" {
		return "unknown"
	}
	return remoteAddress
}

func writeVerificationRetryAfter(
	c *gin.Context,
	now time.Time,
	retryAt time.Time,
) {
	seconds := int64(math.Ceil(retryAt.Sub(now).Seconds()))
	if seconds < 1 {
		seconds = 1
	}
	c.Header("Retry-After", strconv.FormatInt(seconds, 10))
}
