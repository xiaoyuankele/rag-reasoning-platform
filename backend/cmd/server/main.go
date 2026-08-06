// Package main 提供 Go 后端服务的程序入口。
package main

import (
	"context"
	"errors"
	"fmt"
	"log"
	"net/http"
	"os"
	"os/signal"
	"syscall"
	"time"

	"rag-reasoning-platform/backend/internal/api"
	documentapplication "rag-reasoning-platform/backend/internal/application/document"
	"rag-reasoning-platform/backend/internal/config"
	"rag-reasoning-platform/backend/internal/infrastructure/database"
	"rag-reasoning-platform/backend/internal/infrastructure/filestorage"
	"rag-reasoning-platform/backend/internal/infrastructure/postgres"
	"rag-reasoning-platform/backend/migrations"
)

const (
	// httpReadHeaderTimeout 限制客户端发送 HTTP 请求头的时间，
	// 避免连接长时间占用却迟迟不发送完整请求头。
	httpReadHeaderTimeout = 5 * time.Second

	// httpShutdownTimeout 是 HTTP 服务等待已有请求结束的最长时间。
	httpShutdownTimeout = 10 * time.Second
)

// main 调用 run，并统一处理应用程序最终返回的错误。
func main() {
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	if err := run(ctx); err != nil {
		log.Fatalf("application stopped: %v", err)
	}
}

// run 是应用生命周期的编排入口：按顺序完成配置加载、基础设施初始化、
// 启动恢复、后台 Worker 启动、HTTP 服务运行与优雅关闭。
func run(ctx context.Context) error {
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

	workerConfig, err := config.LoadWorker()
	if err != nil {
		return fmt.Errorf("load worker configuration: %w", err)
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

	if err := database.Migrate(
		ctx,
		databasePool,
		migrations.Files,
	); err != nil {
		return fmt.Errorf(
			"migrate database: %w",
			err,
		)
	}

	// Repository 负责 PostgreSQL 数据访问。
	documentRepository := postgres.NewDocumentRepository(databasePool)
	processingJobRepository := postgres.NewProcessingJobRepository(databasePool)
	chunkRepository := postgres.NewChunkRepository(databasePool)

	// Worker 启动前，先恢复上一次异常退出遗留的 processing 任务。
	// main 只负责决定调用时机；恢复规则位于 Application，SQL 位于 Repository。
	interruptedJobRecoveryService :=
		documentapplication.NewInterruptedJobRecoveryService(
			processingJobRepository,
		)
	recoveredJobCount, err := interruptedJobRecoveryService.Recover(ctx)
	if err != nil {
		return fmt.Errorf(
			"recover interrupted processing jobs during startup: %w",
			err,
		)
	}

	if recoveredJobCount > 0 {
		log.Printf(
			"recovered %d interrupted processing jobs",
			recoveredJobCount,
		)
	}

	localFileStorage, err := filestorage.NewLocalStorage(storageConfig.RootDir, storageConfig.MaxFileSizeBytes)
	if err != nil {
		return fmt.Errorf("create local file storage: %w", err)
	}
	// 必须先确认 LocalStorage 创建成功，再把它交给 TextProcessor。

	textProcessor := documentapplication.NewTextProcessor(localFileStorage)
	processorDispatcher, err := documentapplication.NewProcessorDispatcher(
		map[string]documentapplication.DocumentProcessor{
			"text/markdown": textProcessor,
			"text/plain":    textProcessor,
		},
	)
	if err != nil {
		return fmt.Errorf(
			"create document processor dispatcher: %w",
			err,
		)
	}

	// Service 负责文档查询用例和业务参数校验。
	documentService := documentapplication.NewService(documentRepository)
	documentUploadService := documentapplication.NewUploadService(documentRepository, localFileStorage)
	documentListService := documentapplication.NewListService(documentRepository)
	documentDeleteService := documentapplication.NewDeleteService(documentRepository, localFileStorage)
	documentProcessingService := documentapplication.NewQueueProcessingService(
		documentRepository,
		processingJobRepository,
	)
	processingJobService := documentapplication.NewProcessingJobService(
		processingJobRepository,
	)
	worker := documentapplication.NewWorker(
		processingJobRepository,
		documentRepository,
		processorDispatcher,
		chunkRepository,
		workerConfig.ProcessingTimeout,
	)
	workerErrorReporter := func(err error) {
		log.Printf("worker error: %v", err)
	}
	workerLoop, err := documentapplication.NewWorkerLoop(worker, workerConfig.PollInterval, workerErrorReporter)
	if err != nil {
		return fmt.Errorf("create worker loop: %w", err)
	}

	// workerContext 专门控制后台 Worker 的生命周期。
	// 调用 cancelWorker 后，workerContext.Done() 会被关闭，
	// WorkerLoop 就能结束等待并退出循环。
	workerContext, cancelWorker := context.WithCancel(ctx)

	// workerDone 不传递具体数据，只表示“Worker goroutine 已经退出”。
	workerDone := make(chan struct{})

	// go 关键字让 WorkerLoop 在新的 goroutine 中运行。
	// 当前主 goroutine 不会被 WorkerLoop 的循环阻塞，可以继续启动 HTTP 服务。
	go func() {
		// 无论 WorkerLoop 如何正常返回，退出当前 goroutine 前都关闭 workerDone。
		defer close(workerDone)

		workerLoop.Run(workerContext)
	}()

	// defer 在 run 返回前执行。
	// 先通知 Worker 停止，再等待它完全退出。
	defer func() {
		cancelWorker()
		<-workerDone
	}()

	// Handler 负责把 HTTP 请求转换成应用服务调用。
	documentHandler := api.NewDocumentHandler(documentService)
	documentUploadHandler := api.NewDocumentUploadHandler(documentUploadService, storageConfig.MaxFileSizeBytes)
	documentListHandler := api.NewDocumentListHandler(documentListService)
	documentDeleteHandler := api.NewDocumentDeleteHandler(documentDeleteService)
	documentProcessingHandler := api.NewDocumentProcessingHandler(
		documentProcessingService,
	)
	processingJobHandler := api.NewProcessingJobHandler(
		processingJobService,
	)

	router := api.NewRouter()
	documentHandler.RegisterRoutes(router)
	documentUploadHandler.RegisterRoutes(router)
	documentListHandler.RegisterRoutes(router)
	documentDeleteHandler.RegisterRoutes(router)
	documentProcessingHandler.RegisterRoutes(router)
	processingJobHandler.RegisterRoutes(router)

	// Gin Engine 实现了 http.Handler，所以可以交给标准库 http.Server。
	// 不再使用 router.Run，是因为我们需要持有 Server，才能在收到退出信号时
	// 调用 Shutdown 完成优雅关闭。
	httpServer := &http.Server{
		Addr:              appConfig.ServerAddress(),
		Handler:           router,
		ReadHeaderTimeout: httpReadHeaderTimeout,
	}

	// ListenAndServe 会一直阻塞，因此放到独立 goroutine 中。
	// channel 使用容量 1，保证即使主 goroutine 正在处理取消信号，
	// HTTP goroutine 也能写入结果并退出，不会卡在发送操作上。
	httpServerErrors := make(chan error, 1)
	go func() {
		err := httpServer.ListenAndServe()
		if errors.Is(err, http.ErrServerClosed) {
			err = nil
		}
		httpServerErrors <- err
	}()

	select {
	case err := <-httpServerErrors:
		// 没有退出信号，但 HTTP 服务自己停止，通常意味着端口占用等启动错误。
		if err != nil {
			return fmt.Errorf("run HTTP server: %w", err)
		}
		return nil

	case <-ctx.Done():
		// 退出信号到达后，不再接收新连接，并给已有请求最多 10 秒完成。
		shutdownContext, cancelShutdown := context.WithTimeout(
			context.Background(),
			httpShutdownTimeout,
		)
		defer cancelShutdown()

		if err := httpServer.Shutdown(shutdownContext); err != nil {
			// 优雅关闭超时后强制关闭连接，避免 run 永远无法返回。
			closeErr := httpServer.Close()
			return errors.Join(
				fmt.Errorf("shut down HTTP server: %w", err),
				wrapHTTPServerCloseError(closeErr),
			)
		}

		// Shutdown 已使 ListenAndServe 返回；读取 channel 确认 HTTP goroutine
		// 已经真正退出，再让 run 继续执行 Worker 和数据库清理 defer。
		if err := <-httpServerErrors; err != nil {
			return fmt.Errorf("run HTTP server: %w", err)
		}
		return nil
	}
}

// wrapHTTPServerCloseError 只包装真实的强制关闭错误。
// Close 成功时返回 nil，errors.Join 会自动忽略 nil。
func wrapHTTPServerCloseError(err error) error {
	if err == nil {
		return nil
	}
	return fmt.Errorf("force close HTTP server: %w", err)
}
