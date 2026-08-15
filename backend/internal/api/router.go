// api负责定义和组织项目的HTTP接口
package api

import (
	"log/slog"
	"net/http"

	"github.com/gin-gonic/gin"
)

// NewRouter 创建并返回配置完成的 Gin 路由。
// *gin.Engine 表示返回的是Gin路由对象的指针
func NewRouter(logger *slog.Logger) *gin.Engine {
	// New 只创建空路由。中间件由项目显式选择，避免继续使用 Gin 默认文本日志。
	router := gin.New()

	// 中间件按照注册顺序进入、反向退出：先建立 request_id，访问日志才能
	// 在 Recovery 和 Handler 完成后记录最终状态码与同一个请求编号。
	router.Use(
		RequestIDMiddleware(),
		AccessLogMiddleware(logger),
		gin.Recovery(),
	)

	// GET 表示注册一个只接受 HTTP GET 请求的接口。
	// "/health" 是接口路径。
	// func(c *gin.Context) 是处理请求的匿名函数。
	router.GET("/health", func(c *gin.Context) {
		// JSON 把数据转换成 JSON，并以 200 状态码返回。
		// gin.H 是 map[string]any 的简写，适合构造简单 JSON。
		c.JSON(http.StatusOK, gin.H{
			"status": "ok",
		})
	})

	// 把配置完成的路由交给调用者。
	return router
}
