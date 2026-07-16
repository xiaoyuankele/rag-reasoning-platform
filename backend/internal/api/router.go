// api负责定义和组织项目的HTTP接口
package api

import (
	"github.com/gin-gonic/gin"
	"net/http"
)

// NewRouter 创建并返回配置完成的Gin路由
// *gin.Engine 表示返回的是Gin路由对象的指针
func NewRouter() *gin.Engine {
	// Default 创建 Gin 路由，并自动启用请求日志和异常恢复中间件。
	router := gin.Default()

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
