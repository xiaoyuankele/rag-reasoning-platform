// Package main 提供 Go 后端服务的程序入口。
package main

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"os"
	"os/signal"
	"sync"
	"syscall"
	"time"

	"rag-reasoning-platform/backend/internal/api"
	answerapplication "rag-reasoning-platform/backend/internal/application/answer"
	authapplication "rag-reasoning-platform/backend/internal/application/auth"
	documentapplication "rag-reasoning-platform/backend/internal/application/document"
	embeddingapplication "rag-reasoning-platform/backend/internal/application/embedding"
	verificationapplication "rag-reasoning-platform/backend/internal/application/verification"
	"rag-reasoning-platform/backend/internal/config"
	documentdomain "rag-reasoning-platform/backend/internal/domain/document"
	embeddingdomain "rag-reasoning-platform/backend/internal/domain/embedding"
	"rag-reasoning-platform/backend/internal/infrastructure/database"
	"rag-reasoning-platform/backend/internal/infrastructure/filestorage"
	passwordinfrastructure "rag-reasoning-platform/backend/internal/infrastructure/password"
	"rag-reasoning-platform/backend/internal/infrastructure/postgres"
	"rag-reasoning-platform/backend/internal/infrastructure/pythonprocessor"
	"rag-reasoning-platform/backend/internal/infrastructure/ratelimit"
	sessioninfrastructure "rag-reasoning-platform/backend/internal/infrastructure/session"
	verificationinfrastructure "rag-reasoning-platform/backend/internal/infrastructure/verification"
	"rag-reasoning-platform/backend/internal/observability"
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
	// 日志配置必须在正式 Logger 创建前读取。配置本身无效时使用最小 JSON
	// Logger 输出到 stderr，避免启动失败完全不可见。
	loggingConfig, err := config.LoadLogging()
	if err != nil {
		bootstrapLogger := slog.New(slog.NewJSONHandler(os.Stderr, nil))
		bootstrapLogger.Error(
			"Application stopped",
			"event", "application_stopped",
			"error", fmt.Errorf("load logging configuration: %w", err),
		)
		os.Exit(1)
	}

	// Logger 在组合根创建后显式注入，不让业务层依赖全局日志变量。
	logger := newApplicationLogger(os.Stdout, loggingConfig)

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	if err := run(ctx, logger); err != nil {
		logger.Error(
			"Application stopped",
			"event", "application_stopped",
			"error", err,
		)
		os.Exit(1)
	}

	// run 返回前已经完成 HTTP、Worker 和数据库资源清理。
	logger.Info(
		"Application stopped",
		"event", "application_stopped",
		"outcome", "graceful",
	)
}

// run 是应用生命周期的编排入口：按顺序完成配置加载、基础设施初始化、
// 启动恢复、后台 Worker 启动、HTTP 服务运行与优雅关闭。
func run(ctx context.Context, logger *slog.Logger) error {
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

	runtimePathsConfig, err := config.LoadRuntimePaths()
	if err != nil {
		return fmt.Errorf(
			"load runtime paths configuration: %w",
			err,
		)
	}

	storageConfig, err := config.LoadStorage(runtimePathsConfig.AppRoot)
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

	pythonConfig, err := config.LoadPython(runtimePathsConfig.AppRoot)
	if err != nil {
		return fmt.Errorf("load Python processor configuration: %w", err)
	}

	embeddingConfig, err := config.LoadEmbedding()
	if err != nil {
		return fmt.Errorf("load embedding configuration: %w", err)
	}

	generationConfig, err := config.LoadGeneration()
	if err != nil {
		return fmt.Errorf("load generation configuration: %w", err)
	}

	verificationConfig, err := config.LoadVerification()
	if err != nil {
		return fmt.Errorf("load verification configuration: %w", err)
	}

	authConfig, err := config.LoadAuth()
	if err != nil {
		return fmt.Errorf("load auth configuration: %w", err)
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

	// 第 9 号迁移首次创建 vector 扩展后，刷新迁移前已经建立的连接。
	// Repository 随后可以直接传递 pgvector.Vector，而不需要拼接向量字符串。
	if err := database.RefreshVectorTypes(ctx, databasePool); err != nil {
		return fmt.Errorf(
			"refresh pgvector database types: %w",
			err,
		)
	}

	// Repository 负责 PostgreSQL 数据访问。
	documentRepository := postgres.NewDocumentRepository(databasePool)
	scopedDocumentRepository := postgres.NewScopedDocumentRepository(databasePool)
	processingJobRepository :=
		postgres.NewProcessingJobRepositoryWithSchedulingPolicy(
			databasePool,
			documentdomain.ProcessingJobSchedulingPolicy{
				MaxInFlightPerOwner:         workerConfig.OwnerInFlightLimit,
				MaxBorrowedInFlightPerOwner: workerConfig.OwnerBorrowedLimit,
				StarvationThreshold:         workerConfig.StarvationThreshold,
			},
		)
	scopedProcessingJobRepository := postgres.NewScopedProcessingJobRepository(
		databasePool,
		documentdomain.ProcessingJobAdmissionLimits{
			MaxActiveJobsPerOwner: workerConfig.ActiveJobsPerUserLimit,
			MaxActiveJobsGlobal:   workerConfig.ActiveJobsGlobalLimit,
		},
	)
	embeddingJobRepository := postgres.NewEmbeddingJobRepository(databasePool)
	scopedEmbeddingJobRepository := postgres.NewScopedEmbeddingJobRepository(
		databasePool,
		embeddingdomain.JobAdmissionLimits{
			MaxActiveJobsPerOwner: embeddingConfig.ActiveJobsPerUserLimit,
			MaxActiveJobsGlobal:   embeddingConfig.ActiveJobsGlobalLimit,
		},
	)
	chunkRepository := postgres.NewChunkRepository(databasePool)
	scopedChunkRepository := postgres.NewScopedChunkRepository(databasePool)
	verificationChallengeRepository :=
		postgres.NewVerificationChallengeRepository(databasePool)
	authRegistrationRepository :=
		postgres.NewAuthRegistrationRepository(databasePool)
	authPasswordResetRepository :=
		postgres.NewAuthPasswordResetRepository(databasePool)
	authSessionRepository := postgres.NewAuthSessionRepository(databasePool)

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
		logger.Info(
			"Recovered interrupted processing jobs",
			"event", "processing_jobs_recovered",
			"job_count", recoveredJobCount,
		)
	}

	// Embedding Worker 在 shutdown 时保留 processing，避免把正常停机伪装成业务失败。
	// 单实例服务重新启动后，必须先把这些遗留任务放回 queued，随后才能启动 Worker。
	embeddingRecoveryService, err :=
		embeddingapplication.NewInterruptedJobRecoveryService(
			embeddingJobRepository,
		)
	if err != nil {
		return fmt.Errorf("create embedding recovery service: %w", err)
	}
	embeddingRecoveredJobCount, err := embeddingRecoveryService.Recover(ctx)
	if err != nil {
		return fmt.Errorf(
			"recover interrupted embedding jobs during startup: %w",
			err,
		)
	}
	if embeddingRecoveredJobCount > 0 {
		logger.Info(
			"Requeued interrupted embedding jobs",
			"event", "embedding_jobs_requeued",
			"job_count", embeddingRecoveredJobCount,
		)
	}

	localFileStorage, err := filestorage.NewLocalStorage(storageConfig.RootDir, storageConfig.MaxFileSizeBytes)
	if err != nil {
		return fmt.Errorf("create local file storage: %w", err)
	}
	// 必须先确认 LocalStorage 创建成功，再组装 Python 文档处理器。
	// oneshot 和 pool 都满足同一个 Application 端口，模式差异只留在组合根
	// 与 Infrastructure；业务服务和 Worker 不感知子进程是否被复用。
	var pythonDocumentProcessor documentapplication.DocumentProcessor
	closePythonDocumentProcessor := func() error { return nil }
	switch pythonConfig.ProcessMode {
	case config.PythonProcessModeOneShot:
		processor, err := pythonprocessor.NewProcessor(
			localFileStorage,
			pythonConfig.Executable,
			pythonConfig.SourceRoot,
			pythonConfig.PDFMaxFileSizeBytes,
			pythonConfig.PDFMaxPages,
		)
		if err != nil {
			return fmt.Errorf("create oneshot Python document processor: %w", err)
		}
		pythonDocumentProcessor = processor

	case config.PythonProcessModePool:
		// 第一版不允许 Go Worker 多于 Python 槽位，否则多领出的任务会显示
		// processing 却只是在进程池门口等待，扭曲排队和处理耗时指标。
		if pythonConfig.ProcessPoolSize < workerConfig.DocumentConcurrency {
			return fmt.Errorf(
				"Python process pool size %d must be at least document worker concurrency %d",
				pythonConfig.ProcessPoolSize,
				workerConfig.DocumentConcurrency,
			)
		}
		processPool, err := pythonprocessor.NewProcessPool(
			localFileStorage,
			pythonConfig.Executable,
			pythonConfig.SourceRoot,
			pythonConfig.PDFMaxFileSizeBytes,
			pythonConfig.PDFMaxPages,
			pythonConfig.ProcessPoolSize,
			pythonConfig.ProcessMaxDocuments,
		)
		if err != nil {
			return fmt.Errorf("create pooled Python document processor: %w", err)
		}
		pythonDocumentProcessor = processPool
		closePythonDocumentProcessor = processPool.Close
	}
	logger.Info(
		"Configured Python document processor",
		"event", "python_document_processor_configured",
		"mode", pythonConfig.ProcessMode,
		"pool_size", pythonConfig.ProcessPoolSize,
		"max_documents_per_process", pythonConfig.ProcessMaxDocuments,
	)
	textProcessor := documentapplication.NewTextProcessor(localFileStorage)
	processorDispatcher, err := documentapplication.NewProcessorDispatcher(
		map[string]documentapplication.DocumentProcessor{
			"text/markdown":   textProcessor,
			"text/plain":      textProcessor,
			"application/pdf": pythonDocumentProcessor,
		},
	)
	if err != nil {
		return fmt.Errorf(
			"create document processor dispatcher: %w",
			err,
		)
	}

	// Service 负责文档查询用例和业务参数校验。
	documentService := documentapplication.NewService(scopedDocumentRepository)
	baseDocumentUploadService := documentapplication.NewUploadService(
		scopedDocumentRepository,
		localFileStorage,
	)
	documentUploadService, err := documentapplication.NewConcurrentUploadService(
		baseDocumentUploadService,
		observability.NewUploadAdmissionLogger(logger),
		storageConfig.UploadMaxConcurrencyPerUser,
		storageConfig.UploadMaxConcurrencyGlobal,
		storageConfig.UploadQueueWaitTimeout,
	)
	if err != nil {
		return fmt.Errorf("create upload concurrency service: %w", err)
	}
	logger.Info(
		"Upload concurrency configured",
		"event", "upload_concurrency_configured",
		"owner_max_concurrency",
		storageConfig.UploadMaxConcurrencyPerUser,
		"global_max_concurrency",
		storageConfig.UploadMaxConcurrencyGlobal,
		"queue_wait_timeout_ms",
		storageConfig.UploadQueueWaitTimeout.Milliseconds(),
	)
	documentPreflightService := documentapplication.NewPreflightService(
		scopedDocumentRepository,
		storageConfig.MaxFileSizeBytes,
	)
	documentListService := documentapplication.NewListService(scopedDocumentRepository)
	documentChunkListService := documentapplication.NewChunkListService(
		scopedDocumentRepository,
		scopedChunkRepository,
	)
	documentSearchService := documentapplication.NewSearchService(scopedChunkRepository)
	documentDeleteService := documentapplication.NewDeleteService(scopedDocumentRepository, localFileStorage)
	documentProcessingService := documentapplication.NewQueueProcessingService(
		scopedDocumentRepository,
		scopedProcessingJobRepository,
	)
	processingJobService := documentapplication.NewProcessingJobService(
		scopedProcessingJobRepository,
	)
	embeddingQueueService := embeddingapplication.NewQueueService(
		scopedEmbeddingJobRepository,
		embeddingConfig.ModelName,
		embeddingConfig.Dimensions,
	)
	embeddingJobQueryService := embeddingapplication.NewJobQueryService(
		scopedEmbeddingJobRepository,
	)
	embeddingJobCancelService := embeddingapplication.NewCancelService(
		scopedEmbeddingJobRepository,
	)
	documentWorker := documentapplication.NewWorker(
		processingJobRepository,
		documentRepository,
		processorDispatcher,
		chunkRepository,
		observability.NewProcessingJobLogger(logger),
		workerConfig.ProcessingTimeout,
	)
	documentWorkerErrorReporter := func(err error) {
		logger.Error(
			"Document worker iteration failed",
			"event", "document_worker_error",
			"error", err,
		)
	}
	documentWorkerLoop, err := documentapplication.NewWorkerLoop(
		documentWorker,
		workerConfig.PollInterval,
		documentWorkerErrorReporter,
	)
	if err != nil {
		return fmt.Errorf("create document worker loop: %w", err)
	}
	documentWorkerPool, err := documentapplication.NewWorkerPool(
		documentWorkerLoop,
		workerConfig.DocumentConcurrency,
	)
	if err != nil {
		return fmt.Errorf("create document worker pool: %w", err)
	}
	logger.Info(
		"Document worker pool configured",
		"event", "document_worker_pool_configured",
		"concurrency", workerConfig.DocumentConcurrency,
		"owner_in_flight_limit", workerConfig.OwnerInFlightLimit,
		"owner_borrowed_limit", workerConfig.OwnerBorrowedLimit,
		"starvation_threshold_ms",
		workerConfig.StarvationThreshold.Milliseconds(),
	)

	// Worker、公开语义检索和问答内部检索最终都调用同一个远程 Embedding
	// 提供方。组合根创建一个原始客户端、一个共享 Gate，再按后台/在线两种
	// 等待策略包装成两个 Embedder；这样两条执行链竞争的是同一组槽位。
	var workerEmbedder embeddingdomain.Embedder
	var onlineEmbedder embeddingdomain.Embedder
	if embeddingConfig.WorkerEnabled ||
		embeddingConfig.SemanticSearchEnabled ||
		generationConfig.Enabled {
		rawEmbedder, err := newEmbeddingClient(embeddingConfig)
		if err != nil {
			return err
		}

		providerGate, err := embeddingapplication.NewEmbeddingProviderGate(
			embeddingConfig.ProviderMaxConcurrency,
			embeddingConfig.WorkerProviderConcurrency,
			embeddingConfig.OnlineProviderConcurrency,
		)
		if err != nil {
			return fmt.Errorf("create embedding provider gate: %w", err)
		}
		providerAdmissionObserver :=
			observability.NewEmbeddingProviderAdmissionLogger(logger)

		workerEmbedder, err = embeddingapplication.NewGatedEmbedder(
			rawEmbedder,
			providerGate,
			providerAdmissionObserver,
			embeddingapplication.EmbeddingProviderCallOriginWorker,
			0,
		)
		if err != nil {
			return fmt.Errorf("create worker embedding provider gate: %w", err)
		}
		onlineEmbedder, err = embeddingapplication.NewGatedEmbedder(
			rawEmbedder,
			providerGate,
			providerAdmissionObserver,
			embeddingapplication.EmbeddingProviderCallOriginOnline,
			embeddingConfig.OnlineQueueWaitTimeout,
		)
		if err != nil {
			return fmt.Errorf("create online embedding provider gate: %w", err)
		}

		logger.Info(
			"Embedding provider concurrency configured",
			"event", "embedding_provider_concurrency_configured",
			"max_concurrency", embeddingConfig.ProviderMaxConcurrency,
			"worker_max_concurrency",
			embeddingConfig.WorkerProviderConcurrency,
			"online_max_concurrency",
			embeddingConfig.OnlineProviderConcurrency,
			"online_queue_wait_timeout_ms",
			embeddingConfig.OnlineQueueWaitTimeout.Milliseconds(),
		)
	}

	// 语义检索服务有两种消费者：
	// 1. SEMANTIC_SEARCH_ENABLED 控制的公开 POST /semantic-search；
	// 2. ANSWER_ENABLED 控制的 AnswerService 内部证据检索。
	// 因此“创建应用能力”和“是否暴露独立 HTTP 路由”必须分开判断。
	var semanticSearchService *embeddingapplication.SemanticSearchService
	if embeddingConfig.SemanticSearchEnabled || generationConfig.Enabled {
		semanticSearchService, err =
			embeddingapplication.NewSemanticSearchService(
				onlineEmbedder,
				scopedChunkRepository,
				embeddingConfig.ModelName,
				embeddingConfig.Dimensions,
			)
		if err != nil {
			return fmt.Errorf("create semantic search service: %w", err)
		}
	}

	// 第一版问答使用 DashScope 的 OpenAI 兼容生成接口。Generator 只在显式
	// 启用 ANSWER_ENABLED 时创建，避免基础服务启动后意外产生生成费用。
	var answerService *answerapplication.ConcurrentService
	if generationConfig.Enabled {
		generator, err := newGenerationClient(generationConfig)
		if err != nil {
			return err
		}

		baseAnswerService, err := answerapplication.NewService(
			semanticSearchService,
			generator,
			observability.NewGenerationCallLogger(logger),
			generationConfig.ModelName,
			generationConfig.MaxOutputTokens,
			generationConfig.Temperature,
		)
		if err != nil {
			return fmt.Errorf("create answer service: %w", err)
		}

		answerService, err = answerapplication.NewConcurrentService(
			baseAnswerService,
			observability.NewAnswerAdmissionLogger(logger),
			generationConfig.MaxConcurrency,
			generationConfig.QueueWaitTimeout,
		)
		if err != nil {
			return fmt.Errorf("create answer concurrency service: %w", err)
		}

		logger.Info(
			"Answer concurrency configured",
			"event", "answer_concurrency_configured",
			"max_concurrency", generationConfig.MaxConcurrency,
			"queue_wait_timeout_ms",
			generationConfig.QueueWaitTimeout.Milliseconds(),
		)
	}

	// Application 只依赖验证码端口；这里是组合根，负责选择具体实现。
	verificationCodeHasher, err :=
		verificationinfrastructure.NewHMACCodeHasher(
			[]byte(verificationConfig.HMACSecret),
		)
	if err != nil {
		return fmt.Errorf("create verification code hasher: %w", err)
	}

	var verificationSender verificationapplication.Sender
	switch verificationConfig.Sender {
	case config.VerificationSenderFake:
		verificationSender = verificationinfrastructure.NewFakeSender()
	case config.VerificationSenderMailpit:
		verificationSender, err = verificationinfrastructure.NewSMTPSender(
			verificationinfrastructure.SMTPOptions{
				Host:        verificationConfig.SMTPHost,
				Port:        verificationConfig.SMTPPort,
				FromAddress: verificationConfig.SMTPFromAddress,
				FromName:    verificationConfig.SMTPFromName,
				Timeout:     verificationConfig.SMTPTimeout,
			},
		)
		if err != nil {
			return fmt.Errorf("create Mailpit SMTP verification sender: %w", err)
		}
	default:
		return fmt.Errorf(
			"unsupported verification sender %q",
			verificationConfig.Sender,
		)
	}

	verificationService := verificationapplication.NewService(
		verificationChallengeRepository,
		verificationinfrastructure.NewRandomCodeGenerator(),
		verificationCodeHasher,
		verificationSender,
		time.Now,
		verificationapplication.DefaultChallengeTTL,
		verificationapplication.DefaultResendCooldown,
	)
	verificationRequestLimiter, err := ratelimit.NewSlidingWindowLimiter(
		verificationConfig.RateLimitWindow,
		verificationConfig.PerClientLimit,
		verificationConfig.GlobalLimit,
	)
	if err != nil {
		return fmt.Errorf("create verification request limiter: %w", err)
	}

	passwordHasher, err := passwordinfrastructure.NewArgon2idHasher(
		passwordinfrastructure.DefaultParameters(),
	)
	if err != nil {
		return fmt.Errorf("create password hasher: %w", err)
	}
	// 同一个 TokenGenerator 同时负责创建和校验 Session Token。
	// 这里显式组装后，Application 不需要知道具体的随机数与摘要算法。
	sessionTokenManager := sessioninfrastructure.NewTokenGenerator()
	authRegisterService, err := authapplication.NewRegisterService(
		authRegistrationRepository,
		passwordHasher,
		verificationCodeHasher,
		sessionTokenManager,
		time.Now,
		authConfig.SessionTTL,
	)
	if err != nil {
		return fmt.Errorf("create auth register service: %w", err)
	}
	authPasswordResetService, err := authapplication.NewPasswordResetService(
		authPasswordResetRepository,
		passwordHasher,
		verificationCodeHasher,
		time.Now,
	)
	if err != nil {
		return fmt.Errorf("create auth password reset service: %w", err)
	}
	authLoginService, err := authapplication.NewLoginService(
		authSessionRepository,
		passwordHasher,
		sessionTokenManager,
		time.Now,
		authConfig.SessionTTL,
	)
	if err != nil {
		return fmt.Errorf("create auth login service: %w", err)
	}
	authSessionService, err := authapplication.NewSessionService(
		authSessionRepository,
		sessionTokenManager,
		time.Now,
	)
	if err != nil {
		return fmt.Errorf("create auth session service: %w", err)
	}
	authRequestLimiter, err := ratelimit.NewSlidingWindowLimiter(
		authConfig.RateLimitWindow,
		authConfig.PerClientLimit,
		authConfig.GlobalLimit,
	)
	if err != nil {
		return fmt.Errorf("create auth request limiter: %w", err)
	}

	// 默认不启动远程向量 Worker，避免开发者未明确授权时产生后台 API 调用。
	var embeddingWorkerPool *documentapplication.WorkerPool
	if embeddingConfig.WorkerEnabled {

		retryPolicy, err := embeddingapplication.NewRetryPolicy(
			embeddingConfig.MaxAttempts,
			embeddingConfig.RetryBaseDelay,
			embeddingConfig.RetryMaxDelay,
		)
		if err != nil {
			return fmt.Errorf("create embedding retry policy: %w", err)
		}

		embeddingWorker, err := embeddingapplication.NewWorker(
			embeddingJobRepository,
			chunkRepository,
			workerEmbedder,
			observability.NewEmbeddingJobLogger(logger),
			embeddingConfig.BatchSize,
			embeddingConfig.ProcessingTimeout,
			retryPolicy,
		)
		if err != nil {
			return fmt.Errorf("create embedding worker: %w", err)
		}

		embeddingWorkerLoop, err := documentapplication.NewWorkerLoop(
			embeddingWorker,
			embeddingConfig.PollInterval,
			func(err error) {
				logger.Error(
					"Embedding worker iteration failed",
					"event", "embedding_worker_error",
					"error", err,
				)
			},
		)
		if err != nil {
			return fmt.Errorf("create embedding worker loop: %w", err)
		}

		embeddingWorkerPool, err = documentapplication.NewWorkerPool(
			embeddingWorkerLoop,
			embeddingConfig.WorkerConcurrency,
		)
		if err != nil {
			return fmt.Errorf("create embedding worker pool: %w", err)
		}

		logger.Info(
			"Embedding worker pool configured",
			"event", "embedding_worker_pool_configured",
			"concurrency", embeddingConfig.WorkerConcurrency,
		)
	}

	// workerContext 专门控制后台 Worker 的生命周期。
	// 调用 cancelWorker 后，workerContext.Done() 会被关闭，
	// WorkerLoop 就能结束等待并退出循环。
	workerContext, cancelWorker := context.WithCancel(ctx)

	// WaitGroup 记录仍未退出的 Worker goroutine 数量。
	var workerGroup sync.WaitGroup
	startBackgroundWorker := func(runWorker func(context.Context)) {
		workerGroup.Add(1)
		go func() {
			defer workerGroup.Done()
			runWorker(workerContext)
		}()
	}

	startBackgroundWorker(documentWorkerPool.Run)
	if embeddingWorkerPool != nil {
		startBackgroundWorker(embeddingWorkerPool.Run)
	}

	// defer 在 run 返回前执行。
	// 先通知 Worker 停止，再等待它完全退出。
	defer func() {
		cancelWorker()
		workerGroup.Wait()
		if err := closePythonDocumentProcessor(); err != nil {
			logger.Error(
				"Close Python document processor",
				"event", "python_document_processor_close_failed",
				"error", err,
			)
		}
	}()

	// Handler 负责把 HTTP 请求转换成应用服务调用。
	documentHandler := api.NewDocumentHandler(documentService, logger)
	documentUploadHandler := api.NewDocumentUploadHandler(documentUploadService, storageConfig.MaxFileSizeBytes)
	documentPreflightHandler := api.NewDocumentPreflightHandler(
		documentPreflightService,
		logger,
	)
	documentListHandler := api.NewDocumentListHandler(documentListService)
	documentChunkHandler := api.NewDocumentChunkHandler(
		documentChunkListService,
	)
	documentSearchHandler := api.NewDocumentSearchHandler(documentSearchService)
	documentDeleteHandler := api.NewDocumentDeleteHandler(documentDeleteService)
	documentProcessingHandler := api.NewDocumentProcessingHandler(
		documentProcessingService,
	)
	processingJobHandler := api.NewProcessingJobHandler(
		processingJobService,
		logger,
	)
	documentEmbeddingHandler := api.NewDocumentEmbeddingHandler(
		embeddingQueueService,
	)
	documentEmbeddingBatchHandler := api.NewDocumentEmbeddingBatchHandler(
		embeddingQueueService,
		logger,
	)
	embeddingJobHandler := api.NewEmbeddingJobHandler(
		embeddingJobQueryService,
	)
	embeddingJobCancelHandler := api.NewEmbeddingJobCancelHandler(
		embeddingJobCancelService,
		logger,
	)
	var semanticSearchHandler *api.SemanticSearchHandler
	if embeddingConfig.SemanticSearchEnabled {
		semanticSearchHandler = api.NewSemanticSearchHandler(
			semanticSearchService,
		)
	}
	var answerHandler *api.AnswerHandler
	if answerService != nil {
		answerHandler = api.NewAnswerHandler(answerService)
	}
	verificationHandler := api.NewVerificationHandler(
		verificationService,
		verificationRequestLimiter,
		logger,
	)
	authRegisterHandler := api.NewAuthRegisterHandler(
		authRegisterService,
		authRequestLimiter,
		logger,
		authConfig.CookieSecure,
	)
	authLoginHandler := api.NewAuthLoginHandler(
		authLoginService,
		authRequestLimiter,
		logger,
		authConfig.CookieSecure,
	)
	authPasswordResetHandler := api.NewAuthPasswordResetHandler(
		authPasswordResetService,
		authRequestLimiter,
		logger,
		authConfig.CookieSecure,
	)
	authMiddleware := api.NewAuthMiddleware(authSessionService, logger)
	currentUserHandler := api.NewCurrentUserHandler()
	authLogoutHandler := api.NewAuthLogoutHandler(
		authSessionService,
		logger,
		authConfig.CookieSecure,
	)

	router := api.NewRouter(logger)
	verificationHandler.RegisterRoutes(router)
	authRegisterHandler.RegisterRoutes(router)
	authLoginHandler.RegisterRoutes(router)
	authPasswordResetHandler.RegisterRoutes(router)
	authLogoutHandler.RegisterRoutes(router)

	// 只有已经在 Application 与 SQL 两层强制 OwnerScope 的接口，
	// 才允许进入受保护路由组。其余业务接口将在后续隔离批次逐步迁入。
	protectedRoutes := router.Group("")
	protectedRoutes.Use(authMiddleware.Require)
	documentHandler.RegisterRoutes(protectedRoutes)
	documentUploadHandler.RegisterRoutes(protectedRoutes)
	documentPreflightHandler.RegisterRoutes(protectedRoutes)
	documentListHandler.RegisterRoutes(protectedRoutes)
	documentDeleteHandler.RegisterRoutes(protectedRoutes)
	documentChunkHandler.RegisterRoutes(protectedRoutes)
	documentProcessingHandler.RegisterRoutes(protectedRoutes)
	processingJobHandler.RegisterRoutes(protectedRoutes)
	documentEmbeddingHandler.RegisterRoutes(protectedRoutes)
	documentEmbeddingBatchHandler.RegisterRoutes(protectedRoutes)
	embeddingJobHandler.RegisterRoutes(protectedRoutes)
	embeddingJobCancelHandler.RegisterRoutes(protectedRoutes)
	documentSearchHandler.RegisterRoutes(protectedRoutes)
	if semanticSearchHandler != nil {
		semanticSearchHandler.RegisterRoutes(protectedRoutes)
	}
	if answerHandler != nil {
		answerHandler.RegisterRoutes(protectedRoutes)
	}

	users := protectedRoutes.Group("/users")
	currentUserHandler.RegisterRoutes(users)

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
	logger.Info(
		"Application started",
		"event", "application_started",
		"address", appConfig.ServerAddress(),
	)

	select {
	case err := <-httpServerErrors:
		// 没有退出信号，但 HTTP 服务自己停止，通常意味着端口占用等启动错误。
		if err != nil {
			return fmt.Errorf("run HTTP server: %w", err)
		}
		return nil

	case <-ctx.Done():
		logger.Info(
			"Application shutdown started",
			"event", "application_shutdown_started",
			"cause", ctx.Err(),
		)

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
