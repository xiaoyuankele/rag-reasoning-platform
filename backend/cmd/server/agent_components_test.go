package main

import (
	"context"
	"errors"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/jackc/pgx/v5/pgxpool"

	agentapplication "rag-reasoning-platform/backend/internal/application/agent"
	"rag-reasoning-platform/backend/internal/application/agentruntime"
	embeddingapplication "rag-reasoning-platform/backend/internal/application/embedding"
	"rag-reasoning-platform/backend/internal/config"
	accessdomain "rag-reasoning-platform/backend/internal/domain/access"
	documentdomain "rag-reasoning-platform/backend/internal/domain/document"
	"rag-reasoning-platform/backend/internal/infrastructure/postgres"
)

type fakeAgentAssemblyModel struct{}

func (fakeAgentAssemblyModel) Decide(context.Context, agentapplication.ModelRequest) (agentapplication.ModelDecision, error) {
	return agentapplication.ModelDecision{}, agentapplication.ErrInvalidModelDecision
}

type fakeAgentAssemblyCapacityStore struct{}

func (fakeAgentAssemblyCapacityStore) AcquireCapacity(context.Context, []string, []int, time.Duration) (string, []int, bool, error) {
	return "token", []int{1, 1}, true, nil
}

func (fakeAgentAssemblyCapacityStore) ReleaseCapacity(context.Context, []string, string) error {
	return nil
}

type fakeAgentAssemblySemanticSearch struct{}

func (fakeAgentAssemblySemanticSearch) Search(
	context.Context,
	accessdomain.OwnerScope,
	embeddingapplication.SemanticSearchInput,
) (embeddingapplication.SemanticSearchOutput, error) {
	return embeddingapplication.SemanticSearchOutput{Hits: []documentdomain.SemanticSearchHit{}}, nil
}

type fakeAgentStartupWorker struct {
	events *[]string
	err    error
}

func (f *fakeAgentStartupWorker) RunOnce(context.Context) (bool, error) {
	*f.events = append(*f.events, "run")
	return false, nil
}

func (f *fakeAgentStartupWorker) RecoverExpiredRuns(context.Context) (int64, error) {
	*f.events = append(*f.events, "recovery")
	return 2, f.err
}

func TestNewAgentRuntimePlanHonorsRoleAndIndependentFlags(t *testing.T) {
	for _, test := range []struct {
		name   string
		role   applicationRolePlan
		config config.AgentConfig
		want   agentRuntimePlan
	}{
		{name: "all disabled", role: applicationRolePlan{serveHTTP: true, runAgentWorker: true}, want: agentRuntimePlan{}},
		{name: "API enabled", role: applicationRolePlan{serveHTTP: true}, config: config.AgentConfig{Enabled: true}, want: agentRuntimePlan{enableAPI: true}},
		{name: "API never runs Worker", role: applicationRolePlan{serveHTTP: true}, config: config.AgentConfig{WorkerEnabled: true}, want: agentRuntimePlan{}},
		{name: "dedicated Worker ignores API flag", role: applicationRolePlan{runAgentWorker: true}, config: config.AgentConfig{Enabled: true, WorkerEnabled: true}, want: agentRuntimePlan{enableWorker: true}},
		{name: "all both", role: applicationRolePlan{serveHTTP: true, runAgentWorker: true}, config: config.AgentConfig{Enabled: true, WorkerEnabled: true}, want: agentRuntimePlan{enableAPI: true, enableWorker: true}},
	} {
		t.Run(test.name, func(t *testing.T) {
			if got := newAgentRuntimePlan(test.role, test.config); got != test.want {
				t.Fatalf("newAgentRuntimePlan() = %+v, want %+v", got, test.want)
			}
		})
	}
}

func TestNewAgentComponentsSkipsEverythingWhenDisabled(t *testing.T) {
	components, err := newAgentComponents(agentComponentInputs{})
	if err != nil || components != (agentComponents{}) {
		t.Fatalf("newAgentComponents(disabled) = %+v, %v", components, err)
	}
}

func TestNewAgentComponentsAPIOnlyDoesNotConstructProvider(t *testing.T) {
	var modelCalls atomic.Int64
	agentConfig := validAgentAssemblyConfig()
	agentConfig.APIKey = ""
	agentConfig.WorkerEnabled = false
	components, err := newAgentComponents(agentComponentInputs{
		plan:        agentRuntimePlan{enableAPI: true},
		pool:        &pgxpool.Pool{},
		agentConfig: agentConfig,
		logger:      slog.New(slog.NewTextHandler(io.Discard, nil)),
		modelFactory: func(config.AgentConfig) (agentapplication.AgentModel, error) {
			modelCalls.Add(1)
			return fakeAgentAssemblyModel{}, nil
		},
	})
	if err != nil || components.handler == nil || components.workerPool != nil ||
		components.startupRecovery != nil || modelCalls.Load() != 0 {
		t.Fatalf("newAgentComponents(API) = %+v, %v model_calls=%d", components, err, modelCalls.Load())
	}
}

func TestNewAgentComponentsBuildsWorkerWithPublicSearchFlagsOff(t *testing.T) {
	var modelCalls atomic.Int64
	pool := &pgxpool.Pool{}
	components, err := newAgentComponents(agentComponentInputs{
		plan:           agentRuntimePlan{enableWorker: true},
		pool:           pool,
		scopedChunks:   postgres.NewScopedChunkRepository(pool),
		semanticSearch: fakeAgentAssemblySemanticSearch{},
		capacityStore:  fakeAgentAssemblyCapacityStore{},
		capacityConfig: config.CapacityCoordinationConfig{
			Enabled: true, Namespace: "test-capacity", RetryInterval: time.Millisecond,
		},
		agentConfig: validAgentAssemblyConfig(),
		logger:      slog.New(slog.NewTextHandler(io.Discard, nil)),
		modelFactory: func(config.AgentConfig) (agentapplication.AgentModel, error) {
			modelCalls.Add(1)
			return fakeAgentAssemblyModel{}, nil
		},
	})
	if err != nil || components.handler != nil || components.workerPool == nil ||
		components.startupRecovery == nil || modelCalls.Load() != 1 {
		t.Fatalf("newAgentComponents(Worker) = %+v, %v model_calls=%d", components, err, modelCalls.Load())
	}
}

func TestValidateAgentWorkerPrerequisitesFailsClosed(t *testing.T) {
	plan := agentRuntimePlan{enableWorker: true}
	validAgent := validAgentAssemblyConfig()
	validEmbedding := config.EmbeddingConfig{APIKey: "fake-embedding-key"}
	validCapacity := config.CapacityCoordinationConfig{Enabled: true}
	for _, test := range []struct {
		name      string
		agent     config.AgentConfig
		embedding config.EmbeddingConfig
		capacity  config.CapacityCoordinationConfig
	}{
		{name: "missing Agent key", agent: func() config.AgentConfig { value := validAgent; value.APIKey = ""; return value }(), embedding: validEmbedding, capacity: validCapacity},
		{name: "missing Embedding key", agent: validAgent, embedding: config.EmbeddingConfig{}, capacity: validCapacity},
		{name: "capacity disabled", agent: validAgent, embedding: validEmbedding, capacity: config.CapacityCoordinationConfig{}},
	} {
		t.Run(test.name, func(t *testing.T) {
			if err := validateAgentWorkerPrerequisites(plan, test.agent, test.embedding, test.capacity); err == nil {
				t.Fatal("validateAgentWorkerPrerequisites() error = nil")
			}
		})
	}
	if err := validateAgentWorkerPrerequisites(agentRuntimePlan{}, config.AgentConfig{}, config.EmbeddingConfig{}, config.CapacityCoordinationConfig{}); err != nil {
		t.Fatalf("disabled prerequisites error = %v", err)
	}
	if err := validateAgentWorkerPrerequisites(plan, validAgent, validEmbedding, validCapacity); err != nil {
		t.Fatalf("valid prerequisites error = %v", err)
	}
}

func TestValidateSharedAnswerAgentCapacity(t *testing.T) {
	agentConfig := validAgentAssemblyConfig()
	generationConfig := config.GenerationConfig{
		ModelName:             agentConfig.ModelName,
		MaxConcurrency:        agentConfig.ModelMaxConcurrency,
		MaxConcurrencyPerUser: agentConfig.ModelMaxConcurrencyPerOwner,
	}
	if err := validateSharedAnswerAgentCapacity(true, agentConfig, generationConfig); err != nil {
		t.Fatalf("matching capacity error = %v", err)
	}
	generationConfig.MaxConcurrency++
	if err := validateSharedAnswerAgentCapacity(true, agentConfig, generationConfig); err == nil {
		t.Fatal("mismatched shared capacity error = nil")
	}
	generationConfig.ModelName = "different-model"
	if err := validateSharedAnswerAgentCapacity(true, agentConfig, generationConfig); err != nil {
		t.Fatalf("different model capacity error = %v", err)
	}
}

func TestDedicatedAgentWorkerRejectsConfiguredAnswerCapacityMismatch(t *testing.T) {
	role, err := newApplicationRolePlan(config.ApplicationRoleAgentWorker)
	if err != nil || !role.needsGenerationConfig() {
		t.Fatalf("Agent role/generation plan = %+v, %v", role, err)
	}
	agentConfig := validAgentAssemblyConfig()
	generationConfig := config.GenerationConfig{
		Enabled:               false,
		ModelName:             agentConfig.ModelName,
		MaxConcurrency:        agentConfig.ModelMaxConcurrency + 1,
		MaxConcurrencyPerUser: agentConfig.ModelMaxConcurrencyPerOwner,
	}
	if err := validateSharedAnswerAgentCapacity(true, agentConfig, generationConfig); err == nil {
		t.Fatal("dedicated Agent Worker accepted drifted Answer capacity limits")
	}
}

func TestRegisterAgentRoutesDisabled404EnabledUnauthenticated401(t *testing.T) {
	gin.SetMode(gin.TestMode)
	disabled := gin.New()
	disabledComponents, err := newAgentComponents(agentComponentInputs{})
	if err != nil {
		t.Fatal(err)
	}
	registerAgentRoutes(disabled.Group(""), disabledComponents.handler)
	const latestPath = "/conversations/5/agent-runs/latest"
	disabledResponse := httptest.NewRecorder()
	disabled.ServeHTTP(disabledResponse, httptest.NewRequest(http.MethodGet, latestPath, nil))
	if disabledResponse.Code != http.StatusNotFound || len(disabled.Routes()) != 0 {
		t.Fatalf("disabled route status = %d, registered routes = %d", disabledResponse.Code, len(disabled.Routes()))
	}

	components, err := newAgentComponents(agentComponentInputs{
		plan: agentRuntimePlan{enableAPI: true}, pool: &pgxpool.Pool{},
		agentConfig: validAgentAssemblyConfig(),
		logger:      slog.New(slog.NewTextHandler(io.Discard, nil)),
	})
	if err != nil {
		t.Fatalf("newAgentComponents() error = %v", err)
	}
	enabled := gin.New()
	registerAgentRoutes(enabled.Group(""), components.handler)
	enabledResponse := httptest.NewRecorder()
	enabled.ServeHTTP(enabledResponse, httptest.NewRequest(http.MethodGet, latestPath, nil))
	if enabledResponse.Code != http.StatusUnauthorized {
		t.Fatalf("enabled unauthenticated status = %d body=%s", enabledResponse.Code, enabledResponse.Body.String())
	}
}

func TestAgentStartupRecoveryCompletesBeforeReadyFile(t *testing.T) {
	events := make([]string, 0, 2)
	worker := &fakeAgentStartupWorker{events: &events}
	recovered, err := recoverAgentWorkerForStartup(t.Context(), worker)
	if err != nil || recovered != 2 {
		t.Fatalf("recoverAgentWorkerForStartup() = %d, %v", recovered, err)
	}
	readyPath := filepath.Join(t.TempDir(), "agent.ready")
	cleanup, err := writeApplicationReadyFile(readyPath, config.ApplicationRoleAgentWorker)
	if err != nil {
		t.Fatalf("writeApplicationReadyFile() error = %v", err)
	}
	events = append(events, "ready")
	if strings.Join(events, ",") != "recovery,ready" {
		t.Fatalf("startup events = %v", events)
	}
	content, err := os.ReadFile(readyPath)
	if err != nil || string(content) != "agent-worker\n" {
		t.Fatalf("ready file = %q, %v", content, err)
	}
	if err := cleanup(); err != nil {
		t.Fatalf("cleanup() error = %v", err)
	}
	if _, err := os.Stat(readyPath); !os.IsNotExist(err) {
		t.Fatalf("ready file remained after cleanup: %v", err)
	}
}

func TestAgentStartupRecoveryFailurePreventsReady(t *testing.T) {
	events := make([]string, 0, 1)
	worker := &fakeAgentStartupWorker{events: &events, err: errors.New("recovery failed")}
	if _, err := recoverAgentWorkerForStartup(t.Context(), worker); err == nil {
		t.Fatal("recoverAgentWorkerForStartup() error = nil")
	}
	if strings.Join(events, ",") != "recovery" {
		t.Fatalf("startup events = %v", events)
	}
}

func validAgentAssemblyConfig() config.AgentConfig {
	return config.AgentConfig{
		Enabled: true, WorkerEnabled: true,
		ProviderName: config.AgentProviderDashScope, APIKey: "fake-agent-key",
		Endpoint: "http://127.0.0.1:1/v1/chat/completions", ModelName: "qwen3.6-flash",
		ModelHTTPTimeout: 35 * time.Second, ModelMaxOutputTokens: 2048, ModelTemperature: 0.1,
		ModelMaxConcurrency: 10, ModelMaxConcurrencyPerOwner: 2,
		ModelCapacityLeaseTTL: 45 * time.Second, ModelCapacityWaitTimeout: 2 * time.Second,
		MaxSteps: 5, MaxTotalTokens: 48_000, RunTimeout: 120 * time.Second,
		MaxQueuedPerOwner: 5, MaxQueuedGlobal: 500,
		OwnerInFlightLimit: 1, OwnerBorrowedLimit: 1, StarvationThreshold: 30 * time.Second,
		WorkerConcurrency: 10, PollInterval: time.Second, ModelTimeout: 30 * time.Second,
		ToolTimeout: 15 * time.Second, RetryDelay: 2 * time.Second, RecoveryBatchSize: 100,
		WorkerID: "agent-worker-test", LeaseDuration: 60 * time.Second, HeartbeatInterval: 15 * time.Second,
		ConversationHistoryMaxTurns: 6, ConversationHistoryMaxTokens: 3000, InputTokenBudget: 16_000,
	}
}

var _ agentruntime.SemanticSearchService = fakeAgentAssemblySemanticSearch{}
