package main

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"strings"

	"github.com/gin-gonic/gin"
	"github.com/jackc/pgx/v5/pgxpool"

	"rag-reasoning-platform/backend/internal/api"
	agentapplication "rag-reasoning-platform/backend/internal/application/agent"
	"rag-reasoning-platform/backend/internal/application/agentruntime"
	answerapplication "rag-reasoning-platform/backend/internal/application/answer"
	documentapplication "rag-reasoning-platform/backend/internal/application/document"
	"rag-reasoning-platform/backend/internal/config"
	"rag-reasoning-platform/backend/internal/infrastructure/postgres"
	"rag-reasoning-platform/backend/internal/observability"
)

var (
	errAgentComponentDependencies  = errors.New("Agent runtime dependencies must be provided")
	errAgentEmbeddingKeyRequired   = errors.New("Agent Worker requires an Embedding provider API key")
	errAgentCapacityRequired       = errors.New("Agent Worker requires capacity coordination")
	errAgentAnswerCapacityMismatch = errors.New("Answer and Agent capacity limits must match for a shared provider model")
)

type agentRuntimePlan struct {
	enableAPI    bool
	enableWorker bool
}

func newAgentRuntimePlan(role applicationRolePlan, agentConfig config.AgentConfig) agentRuntimePlan {
	return agentRuntimePlan{
		enableAPI:    role.serveHTTP && agentConfig.Enabled,
		enableWorker: role.runAgentWorker && agentConfig.WorkerEnabled,
	}
}

func (p agentRuntimePlan) enabled() bool { return p.enableAPI || p.enableWorker }

type agentComponents struct {
	handler         *api.AgentRunHandler
	workerPool      *documentapplication.WorkerPool
	startupRecovery observability.AgentWorkerRunner
}

type agentComponentInputs struct {
	plan           agentRuntimePlan
	pool           *pgxpool.Pool
	scopedChunks   *postgres.ScopedChunkRepository
	semanticSearch agentruntime.SemanticSearchService
	capacityStore  agentapplication.ModelCapacityStore
	capacityConfig config.CapacityCoordinationConfig
	agentConfig    config.AgentConfig
	logger         *slog.Logger
	modelFactory   agentModelFactory
}

func newAgentComponents(inputs agentComponentInputs) (agentComponents, error) {
	if !inputs.plan.enabled() {
		return agentComponents{}, nil
	}
	if inputs.pool == nil || inputs.logger == nil {
		return agentComponents{}, errAgentComponentDependencies
	}

	repository := postgres.NewAgentRepositoryWithPolicies(
		inputs.pool,
		inputs.agentConfig.AdmissionLimits(),
		inputs.agentConfig.SchedulingPolicy(),
		inputs.agentConfig.LeasePolicy(),
	)
	components := agentComponents{}
	if inputs.plan.enableAPI {
		service, err := agentapplication.NewService(
			repository,
			repository,
			inputs.agentConfig.RunPolicy(),
		)
		if err != nil {
			return agentComponents{}, fmt.Errorf("create Agent application service: %w", err)
		}
		components.handler = api.NewAgentRunHandler(service, inputs.logger)
	}
	if !inputs.plan.enableWorker {
		return components, nil
	}
	if inputs.scopedChunks == nil || inputs.semanticSearch == nil || inputs.capacityStore == nil ||
		inputs.modelFactory == nil || !inputs.capacityConfig.Enabled {
		return agentComponents{}, errAgentComponentDependencies
	}

	observedRepository := observability.NewAgentExecutionRepositoryLogger(
		repository,
		repository,
		inputs.logger,
	)
	contextProvider, err := agentapplication.NewRuntimeContextProvider(
		postgres.NewAgentContextRepository(inputs.pool),
		inputs.agentConfig.HistoryConfig(),
	)
	if err != nil {
		return agentComponents{}, fmt.Errorf("create Agent runtime context: %w", err)
	}
	searchDocuments, err := agentruntime.NewSearchDocuments(
		documentapplication.NewSearchService(inputs.scopedChunks),
		inputs.semanticSearch,
	)
	if err != nil {
		return agentComponents{}, fmt.Errorf("create Agent search tools: %w", err)
	}
	evidenceReader, err := agentapplication.NewRuntimeEvidenceReader(
		postgres.NewAgentEvidenceRepository(inputs.pool),
	)
	if err != nil {
		return agentComponents{}, fmt.Errorf("create Agent evidence reader: %w", err)
	}
	tools, err := agentapplication.NewToolRegistry(searchDocuments, evidenceReader)
	if err != nil {
		return agentComponents{}, fmt.Errorf("create Agent Tool Registry: %w", err)
	}
	baseModel, err := inputs.modelFactory(inputs.agentConfig)
	if err != nil {
		return agentComponents{}, err
	}
	keyBuilder, err := answerapplication.NewProviderCapacityKeyBuilder(
		inputs.capacityConfig.Namespace,
		inputs.agentConfig.ProviderName,
		inputs.agentConfig.ModelName,
	)
	if err != nil {
		return agentComponents{}, fmt.Errorf("create Agent capacity keys: %w", err)
	}
	capacityPolicy, err := inputs.agentConfig.ModelCapacityConfig(inputs.capacityConfig.RetryInterval)
	if err != nil {
		return agentComponents{}, fmt.Errorf("create Agent capacity policy: %w", err)
	}
	capacityModel, err := agentapplication.NewCapacityModel(
		baseModel,
		inputs.capacityStore,
		observability.NewAgentModelCapacityLogger(inputs.logger),
		keyBuilder,
		capacityPolicy,
	)
	if err != nil {
		return agentComponents{}, fmt.Errorf("create capacity-aware Agent model: %w", err)
	}
	worker, err := agentapplication.NewWorker(
		observedRepository,
		contextProvider,
		capacityModel,
		tools,
		inputs.agentConfig.WorkerPolicy(),
	)
	if err != nil {
		return agentComponents{}, fmt.Errorf("create Agent Worker: %w", err)
	}
	observedWorker := observability.NewAgentWorkerLogger(worker, inputs.logger)
	workerLoop, err := documentapplication.NewWorkerLoop(
		observedWorker,
		inputs.agentConfig.PollInterval,
		func(error) {
			inputs.logger.Warn(
				"Agent Worker iteration failed",
				"event", "agent_worker_loop_error",
			)
		},
	)
	if err != nil {
		return agentComponents{}, fmt.Errorf("create Agent Worker loop: %w", err)
	}
	workerPool, err := documentapplication.NewWorkerPool(
		workerLoop,
		inputs.agentConfig.WorkerConcurrency,
	)
	if err != nil {
		return agentComponents{}, fmt.Errorf("create Agent Worker pool: %w", err)
	}
	components.workerPool = workerPool
	components.startupRecovery = observedWorker
	return components, nil
}

func validateAgentWorkerPrerequisites(
	plan agentRuntimePlan,
	agentConfig config.AgentConfig,
	embeddingConfig config.EmbeddingConfig,
	capacityConfig config.CapacityCoordinationConfig,
) error {
	if !plan.enableWorker {
		return nil
	}
	if strings.TrimSpace(agentConfig.APIKey) == "" {
		return config.ErrAgentAPIKeyRequired
	}
	if strings.TrimSpace(embeddingConfig.APIKey) == "" {
		return errAgentEmbeddingKeyRequired
	}
	if !capacityConfig.Enabled {
		return errAgentCapacityRequired
	}
	return nil
}

func validateSharedAnswerAgentCapacity(
	agentWorkerEnabled bool,
	agentConfig config.AgentConfig,
	generationConfig config.GenerationConfig,
) error {
	if !agentWorkerEnabled ||
		agentConfig.ProviderName != config.AgentProviderDashScope ||
		agentConfig.ModelName != generationConfig.ModelName {
		return nil
	}
	if agentConfig.ModelMaxConcurrency != generationConfig.MaxConcurrency ||
		agentConfig.ModelMaxConcurrencyPerOwner != generationConfig.MaxConcurrencyPerUser {
		return errAgentAnswerCapacityMismatch
	}
	return nil
}

type agentRouteRegistrar interface {
	RegisterRoutes(gin.IRoutes)
}

func registerAgentRoutes(routes gin.IRoutes, handler agentRouteRegistrar) {
	if handler != nil {
		handler.RegisterRoutes(routes)
	}
}

func recoverAgentWorkerForStartup(
	ctx context.Context,
	worker observability.AgentWorkerRunner,
) (int64, error) {
	if worker == nil {
		return 0, nil
	}
	return worker.RecoverExpiredRuns(ctx)
}
