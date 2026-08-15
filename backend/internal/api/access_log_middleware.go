package api

import (
	"log/slog"
	"net/http"
	"time"

	"github.com/gin-gonic/gin"

	"rag-reasoning-platform/backend/internal/observability"
)

// AccessLogMiddleware 在请求处理结束后写入一条结构化访问日志。
//
// 日志记录路由模板而不是完整查询字符串，既能聚合相同接口，又能避免把问题正文、
// 筛选条件或其他用户输入直接复制进访问日志。
func AccessLogMiddleware(logger *slog.Logger) gin.HandlerFunc {
	if logger == nil {
		panic("AccessLogMiddleware requires a non-nil logger")
	}

	return func(c *gin.Context) {
		startedAt := time.Now()

		c.Next()

		requestID, _ := observability.RequestIDFromContext(
			c.Request.Context(),
		)
		route := c.FullPath()
		if route == "" {
			route = "unmatched"
		}

		statusCode := c.Writer.Status()
		logger.LogAttrs(
			c.Request.Context(),
			accessLogLevel(statusCode),
			"HTTP request completed",
			slog.String("event", "http_request_completed"),
			slog.String("request_id", requestID),
			slog.String("method", c.Request.Method),
			slog.String("route", route),
			slog.Int("status_code", statusCode),
			slog.Int64("duration_ms", time.Since(startedAt).Milliseconds()),
			slog.Int("response_bytes", c.Writer.Size()),
			slog.Int("error_count", len(c.Errors)),
		)
	}
}

func accessLogLevel(statusCode int) slog.Level {
	switch {
	case statusCode >= http.StatusInternalServerError:
		return slog.LevelError
	case statusCode >= http.StatusBadRequest:
		return slog.LevelWarn
	default:
		return slog.LevelInfo
	}
}
