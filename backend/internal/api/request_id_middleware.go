package api

import (
	"crypto/rand"
	"encoding/hex"
	"fmt"
	"regexp"
	"strings"
	"sync/atomic"
	"time"

	"github.com/gin-gonic/gin"

	"rag-reasoning-platform/backend/internal/observability"
)

const (
	// RequestIDHeader 是调用方传入和后端返回请求 ID 时使用的 HTTP 头。
	RequestIDHeader = "X-Request-ID"

	maximumRequestIDLength = 128
)

var (
	requestIDPattern = regexp.MustCompile(`^[A-Za-z0-9._-]+$`)
	fallbackSequence atomic.Uint64
)

// RequestIDMiddleware 为每个 HTTP 请求建立稳定的请求 ID。
//
// 合法的调用方 ID 会被沿用，方便前端、反向代理和后端日志共同追踪一次请求；
// 缺失或非法值会被替换，避免空值、控制字符或超长内容进入响应头和日志。
func RequestIDMiddleware() gin.HandlerFunc {
	return func(c *gin.Context) {
		requestID := strings.TrimSpace(c.GetHeader(RequestIDHeader))
		if !isValidRequestID(requestID) {
			requestID = generateRequestID()
		}

		c.Header(RequestIDHeader, requestID)
		c.Request = c.Request.WithContext(
			observability.WithRequestID(c.Request.Context(), requestID),
		)

		c.Next()
	}
}

func isValidRequestID(requestID string) bool {
	return requestID != "" &&
		len(requestID) <= maximumRequestIDLength &&
		requestIDPattern.MatchString(requestID)
}

func generateRequestID() string {
	var randomBytes [16]byte
	if _, err := rand.Read(randomBytes[:]); err == nil {
		return hex.EncodeToString(randomBytes[:])
	}

	// 请求 ID 不是安全令牌。极少数系统随机源不可用时，时间戳加进程内序号
	// 仍能提供足够的排障唯一性，并保证请求不会因为日志辅助能力而失败。
	return fmt.Sprintf(
		"fallback-%d-%d",
		time.Now().UnixNano(),
		fallbackSequence.Add(1),
	)
}
