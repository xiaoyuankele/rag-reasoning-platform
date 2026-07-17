// Package main 提供 Go 后端服务的程序入口。
package main

import (
	"log"

	"rag-reasoning-platform/backend/internal/api"
	"rag-reasoning-platform/backend/internal/config"
)

// main 读取配置、创建路由并启动 HTTP 服务。
func main() {
	// 使用 appConfig 作为变量名，避免遮挡导入的 config 包。
	appConfig, err := config.Load()
	if err != nil {
		// 配置无效时输出原因并立即终止，避免服务带着错误配置运行。
		log.Fatalf("load configuration: %v", err)
	}

	// 创建已经注册好所有 HTTP 接口的 Gin 路由。
	router := api.NewRouter()

	// 根据配置启动服务，例如监听 ":8080" 或 ":9090"。
	err = router.Run(appConfig.ServerAddress())
	if err != nil {
		// 端口被占用等错误会导致服务启动失败。
		log.Fatalf("run server: %v", err)
	}
}
