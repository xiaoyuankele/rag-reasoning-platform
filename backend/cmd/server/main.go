package main

import (
	// "github.com/gin-gonic/gin"
	// "net/http"
	"rag-reasoning-platform/backend/internal/api"
)

func main() {

	// 创建已经注册好所有 HTTP 接口的 Gin 路由。
	router := api.NewRouter()
	// router := gin.Default()

	// 在本机 8080 端口启动 HTTP 服务。
	// Run 会持续运行并阻塞当前终端，直到程序被停止或发生错误。
	err := router.Run(":8080")
	// router.GET("/health", func(c *gin.Context) {
	// 	c.JSON(http.StatusOK, gin.H{
	// 		"status": "ok",
	// 	})
	// })

	// err := router.Run(":8080")

	// Go 通常通过判断 err 是否为 nil 来检查操作是否失败。
	// nil 表示没有错误，非 nil 表示启动服务失败。
	if err != nil {
		// panic 会终止程序并打印错误信息。
		// 当前阶段先使用它处理无法启动服务器的严重错误。
		panic(err)
	}
}
