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
	infrastructure "rag-reasoning-platform/backend/internal/infrastructure"
	"rag-reasoning-platform/backend/internal/infrastructure/database"
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
	rolePlan, err := newApplicationRolePlan(appConfig.Role)
	if err != nil {
		return err
	}

	databaseConfig, err := config.LoadDatabase()
	if err != nil {
		return fmt.Errorf(
			"load database configuration: %w",
			err,
		)
	}

	var runtimePathsConfig config.RuntimePathsConfig
	var storageConfig config.StorageConfig
	if rolePlan.needsStorage() {
		runtimePathsConfig, err = config.LoadRuntimePaths()
		if err != nil {
			return fmt.Errorf(
				"load runtime paths configuration: %w",
				err,
			)
		}

		storageConfig, err = config.LoadStorage(runtimePathsConfig.AppRoot)
		if err != nil {
			return fmt.Errorf(
				"load storage configuration: %w",
				err,
			)
		}
	}

	var workerConfig config.WorkerConfig
	if rolePlan.needsDocumentConfig() {
		workerConfig, err = config.LoadWorker()
		if err != nil {
			return fmt.Errorf("load worker configuration: %w", err)
		}
	}

	var pythonConfig config.PythonConfig
	if rolePlan.runDocumentWorker {
		pythonConfig, err = config.LoadPython(runtimePathsConfig.AppRoot)
		if err != nil {
			return fmt.Errorf("load Python processor configuration: %w", err)
		}
	}

	var embeddingConfig config.EmbeddingConfig
	if rolePlan.needsEmbeddingConfig() {
		embeddingConfig, err = config.LoadEmbedding()
		if err != nil {
			return fmt.Errorf("load embedding configuration: %w", err)
		}
	}

	var generationConfig config.GenerationConfig
	if rolePlan.needsGenerationConfig() {
		generationConfig, err = config.LoadGeneration()
		if err != nil {
			return fmt.Errorf("load generation configuration: %w", err)
		}
	}

	var answerJobsConfig config.AnswerJobsConfig
	if rolePlan.needsAnswerJobsConfig() {
		answerJobsConfig, err = config.LoadAnswerJobs()
		if err != nil {
			return fmt.Errorf("load answer jobs configuration: %w", err)
		}
		if answerJobsConfig.Enabled && !generationConfig.Enabled {
			return errors.New(
				"ANSWER_JOBS_ENABLED requires ANSWER_ENABLED=true",
			)
		}
	}

	if err := validateApplicationRoleFeatures(
		appConfig.Role,
		embeddingConfig.WorkerEnabled,
		generationConfig.Enabled,
		answerJobsConfig.Enabled,
	); err != nil {
		return err
	}

	var verificationConfig config.VerificationConfig
	var authConfig config.AuthConfig
	if rolePlan.serveHTTP {
		verificationConfig, err = config.LoadVerification()
		if err != nil {
			return fmt.Errorf("load verification configuration: %w", err)
		}

		authConfig, err = config.LoadAuth()
		if err != nil {
			return fmt.Errorf("load auth configuration: %w", err)
		}
	}

	var cacheConfig config.CacheConfig
	if rolePlan.needsCacheConfig() {
		cacheConfig, err = config.LoadCache()
		if err != nil {
			return fmt.Errorf("load RAG cache configuration: %w", err)
		}
	}

	var capacityConfig config.CapacityCoordinationConfig
	if rolePlan.needsCapacityCoordinationConfig() {
		capacityConfig, err = config.LoadCapacityCoordination()
		if err != nil {
			return fmt.Errorf("load capacity coordination configuration: %w", err)
		}
	}

	// Redis 只保存可丢弃的加速副本。即使启动时 Ping 失败，服务仍会启动，
	// 后续每次缓存读写失败都由 Application 自动回源远程模型。
	var ragCache *infrastructure.RedisCache
	var cacheDigester *infrastructure.HMACSHA256Digester
	if cacheConfig.Enabled {
		ragCache, err = infrastructure.NewRedisCache(
			infrastructure.RedisCacheOptions{
				Address:          cacheConfig.RedisAddress,
				Password:         cacheConfig.RedisPassword,
				Database:         cacheConfig.RedisDatabase,
				OperationTimeout: cacheConfig.OperationTimeout,
			},
		)
		if err != nil {
			return fmt.Errorf("create Redis RAG cache: %w", err)
		}
		defer func() {
			if closeErr := ragCache.Close(); closeErr != nil {
				logger.Warn(
					"Close Redis cache",
					"event", "redis_cache_close_failed",
					"error", closeErr,
				)
			}
		}()

		cacheDigester, err = infrastructure.NewHMACSHA256Digester(
			[]byte(cacheConfig.HMACSecret),
		)
		if err != nil {
			return fmt.Errorf("create cache question digester: %w", err)
		}
		if pingErr := ragCache.Ping(ctx); pingErr != nil {
			logger.Warn(
				"Redis cache unavailable during startup; requests will use provider fallback",
				"event", "redis_cache_startup_unavailable",
				"error", pingErr,
			)
		} else {
			logger.Info(
				"Redis RAG cache configured",
				"event", "redis_cache_configured",
				"namespace", cacheConfig.Namespace,
				"query_vector_ttl_ms", cacheConfig.QueryVectorTTL.Milliseconds(),
				"answer_result_ttl_ms", cacheConfig.AnswerResultTTL.Milliseconds(),
			)
		}
	}

	needsWorkerEmbedder := rolePlan.runEmbeddingWorker &&
		embeddingConfig.WorkerEnabled
	needsOnlineEmbedder := (rolePlan.serveHTTP || rolePlan.runAnswerWorker) &&
		(embeddingConfig.SemanticSearchEnabled || generationConfig.Enabled)
	needsRemoteCapacity := needsWorkerEmbedder ||
		needsOnlineEmbedder ||
		((rolePlan.serveHTTP || rolePlan.runAnswerWorker) && generationConfig.Enabled)

	// 容量协调不是可丢弃缓存：显式启用后必须在启动时可用，否则不同进程会
	// 各自放行完整并发上限。运行期故障会拒绝新的远程调用，并由同步 503 或
	// 异步任务重试承接，不会无保护地绕过 Redis。
	var capacityStore *infrastructure.RedisCapacityStore
	if capacityConfig.Enabled && needsRemoteCapacity {
		minimumLeaseTTL := embeddingConfig.HTTPTimeout
		if generationConfig.Enabled {
			minimumLeaseTTL += generationConfig.HTTPTimeout + 30*time.Second
		}
		if capacityConfig.LeaseTTL <= minimumLeaseTTL {
			return fmt.Errorf(
				"CAPACITY_LEASE_TTL must exceed the longest coordinated call budget %s",
				minimumLeaseTTL,
			)
		}

		capacityStore, err = infrastructure.NewRedisCapacityStore(
			infrastructure.RedisCapacityOptions{
				Address:          capacityConfig.RedisAddress,
				Password:         capacityConfig.RedisPassword,
				Database:         capacityConfig.RedisDatabase,
				OperationTimeout: capacityConfig.OperationTimeout,
			},
		)
		if err != nil {
			return fmt.Errorf("create Redis capacity coordinator: %w", err)
		}
		defer func() {
			if closeErr := capacityStore.Close(); closeErr != nil {
				logger.Warn(
					"Close Redis capacity coordinator",
					"event", "redis_capacity_close_failed",
					"error", closeErr,
				)
			}
		}()
		if pingErr := capacityStore.Ping(ctx); pingErr != nil {
			return fmt.Errorf(
				"ping required Redis capacity coordinator: %w",
				pingErr,
			)
		}
		logger.Info(
			"Redis provider capacity coordination configured",
			"event", "redis_capacity_coordination_configured",
			"namespace", capacityConfig.Namespace,
			"lease_ttl_ms", capacityConfig.LeaseTTL.Milliseconds(),
			"retry_interval_ms", capacityConfig.RetryInterval.Milliseconds(),
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

	// Repository 只在当前角色真正需要时创建。虽然 Repository 本身很轻，
	// 但显式边界可以防止后续误把无关依赖继续接入某个独立角色。
	var documentRepository *postgres.DocumentRepository
	var processingJobRepository *postgres.ProcessingJobRepository
	var chunkRepository *postgres.ChunkRepository
	if rolePlan.runDocumentWorker {
		documentRepository = postgres.NewDocumentRepository(databasePool)
		processingJobRepository =
			postgres.NewProcessingJobRepositoryWithPolicies(
				databasePool,
				documentdomain.ProcessingJobSchedulingPolicy{
					MaxInFlightPerOwner:         workerConfig.OwnerInFlightLimit,
					MaxBorrowedInFlightPerOwner: workerConfig.OwnerBorrowedLimit,
					StarvationThreshold:         workerConfig.StarvationThreshold,
				},
				documentdomain.ProcessingJobLeasePolicy{
					WorkerID:      workerConfig.DocumentWorkerID,
					LeaseDuration: workerConfig.JobLeaseDuration,
				},
			)
		chunkRepository = postgres.NewChunkRepository(databasePool)
	}

	var scopedDocumentRepository *postgres.ScopedDocumentRepository
	var scopedProcessingJobRepository *postgres.ScopedProcessingJobRepository
	var scopedEmbeddingJobRepository *postgres.ScopedEmbeddingJobRepository
	if rolePlan.serveHTTP {
		scopedDocumentRepository = postgres.NewScopedDocumentRepository(databasePool)
		scopedProcessingJobRepository = postgres.NewScopedProcessingJobRepository(
			databasePool,
			documentdomain.ProcessingJobAdmissionLimits{
				MaxActiveJobsPerOwner: workerConfig.ActiveJobsPerUserLimit,
				MaxActiveJobsGlobal:   workerConfig.ActiveJobsGlobalLimit,
			},
		)
		scopedEmbeddingJobRepository = postgres.NewScopedEmbeddingJobRepository(
			databasePool,
			embeddingdomain.JobAdmissionLimits{
				MaxActiveJobsPerOwner: embeddingConfig.ActiveJobsPerUserLimit,
				MaxActiveJobsGlobal:   embeddingConfig.ActiveJobsGlobalLimit,
			},
		)
	}

	var embeddingJobRepository *postgres.EmbeddingJobRepository
	if rolePlan.runEmbeddingWorker {
		embeddingJobRepository =
			postgres.NewEmbeddingJobRepositoryWithPolicies(
				databasePool,
				embeddingdomain.JobSchedulingPolicy{
					MaxInFlightPerOwner:         embeddingConfig.OwnerInFlightLimit,
					MaxBorrowedInFlightPerOwner: embeddingConfig.OwnerBorrowedLimit,
					StarvationThreshold:         embeddingConfig.StarvationThreshold,
				},
				embeddingdomain.JobLeasePolicy{
					WorkerID:      embeddingConfig.WorkerID,
					LeaseDuration: embeddingConfig.JobLeaseDuration,
				},
			)
		if chunkRepository == nil {
			chunkRepository = postgres.NewChunkRepository(databasePool)
		}
	}

	var scopedChunkRepository *postgres.ScopedChunkRepository
	var corpusRevisionRepository *postgres.CorpusRevisionRepository
	if rolePlan.serveHTTP || rolePlan.runAnswerWorker {
		scopedChunkRepository = postgres.NewScopedChunkRepository(databasePool)
	}
	if cacheConfig.Enabled &&
		(rolePlan.serveHTTP || rolePlan.runAnswerWorker) {
		corpusRevisionRepository = postgres.NewCorpusRevisionRepository(databasePool)
	}

	var verificationChallengeRepository *postgres.VerificationChallengeRepository
	var authRegistrationRepository *postgres.AuthRegistrationRepository
	var authPasswordResetRepository *postgres.AuthPasswordResetRepository
	var authSessionRepository *postgres.AuthSessionRepository
	if rolePlan.serveHTTP {
		verificationChallengeRepository =
			postgres.NewVerificationChallengeRepository(databasePool)
		authRegistrationRepository =
			postgres.NewAuthRegistrationRepository(databasePool)
		authPasswordResetRepository =
			postgres.NewAuthPasswordResetRepository(databasePool)
		authSessionRepository = postgres.NewAuthSessionRepository(databasePool)
	}

	var answerJobRepository *postgres.AnswerJobRepository
	if answerJobsConfig.Enabled &&
		(rolePlan.serveHTTP || rolePlan.runAnswerWorker) {
		answerJobRepository = postgres.NewAnswerJobRepositoryWithPolicies(
			databasePool,
			answerapplication.JobAdmissionLimits{
				MaxQueuedJobsPerOwner: answerJobsConfig.MaxQueuedPerUser,
				MaxQueuedJobsGlobal:   answerJobsConfig.MaxQueuedGlobal,
			},
			answerapplication.JobSchedulingPolicy{
				MaxInFlightPerOwner:         answerJobsConfig.OwnerInFlightLimit,
				MaxBorrowedInFlightPerOwner: answerJobsConfig.OwnerBorrowedInFlightLimit,
				StarvationThreshold:         answerJobsConfig.StarvationThreshold,
			},
			answerapplication.JobLeasePolicy{
				WorkerID:      answerJobsConfig.WorkerID,
				LeaseDuration: answerJobsConfig.JobLeaseDuration,
			},
		)
	}

	// Worker 启动前先重排已经过期的 processing 租约。仍在其他实例心跳的
	// 任务不会被恢复；规则位于 Application，SQL 位于 Repository。
	if rolePlan.runDocumentWorker {
		expiredJobRecoveryService :=
			documentapplication.NewExpiredJobRecoveryService(
				processingJobRepository,
			)
		recoveredJobCount, err := expiredJobRecoveryService.Recover(ctx)
		if err != nil {
			return fmt.Errorf(
				"recover expired processing jobs during startup: %w",
				err,
			)
		}

		if recoveredJobCount > 0 {
			logger.Info(
				"Requeued expired processing jobs",
				"event", "processing_jobs_recovered",
				"job_count", recoveredJobCount,
			)
		}
	}

	// Embedding Worker 只恢复真正过期的 processing 租约。其他实例仍在
	// 心跳的任务不会被重排，因此不同进程可以安全共享同一队列。
	if rolePlan.runEmbeddingWorker {
		embeddingRecoveryService, err :=
			embeddingapplication.NewExpiredJobRecoveryService(
				embeddingJobRepository,
			)
		if err != nil {
			return fmt.Errorf("create embedding recovery service: %w", err)
		}
		embeddingRecoveredJobCount, err := embeddingRecoveryService.Recover(ctx)
		if err != nil {
			return fmt.Errorf(
				"recover expired embedding jobs during startup: %w",
				err,
			)
		}
		if embeddingRecoveredJobCount > 0 {
			logger.Info(
				"Requeued expired embedding jobs",
				"event", "embedding_jobs_requeued",
				"job_count", embeddingRecoveredJobCount,
			)
		}
	}

	var fileStorage runtimeFileStorage
	if rolePlan.needsStorage() {
		fileStorage, err = newRuntimeFileStorage(storageConfig)
		if err != nil {
			return err
		}
		logger.Info(
			"Configured document file storage",
			"event", "document_file_storage_configured",
			"driver", storageConfig.Driver,
		)
	}

	// 必须先确认统一文件存储创建成功，再组装 Python 文档处理器。
	// oneshot 和 pool 都满足同一个 Application 端口，模式差异只留在组合根
	// 与 Infrastructure；业务服务和 Worker 不感知子进程是否被复用。
	closePythonDocumentProcessor := func() error { return nil }
	var processorDispatcher *documentapplication.ProcessorDispatcher
	if rolePlan.runDocumentWorker {
		var pythonDocumentProcessor documentapplication.DocumentProcessor
		switch pythonConfig.ProcessMode {
		case config.PythonProcessModeOneShot:
			processor, err := pythonprocessor.NewProcessor(
				fileStorage,
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
				fileStorage,
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
		textProcessor := documentapplication.NewTextProcessor(fileStorage)
		processorDispatcher, err = documentapplication.NewProcessorDispatcher(
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
	}

	var documentWorkerPool *documentapplication.WorkerPool
	if rolePlan.runDocumentWorker {
		documentWorker, err := documentapplication.NewWorkerWithHeartbeatInterval(
			processingJobRepository,
			documentRepository,
			processorDispatcher,
			chunkRepository,
			observability.NewProcessingJobLogger(logger),
			workerConfig.ProcessingTimeout,
			workerConfig.JobHeartbeatInterval,
		)
		if err != nil {
			return fmt.Errorf("create leased document worker: %w", err)
		}
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
		documentWorkerPool, err = documentapplication.NewWorkerPool(
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
			"worker_id", workerConfig.DocumentWorkerID,
			"lease_duration_ms", workerConfig.JobLeaseDuration.Milliseconds(),
			"heartbeat_interval_ms",
			workerConfig.JobHeartbeatInterval.Milliseconds(),
		)
	}

	// Worker、公开语义检索和问答内部检索最终都调用同一个远程 Embedding
	// 提供方。组合根创建一个原始客户端、一个共享 Gate，再按后台/在线两种
	// 等待策略包装成两个 Embedder；这样两条执行链竞争的是同一组槽位。
	var workerEmbedder embeddingdomain.Embedder
	var onlineEmbedder embeddingdomain.Embedder
	if needsWorkerEmbedder || needsOnlineEmbedder {
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

		if needsWorkerEmbedder {
			workerProvider := rawEmbedder
			if capacityStore != nil {
				workerProvider, err = embeddingapplication.NewDistributedGatedEmbedder(
					rawEmbedder,
					capacityStore,
					providerAdmissionObserver,
					embeddingapplication.DistributedEmbeddingProviderGateConfig{
						Namespace:              capacityConfig.Namespace,
						Provider:               string(embeddingConfig.Provider),
						Model:                  embeddingConfig.ModelName,
						Dimensions:             embeddingConfig.Dimensions,
						Origin:                 embeddingapplication.EmbeddingProviderCallOriginWorker,
						ProviderMaxConcurrency: embeddingConfig.ProviderMaxConcurrency,
						OriginMaxConcurrency:   embeddingConfig.WorkerProviderConcurrency,
						LeaseTTL:               capacityConfig.LeaseTTL,
						RetryInterval:          capacityConfig.RetryInterval,
						WaitTimeout:            0,
					},
				)
				if err != nil {
					return fmt.Errorf("create distributed worker embedding gate: %w", err)
				}
			}
			workerEmbedder, err = embeddingapplication.NewGatedEmbedder(
				workerProvider,
				providerGate,
				providerAdmissionObserver,
				embeddingapplication.EmbeddingProviderCallOriginWorker,
				0,
			)
			if err != nil {
				return fmt.Errorf("create worker embedding provider gate: %w", err)
			}
		}
		if needsOnlineEmbedder {
			onlineProvider := rawEmbedder
			if capacityStore != nil {
				onlineProvider, err = embeddingapplication.NewDistributedGatedEmbedder(
					rawEmbedder,
					capacityStore,
					providerAdmissionObserver,
					embeddingapplication.DistributedEmbeddingProviderGateConfig{
						Namespace:              capacityConfig.Namespace,
						Provider:               string(embeddingConfig.Provider),
						Model:                  embeddingConfig.ModelName,
						Dimensions:             embeddingConfig.Dimensions,
						Origin:                 embeddingapplication.EmbeddingProviderCallOriginOnline,
						ProviderMaxConcurrency: embeddingConfig.ProviderMaxConcurrency,
						OriginMaxConcurrency:   embeddingConfig.OnlineProviderConcurrency,
						LeaseTTL:               capacityConfig.LeaseTTL,
						RetryInterval:          capacityConfig.RetryInterval,
						WaitTimeout:            embeddingConfig.OnlineQueueWaitTimeout,
					},
				)
				if err != nil {
					return fmt.Errorf("create distributed online embedding gate: %w", err)
				}
			}
			onlineEmbedder, err = embeddingapplication.NewGatedEmbedder(
				onlineProvider,
				providerGate,
				providerAdmissionObserver,
				embeddingapplication.EmbeddingProviderCallOriginOnline,
				embeddingConfig.OnlineQueueWaitTimeout,
			)
			if err != nil {
				return fmt.Errorf("create online embedding provider gate: %w", err)
			}
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
	if (rolePlan.serveHTTP || rolePlan.runAnswerWorker) &&
		(embeddingConfig.SemanticSearchEnabled || generationConfig.Enabled) {
		if cacheConfig.Enabled {
			semanticSearchService, err =
				embeddingapplication.NewSemanticSearchServiceWithQueryCache(
					onlineEmbedder,
					scopedChunkRepository,
					embeddingConfig.ModelName,
					embeddingConfig.Dimensions,
					ragCache,
					cacheDigester,
					observability.NewQueryVectorCacheLogger(logger),
					embeddingapplication.QueryVectorCacheConfig{
						Namespace:   cacheConfig.Namespace,
						Provider:    string(embeddingConfig.Provider),
						TTL:         cacheConfig.QueryVectorTTL,
						LockTTL:     cacheConfig.QueryVectorLockTTL,
						WaitTimeout: cacheConfig.QueryVectorWaitTimeout,
					},
				)
		} else {
			semanticSearchService, err = embeddingapplication.NewSemanticSearchService(
				onlineEmbedder,
				scopedChunkRepository,
				embeddingConfig.ModelName,
				embeddingConfig.Dimensions,
			)
		}
		if err != nil {
			return fmt.Errorf("create semantic search service: %w", err)
		}
	}

	// 第一版问答使用 DashScope 的 OpenAI 兼容生成接口。Generator 只在显式
	// 启用 ANSWER_ENABLED 时创建，避免基础服务启动后意外产生生成费用。
	var answerService answerapplication.Answerer
	if (rolePlan.serveHTTP || rolePlan.runAnswerWorker) &&
		generationConfig.Enabled {
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

		answerAdmissionObserver := observability.NewAnswerAdmissionLogger(logger)
		var capacityAwareAnswerService answerapplication.Answerer = baseAnswerService
		if capacityStore != nil {
			capacityAwareAnswerService, err = answerapplication.NewDistributedService(
				baseAnswerService,
				capacityStore,
				answerAdmissionObserver,
				answerapplication.DistributedAnswerConfig{
					Namespace:              capacityConfig.Namespace,
					Provider:               "dashscope",
					Model:                  generationConfig.ModelName,
					MaxConcurrencyGlobal:   generationConfig.MaxConcurrency,
					MaxConcurrencyPerOwner: generationConfig.MaxConcurrencyPerUser,
					LeaseTTL:               capacityConfig.LeaseTTL,
					RetryInterval:          capacityConfig.RetryInterval,
					WaitTimeout:            generationConfig.QueueWaitTimeout,
				},
			)
			if err != nil {
				return fmt.Errorf("create distributed answer capacity service: %w", err)
			}
		}

		concurrentAnswerService, err := answerapplication.NewConcurrentService(
			capacityAwareAnswerService,
			answerAdmissionObserver,
			answerapplication.AnswerAdmissionLimits{
				MaxConcurrencyGlobal:   generationConfig.MaxConcurrency,
				MaxConcurrencyPerOwner: generationConfig.MaxConcurrencyPerUser,
				MaxWaitersGlobal:       generationConfig.MaxWaitersGlobal,
				MaxWaitersPerOwner:     generationConfig.MaxWaitersPerUser,
				WaitTimeout:            generationConfig.QueueWaitTimeout,
			},
		)
		if err != nil {
			return fmt.Errorf("create answer concurrency service: %w", err)
		}
		answerService = concurrentAnswerService

		// 缓存放在并发闸门外层：命中直接返回，未命中才占用生成槽位。
		if cacheConfig.Enabled {
			answerService, err = answerapplication.NewCachedService(
				concurrentAnswerService,
				corpusRevisionRepository,
				ragCache,
				cacheDigester,
				observability.NewAnswerCacheLogger(logger),
				answerapplication.AnswerCacheConfig{
					Namespace:           cacheConfig.Namespace,
					GenerationProvider:  "dashscope",
					GenerationModel:     generationConfig.ModelName,
					PromptVersion:       answerapplication.AnswerPromptVersion,
					RetrievalVersion:    answerapplication.AnswerRetrievalVersion,
					EmbeddingModel:      embeddingConfig.ModelName,
					EmbeddingDimensions: embeddingConfig.Dimensions,
					MaxOutputTokens:     generationConfig.MaxOutputTokens,
					Temperature:         generationConfig.Temperature,
					TTL:                 cacheConfig.AnswerResultTTL,
					LockTTL:             cacheConfig.AnswerResultLockTTL,
					WaitTimeout:         cacheConfig.AnswerResultWaitTimeout,
				},
			)
			if err != nil {
				return fmt.Errorf("create answer result cache service: %w", err)
			}
		}

		logger.Info(
			"Answer concurrency configured",
			"event", "answer_concurrency_configured",
			"max_concurrency_global", generationConfig.MaxConcurrency,
			"max_concurrency_per_user", generationConfig.MaxConcurrencyPerUser,
			"max_waiters_global", generationConfig.MaxWaitersGlobal,
			"max_waiters_per_user", generationConfig.MaxWaitersPerUser,
			"queue_wait_timeout_ms",
			generationConfig.QueueWaitTimeout.Milliseconds(),
		)
	}

	// 异步问答复用同一套 AnswerService，但把 HTTP 连接的等待转换为数据库任务。
	// Worker 并发仍会经过 ConcurrentService 的总闸门，因此同步和异步请求不会
	// 绕开同一组远程生成容量限制。
	var answerJobService *answerapplication.JobService
	var answerJobWorkerPool *documentapplication.WorkerPool
	var answerJobCleanupLoop *documentapplication.WorkerLoop
	if answerJobsConfig.Enabled && rolePlan.serveHTTP {
		answerJobService, err = answerapplication.NewJobService(
			answerJobRepository,
		)
		if err != nil {
			return fmt.Errorf("create answer job service: %w", err)
		}
	}

	if answerJobsConfig.Enabled && rolePlan.runAnswerWorker {
		answerJobRecoveryService, err :=
			answerapplication.NewExpiredJobRecoveryService(
				answerJobRepository,
			)
		if err != nil {
			return fmt.Errorf("create answer job recovery service: %w", err)
		}
		answerJobRecoveredCount, err := answerJobRecoveryService.Recover(ctx)
		if err != nil {
			return fmt.Errorf(
				"recover interrupted answer jobs during startup: %w",
				err,
			)
		}
		if answerJobRecoveredCount > 0 {
			logger.Info(
				"Requeued interrupted answer jobs",
				"event", "answer_jobs_requeued",
				"job_count", answerJobRecoveredCount,
			)
		}

		answerJobRetryPolicy, err := answerapplication.NewJobRetryPolicy(
			answerJobsConfig.MaxAttempts,
			answerJobsConfig.RetryBaseDelay,
			answerJobsConfig.RetryMaxDelay,
		)
		if err != nil {
			return fmt.Errorf("create answer job retry policy: %w", err)
		}
		answerJobLogger := observability.NewAnswerJobLogger(logger)
		answerJobWorker, err := answerapplication.NewJobWorkerWithHeartbeatInterval(
			answerJobRepository,
			answerService,
			answerJobLogger,
			answerJobsConfig.ProcessingTimeout,
			answerJobRetryPolicy,
			answerJobsConfig.JobHeartbeatInterval,
		)
		if err != nil {
			return fmt.Errorf("create answer job worker: %w", err)
		}
		answerJobWorkerLoop, err := documentapplication.NewWorkerLoop(
			answerJobWorker,
			answerJobsConfig.PollInterval,
			func(err error) {
				logger.Error(
					"Answer job worker iteration failed",
					"event", "answer_job_worker_error",
					"error", err,
				)
			},
		)
		if err != nil {
			return fmt.Errorf("create answer job worker loop: %w", err)
		}
		answerJobWorkerPool, err = documentapplication.NewWorkerPool(
			answerJobWorkerLoop,
			answerJobsConfig.WorkerConcurrency,
		)
		if err != nil {
			return fmt.Errorf("create answer job worker pool: %w", err)
		}
		answerJobRetentionService, err :=
			answerapplication.NewJobRetentionService(
				answerJobRepository,
				answerJobLogger,
				answerJobsConfig.Retention,
				answerJobsConfig.CleanupBatchSize,
			)
		if err != nil {
			return fmt.Errorf("create answer job retention service: %w", err)
		}
		answerJobCleanupLoop, err = documentapplication.NewWorkerLoop(
			answerJobRetentionService,
			answerJobsConfig.CleanupInterval,
			func(err error) {
				logger.Error(
					"Answer job retention iteration failed",
					"event", "answer_job_retention_error",
					"error", err,
				)
			},
		)
		if err != nil {
			return fmt.Errorf("create answer job retention loop: %w", err)
		}

		logger.Info(
			"Answer job worker pool configured",
			"event", "answer_job_worker_pool_configured",
			"concurrency", answerJobsConfig.WorkerConcurrency,
			"max_queued_per_user", answerJobsConfig.MaxQueuedPerUser,
			"max_queued_global", answerJobsConfig.MaxQueuedGlobal,
			"owner_in_flight_limit", answerJobsConfig.OwnerInFlightLimit,
			"owner_borrowed_limit",
			answerJobsConfig.OwnerBorrowedInFlightLimit,
			"starvation_threshold_ms",
			answerJobsConfig.StarvationThreshold.Milliseconds(),
			"retention_ms", answerJobsConfig.Retention.Milliseconds(),
			"cleanup_interval_ms",
			answerJobsConfig.CleanupInterval.Milliseconds(),
			"cleanup_batch_size", answerJobsConfig.CleanupBatchSize,
			"worker_id", answerJobsConfig.WorkerID,
			"lease_duration_ms",
			answerJobsConfig.JobLeaseDuration.Milliseconds(),
			"heartbeat_interval_ms",
			answerJobsConfig.JobHeartbeatInterval.Milliseconds(),
		)
	}

	var verificationService *verificationapplication.Service
	var verificationRequestLimiter *ratelimit.SlidingWindowLimiter
	var authRegisterService *authapplication.RegisterService
	var authPasswordResetService *authapplication.PasswordResetService
	var authLoginService *authapplication.LoginService
	var authSessionService *authapplication.SessionService
	var authRequestLimiter *ratelimit.SlidingWindowLimiter
	if rolePlan.serveHTTP {
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

		verificationService = verificationapplication.NewService(
			verificationChallengeRepository,
			verificationinfrastructure.NewRandomCodeGenerator(),
			verificationCodeHasher,
			verificationSender,
			time.Now,
			verificationapplication.DefaultChallengeTTL,
			verificationapplication.DefaultResendCooldown,
		)
		verificationRequestLimiter, err = ratelimit.NewSlidingWindowLimiter(
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
		authRegisterService, err = authapplication.NewRegisterService(
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
		authPasswordResetService, err = authapplication.NewPasswordResetService(
			authPasswordResetRepository,
			passwordHasher,
			verificationCodeHasher,
			time.Now,
		)
		if err != nil {
			return fmt.Errorf("create auth password reset service: %w", err)
		}
		authLoginService, err = authapplication.NewLoginService(
			authSessionRepository,
			passwordHasher,
			sessionTokenManager,
			time.Now,
			authConfig.SessionTTL,
		)
		if err != nil {
			return fmt.Errorf("create auth login service: %w", err)
		}
		authSessionService, err = authapplication.NewSessionService(
			authSessionRepository,
			sessionTokenManager,
			time.Now,
		)
		if err != nil {
			return fmt.Errorf("create auth session service: %w", err)
		}
		authRequestLimiter, err = ratelimit.NewSlidingWindowLimiter(
			authConfig.RateLimitWindow,
			authConfig.PerClientLimit,
			authConfig.GlobalLimit,
		)
		if err != nil {
			return fmt.Errorf("create auth request limiter: %w", err)
		}
	}

	// 默认不启动远程向量 Worker，避免开发者未明确授权时产生后台 API 调用。
	var embeddingWorkerPool *documentapplication.WorkerPool
	if rolePlan.runEmbeddingWorker && embeddingConfig.WorkerEnabled {

		retryPolicy, err := embeddingapplication.NewRetryPolicy(
			embeddingConfig.MaxAttempts,
			embeddingConfig.RetryBaseDelay,
			embeddingConfig.RetryMaxDelay,
		)
		if err != nil {
			return fmt.Errorf("create embedding retry policy: %w", err)
		}

		embeddingWorker, err := embeddingapplication.NewWorkerWithHeartbeatInterval(
			embeddingJobRepository,
			chunkRepository,
			workerEmbedder,
			observability.NewEmbeddingJobLogger(logger),
			embeddingConfig.BatchSize,
			embeddingConfig.ProcessingTimeout,
			retryPolicy,
			embeddingConfig.JobHeartbeatInterval,
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
			"owner_in_flight_limit", embeddingConfig.OwnerInFlightLimit,
			"owner_borrowed_limit", embeddingConfig.OwnerBorrowedLimit,
			"starvation_threshold_ms",
			embeddingConfig.StarvationThreshold.Milliseconds(),
			"worker_id", embeddingConfig.WorkerID,
			"lease_duration_ms", embeddingConfig.JobLeaseDuration.Milliseconds(),
			"heartbeat_interval_ms",
			embeddingConfig.JobHeartbeatInterval.Milliseconds(),
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

	if documentWorkerPool != nil {
		startBackgroundWorker(documentWorkerPool.Run)
	}
	if embeddingWorkerPool != nil {
		startBackgroundWorker(embeddingWorkerPool.Run)
	}
	if answerJobWorkerPool != nil {
		startBackgroundWorker(answerJobWorkerPool.Run)
	}
	if answerJobCleanupLoop != nil {
		startBackgroundWorker(answerJobCleanupLoop.Run)
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

	// Worker-only 角色没有 HTTP 监听端口。完成组装后只等待进程退出信号，
	// 实际任务由上面的后台循环从 PostgreSQL 队列领取。
	if !rolePlan.serveHTTP {
		cleanupReadyFile, err := writeApplicationReadyFile(
			appConfig.ReadyFile,
			appConfig.Role,
		)
		if err != nil {
			return err
		}
		defer func() {
			if err := cleanupReadyFile(); err != nil {
				logger.Warn(
					"Remove application ready file",
					"event", "application_ready_file_remove_failed",
					"role", appConfig.Role,
					"error", err,
				)
			}
		}()

		logger.Info(
			"Application started",
			"event", "application_started",
			"role", appConfig.Role,
			"http_enabled", false,
			"ready_file_enabled", appConfig.ReadyFile != "",
		)
		<-ctx.Done()
		logger.Info(
			"Application shutdown started",
			"event", "application_shutdown_started",
			"role", appConfig.Role,
			"cause", ctx.Err(),
		)
		return nil
	}

	// API 角色创建面向 HTTP 用例的服务。Worker-only 角色不会经过该区块，
	// 因而不会加载认证、上传、同步检索或任务提交 Handler。
	documentService := documentapplication.NewService(scopedDocumentRepository)
	baseDocumentUploadService := documentapplication.NewUploadService(
		scopedDocumentRepository,
		fileStorage,
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
	documentListService := documentapplication.NewListService(
		scopedDocumentRepository,
	)
	documentChunkListService := documentapplication.NewChunkListService(
		scopedDocumentRepository,
		scopedChunkRepository,
	)
	documentSearchService := documentapplication.NewSearchService(
		scopedChunkRepository,
	)
	documentDeleteService := documentapplication.NewDeleteService(
		scopedDocumentRepository,
		fileStorage,
	)
	documentProcessingService := documentapplication.NewQueueProcessingService(
		scopedDocumentRepository,
		scopedProcessingJobRepository,
	)
	processingJobService := documentapplication.NewProcessingJobService(
		scopedProcessingJobRepository,
	)
	processingJobLatestService :=
		documentapplication.NewProcessingJobLatestService(
			scopedProcessingJobRepository,
		)
	processingJobCancelService :=
		documentapplication.NewProcessingJobCancelService(
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
	processingJobLatestHandler := api.NewProcessingJobLatestHandler(
		processingJobLatestService,
		logger,
	)
	processingJobCancelHandler := api.NewProcessingJobCancelHandler(
		processingJobCancelService,
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
		answerHandler = api.NewAnswerHandler(
			answerService,
			generationConfig.QueueWaitTimeout,
		)
	}
	var answerJobHandler *api.AnswerJobHandler
	if answerJobService != nil {
		answerJobHandler = api.NewAnswerJobHandler(answerJobService, logger)
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
	processingJobLatestHandler.RegisterRoutes(protectedRoutes)
	processingJobCancelHandler.RegisterRoutes(protectedRoutes)
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
	if answerJobHandler != nil {
		answerJobHandler.RegisterRoutes(protectedRoutes)
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
		"role", appConfig.Role,
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
