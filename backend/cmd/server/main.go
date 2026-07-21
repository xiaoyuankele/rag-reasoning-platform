// Package main 提供 Go 后端服务的程序入口。
package main

import (
	"context"
	"fmt"
	"log"

	"rag-reasoning-platform/backend/internal/api"
	documentapplication "rag-reasoning-platform/backend/internal/application/document"
	"rag-reasoning-platform/backend/internal/config"
	"rag-reasoning-platform/backend/internal/infrastructure/database"
	"rag-reasoning-platform/backend/internal/infrastructure/filestorage"
	"rag-reasoning-platform/backend/internal/infrastructure/postgres"
)

// main 调用 run，并统一处理应用程序最终返回的错误。
func main() {
	if err := run(); err != nil {
		log.Fatalf("application stopped: %v", err)
	}
}

// run 按顺序完成读取配置、连接数据库和启动 HTTP 服务。
func run() error {
	// Background 创建应用程序的根 context。
	// 后续数据库超时 context 都以它为父级。
	ctx := context.Background()

	appConfig, err := config.Load()
	if err != nil {
		return fmt.Errorf(
			"load application configuration: %w",
			err,
		)
	}

	databaseConfig, err := config.LoadDatabase()
	if err != nil {
		return fmt.Errorf(
			"load database configuration: %w",
			err,
		)
	}

	storageConfig, err := config.LoadStorage()
	if err != nil {
		return fmt.Errorf(
			"load storage configuration: %w",
			err,
		)
	}

	// ConnectionString 包含密码，只传给数据库层，不写入日志。
	databasePool, err := database.Open(
		ctx,
		databaseConfig.ConnectionString(),
	)
	if err != nil {
		return fmt.Errorf(
			"open database: %w",
			err,
		)
	}

	// run 返回前关闭连接池。
	defer databasePool.Close()

	// Repository 负责 PostgreSQL 数据访问。
	documentRepository := postgres.NewDocumentRepository(databasePool)
	localFileStorage, err := filestorage.NewLocalStorage(storageConfig.RootDir, storageConfig.MaxFileSizeBytes)
	if err != nil {
		return fmt.Errorf(
			"create local file storage: %w",
			err,
		)
	}
	// Service 负责文档查询用例和业务参数校验。
	documentService := documentapplication.NewService(documentRepository)
	documentUploadService := documentapplication.NewUploadService(documentRepository, localFileStorage)
	documentListService := documentapplication.NewListService(documentRepository)

	// Handler 负责把 HTTP 请求转换成应用服务调用。
	documentHandler := api.NewDocumentHandler(documentService)
	documentUploadHandler := api.NewDocumentUploadHandler(documentUploadService, storageConfig.MaxFileSizeBytes)
	documentListHandler := api.NewDocumentListHandler(documentListService)

	router := api.NewRouter()
	documentHandler.RegisterRoutes(router)
	documentUploadHandler.RegisterRoutes(router)
	documentListHandler.RegisterRoutes(router)

	if err := router.Run(appConfig.ServerAddress()); err != nil {
		return fmt.Errorf(
			"run HTTP server: %w",
			err,
		)
	}

	return nil
}
