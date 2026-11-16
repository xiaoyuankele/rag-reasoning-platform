package config

import (
	"errors"
	"fmt"
	"math"
	"os"
	"strconv"
	"strings"
	"time"

	agentapplication "rag-reasoning-platform/backend/internal/application/agent"
	answerapplication "rag-reasoning-platform/backend/internal/application/answer"
	agentdomain "rag-reasoning-platform/backend/internal/domain/agent"
)

const (
	AgentProviderDashScope = "dashscope"

	defaultDashScopeAgentEndpoint        = "https://dashscope.aliyuncs.com/compatible-mode/v1/chat/completions"
	defaultAgentModel                    = "qwen3.6-flash"
	defaultAgentModelHTTPTimeout         = 35 * time.Second
	defaultAgentModelMaxOutputTokens     = 2048
	maximumAgentModelMaxOutputTokens     = 8192
	defaultAgentModelTemperature         = 0.1
	defaultAgentModelMaxConcurrency      = 10
	maximumAgentModelMaxConcurrency      = 128
	defaultAgentModelMaxConcurrencyOwner = 2
	maximumAgentModelMaxConcurrencyOwner = 16
	defaultAgentModelCapacityLeaseTTL    = 45 * time.Second
	defaultAgentModelCapacityWaitTimeout = 2 * time.Second
	defaultAgentMaxQueuedPerOwner        = 5
	maximumAgentMaxQueuedPerOwner        = 100
	defaultAgentMaxQueuedGlobal          = 500
	maximumAgentMaxQueuedGlobal          = 10_000
	defaultAgentOwnerInFlightLimit       = 1
	defaultAgentOwnerBorrowedLimit       = 1
	maximumAgentOwnerInFlightLimit       = 64
	defaultAgentStarvationThreshold      = 30 * time.Second
	defaultAgentWorkerConcurrency        = 10
	maximumAgentWorkerConcurrency        = 32
	defaultAgentPollInterval             = time.Second
	defaultAgentModelTimeout             = 30 * time.Second
	defaultAgentToolTimeout              = 15 * time.Second
	defaultAgentRetryDelay               = 2 * time.Second
	defaultAgentRecoveryBatchSize        = agentapplication.MaxRecoveryBatchSize
	defaultAgentLeaseDuration            = 60 * time.Second
	defaultAgentHeartbeatInterval        = 15 * time.Second
	defaultAgentRunTimeout               = 120 * time.Second
	minimumAgentReservedInputTokens      = 2_000
)

var (
	ErrAgentAPIKeyRequired          = errors.New("Agent API key must be provided when the Agent Worker is enabled")
	ErrInvalidAgentModelTemperature = errors.New("Agent model temperature must be between 0 and 2")
	ErrInvalidAgentModelCapacity    = errors.New("Agent model capacity limits are invalid")
	ErrInvalidAgentQueueCapacity    = errors.New("Agent owner queue capacity must not exceed global capacity")
	ErrInvalidAgentSchedulingPolicy = errors.New("Agent v0 owner scheduling limits must both equal 1")
	ErrInvalidAgentRuntimeTiming    = errors.New("Agent runtime timeout and lease timing is invalid")
	ErrInvalidAgentOutputBudget     = errors.New("Agent model output budget must be smaller than total Token budget")
	ErrInvalidAgentHistoryBudget    = errors.New("Agent history budget must leave space for the current question and evidence")
)

type AgentConfig struct {
	Enabled       bool
	WorkerEnabled bool

	ProviderName         string
	APIKey               string
	Endpoint             string
	ModelName            string
	ModelHTTPTimeout     time.Duration
	ModelMaxOutputTokens int
	ModelTemperature     float64

	ModelMaxConcurrency         int
	ModelMaxConcurrencyPerOwner int
	ModelCapacityLeaseTTL       time.Duration
	ModelCapacityWaitTimeout    time.Duration

	MaxSteps       int
	MaxTotalTokens int
	RunTimeout     time.Duration

	MaxQueuedPerOwner   int
	MaxQueuedGlobal     int
	OwnerInFlightLimit  int
	OwnerBorrowedLimit  int
	StarvationThreshold time.Duration

	WorkerConcurrency int
	PollInterval      time.Duration
	ModelTimeout      time.Duration
	ToolTimeout       time.Duration
	RetryDelay        time.Duration
	RecoveryBatchSize int
	WorkerID          string
	LeaseDuration     time.Duration
	HeartbeatInterval time.Duration

	ConversationHistoryMaxTurns  int
	ConversationHistoryMaxTokens int
	InputTokenBudget             int
}

func LoadAgent() (AgentConfig, error) {
	enabled, err := loadOptionalBool("AGENT_ENABLED", false)
	if err != nil {
		return AgentConfig{}, err
	}
	workerEnabled, err := loadOptionalBool("AGENT_WORKER_ENABLED", false)
	if err != nil {
		return AgentConfig{}, err
	}
	apiKey := strings.TrimSpace(os.Getenv("DASHSCOPE_API_KEY"))
	if workerEnabled && apiKey == "" {
		return AgentConfig{}, ErrAgentAPIKeyRequired
	}

	endpoint := strings.TrimSpace(os.Getenv("DASHSCOPE_AGENT_ENDPOINT"))
	if endpoint == "" {
		endpoint = defaultDashScopeAgentEndpoint
	}
	modelName := strings.TrimSpace(os.Getenv("AGENT_MODEL"))
	if modelName == "" {
		modelName = defaultAgentModel
	}

	modelHTTPTimeout, err := loadPositiveDuration("AGENT_MODEL_HTTP_TIMEOUT", defaultAgentModelHTTPTimeout)
	if err != nil {
		return AgentConfig{}, err
	}
	modelMaxOutputTokens, err := loadPositiveBoundedInt(
		"AGENT_MODEL_MAX_OUTPUT_TOKENS", defaultAgentModelMaxOutputTokens, maximumAgentModelMaxOutputTokens,
	)
	if err != nil {
		return AgentConfig{}, err
	}
	modelTemperature, err := loadAgentModelTemperature()
	if err != nil {
		return AgentConfig{}, err
	}
	modelMaxConcurrency, err := loadPositiveBoundedInt(
		"AGENT_MODEL_MAX_CONCURRENCY", defaultAgentModelMaxConcurrency, maximumAgentModelMaxConcurrency,
	)
	if err != nil {
		return AgentConfig{}, err
	}
	modelMaxConcurrencyPerOwner, err := loadPositiveBoundedInt(
		"AGENT_MODEL_MAX_CONCURRENCY_PER_OWNER", defaultAgentModelMaxConcurrencyOwner, maximumAgentModelMaxConcurrencyOwner,
	)
	if err != nil {
		return AgentConfig{}, err
	}
	modelCapacityLeaseTTL, err := loadPositiveDuration(
		"AGENT_MODEL_CAPACITY_LEASE_TTL", defaultAgentModelCapacityLeaseTTL,
	)
	if err != nil {
		return AgentConfig{}, err
	}
	modelCapacityWaitTimeout, err := loadPositiveDuration(
		"AGENT_MODEL_CAPACITY_WAIT_TIMEOUT", defaultAgentModelCapacityWaitTimeout,
	)
	if err != nil {
		return AgentConfig{}, err
	}

	maxSteps, err := loadPositiveBoundedInt("AGENT_MAX_STEPS", agentdomain.MaxPersistedSteps, agentdomain.MaxPersistedSteps)
	if err != nil {
		return AgentConfig{}, err
	}
	maxTotalTokens, err := loadPositiveBoundedInt(
		"AGENT_MAX_TOTAL_TOKENS", agentdomain.MaxRunTotalTokens, agentdomain.MaxRunTotalTokens,
	)
	if err != nil {
		return AgentConfig{}, err
	}
	runTimeout, err := loadPositiveDuration("AGENT_RUN_TIMEOUT", defaultAgentRunTimeout)
	if err != nil {
		return AgentConfig{}, err
	}
	maxQueuedPerOwner, err := loadPositiveBoundedInt(
		"AGENT_MAX_QUEUED_PER_OWNER", defaultAgentMaxQueuedPerOwner, maximumAgentMaxQueuedPerOwner,
	)
	if err != nil {
		return AgentConfig{}, err
	}
	maxQueuedGlobal, err := loadPositiveBoundedInt(
		"AGENT_MAX_QUEUED_GLOBAL", defaultAgentMaxQueuedGlobal, maximumAgentMaxQueuedGlobal,
	)
	if err != nil {
		return AgentConfig{}, err
	}
	ownerInFlightLimit, err := loadPositiveBoundedInt(
		"AGENT_OWNER_IN_FLIGHT_LIMIT", defaultAgentOwnerInFlightLimit, maximumAgentOwnerInFlightLimit,
	)
	if err != nil {
		return AgentConfig{}, err
	}
	ownerBorrowedLimit, err := loadPositiveBoundedInt(
		"AGENT_OWNER_BORROWED_LIMIT", defaultAgentOwnerBorrowedLimit, maximumAgentOwnerInFlightLimit,
	)
	if err != nil {
		return AgentConfig{}, err
	}
	starvationThreshold, err := loadPositiveDuration("AGENT_STARVATION_THRESHOLD", defaultAgentStarvationThreshold)
	if err != nil {
		return AgentConfig{}, err
	}
	workerConcurrency, err := loadPositiveBoundedInt(
		"AGENT_WORKER_CONCURRENCY", defaultAgentWorkerConcurrency, maximumAgentWorkerConcurrency,
	)
	if err != nil {
		return AgentConfig{}, err
	}
	pollInterval, err := loadPositiveDuration("AGENT_POLL_INTERVAL", defaultAgentPollInterval)
	if err != nil {
		return AgentConfig{}, err
	}
	modelTimeout, err := loadPositiveDuration("AGENT_MODEL_TIMEOUT", defaultAgentModelTimeout)
	if err != nil {
		return AgentConfig{}, err
	}
	toolTimeout, err := loadPositiveDuration("AGENT_TOOL_TIMEOUT", defaultAgentToolTimeout)
	if err != nil {
		return AgentConfig{}, err
	}
	retryDelay, err := loadPositiveDuration("AGENT_RETRY_DELAY", defaultAgentRetryDelay)
	if err != nil {
		return AgentConfig{}, err
	}
	recoveryBatchSize, err := loadPositiveBoundedInt(
		"AGENT_RECOVERY_BATCH_SIZE", defaultAgentRecoveryBatchSize, agentapplication.MaxRecoveryBatchSize,
	)
	if err != nil {
		return AgentConfig{}, err
	}
	workerID := strings.TrimSpace(os.Getenv("AGENT_WORKER_ID"))
	if workerID == "" {
		hostname, hostnameErr := os.Hostname()
		if hostnameErr != nil || strings.TrimSpace(hostname) == "" {
			hostname = "agent-worker"
		}
		workerID = fmt.Sprintf("%s-%d", strings.TrimSpace(hostname), os.Getpid())
	}
	leaseDuration, err := loadPositiveDuration("AGENT_LEASE_DURATION", defaultAgentLeaseDuration)
	if err != nil {
		return AgentConfig{}, err
	}
	heartbeatInterval, err := loadPositiveDuration("AGENT_HEARTBEAT_INTERVAL", defaultAgentHeartbeatInterval)
	if err != nil {
		return AgentConfig{}, err
	}
	historyMaxTurns, err := loadPositiveBoundedInt(
		"CONVERSATION_HISTORY_MAX_TURNS", defaultConversationHistoryMaxTurns, maxContextHistoryTurnsForAgent,
	)
	if err != nil {
		return AgentConfig{}, err
	}
	historyMaxTokens, err := loadPositiveBoundedInt(
		"CONVERSATION_HISTORY_MAX_TOKENS", defaultConversationHistoryMaxTokens, maximumConversationHistoryMaxTokens,
	)
	if err != nil {
		return AgentConfig{}, err
	}
	inputTokenBudget, err := loadPositiveBoundedInt(
		"ANSWER_INPUT_TOKEN_BUDGET", defaultAnswerInputTokenBudget, maximumAnswerInputTokenBudget,
	)
	if err != nil {
		return AgentConfig{}, err
	}

	config := AgentConfig{
		Enabled: enabled, WorkerEnabled: workerEnabled,
		ProviderName: AgentProviderDashScope, APIKey: apiKey, Endpoint: endpoint, ModelName: modelName,
		ModelHTTPTimeout: modelHTTPTimeout, ModelMaxOutputTokens: modelMaxOutputTokens,
		ModelTemperature: modelTemperature, ModelMaxConcurrency: modelMaxConcurrency,
		ModelMaxConcurrencyPerOwner: modelMaxConcurrencyPerOwner,
		ModelCapacityLeaseTTL:       modelCapacityLeaseTTL, ModelCapacityWaitTimeout: modelCapacityWaitTimeout,
		MaxSteps: maxSteps, MaxTotalTokens: maxTotalTokens, RunTimeout: runTimeout,
		MaxQueuedPerOwner: maxQueuedPerOwner, MaxQueuedGlobal: maxQueuedGlobal,
		OwnerInFlightLimit: ownerInFlightLimit, OwnerBorrowedLimit: ownerBorrowedLimit,
		StarvationThreshold: starvationThreshold, WorkerConcurrency: workerConcurrency,
		PollInterval: pollInterval, ModelTimeout: modelTimeout, ToolTimeout: toolTimeout,
		RetryDelay: retryDelay, RecoveryBatchSize: recoveryBatchSize, WorkerID: workerID,
		LeaseDuration: leaseDuration, HeartbeatInterval: heartbeatInterval,
		ConversationHistoryMaxTurns:  historyMaxTurns,
		ConversationHistoryMaxTokens: historyMaxTokens, InputTokenBudget: inputTokenBudget,
	}
	if err := config.Validate(); err != nil {
		return AgentConfig{}, err
	}
	return config, nil
}

const maxContextHistoryTurnsForAgent = 16

func (c AgentConfig) Validate() error {
	if c.ModelMaxConcurrencyPerOwner > c.ModelMaxConcurrency {
		return ErrInvalidAgentModelCapacity
	}
	if c.MaxQueuedPerOwner > c.MaxQueuedGlobal {
		return ErrInvalidAgentQueueCapacity
	}
	if c.OwnerInFlightLimit != 1 || c.OwnerBorrowedLimit != 1 {
		return ErrInvalidAgentSchedulingPolicy
	}
	if !(c.ModelTimeout < c.ModelCapacityLeaseTTL &&
		c.ModelCapacityLeaseTTL <= c.LeaseDuration &&
		c.LeaseDuration < c.RunTimeout &&
		c.HeartbeatInterval < c.LeaseDuration) {
		return ErrInvalidAgentRuntimeTiming
	}
	if c.ModelMaxOutputTokens >= c.MaxTotalTokens {
		return ErrInvalidAgentOutputBudget
	}
	if c.InputTokenBudget < c.ConversationHistoryMaxTokens+minimumAgentReservedInputTokens {
		return ErrInvalidAgentHistoryBudget
	}
	return nil
}

func (c AgentConfig) RunPolicy() agentapplication.RunPolicy {
	return agentapplication.RunPolicy{
		MaxSteps: c.MaxSteps, MaxTotalTokens: c.MaxTotalTokens, RunTimeout: c.RunTimeout,
	}
}

func (c AgentConfig) AdmissionLimits() agentdomain.AdmissionLimits {
	return agentdomain.AdmissionLimits{
		MaxQueuedRunsPerOwner: c.MaxQueuedPerOwner, MaxQueuedRunsGlobal: c.MaxQueuedGlobal,
	}
}

func (c AgentConfig) SchedulingPolicy() agentdomain.SchedulingPolicy {
	return agentdomain.SchedulingPolicy{
		MaxInFlightPerOwner:         c.OwnerInFlightLimit,
		MaxBorrowedInFlightPerOwner: c.OwnerBorrowedLimit,
		StarvationThreshold:         c.StarvationThreshold,
	}
}

func (c AgentConfig) LeasePolicy() agentdomain.LeasePolicy {
	return agentdomain.LeasePolicy{
		WorkerID: c.WorkerID, LeaseDuration: c.LeaseDuration,
		MaxAttempts: agentdomain.MaxAutomaticAttempts,
	}
}

func (c AgentConfig) WorkerPolicy() agentapplication.WorkerPolicy {
	return agentapplication.WorkerPolicy{
		ModelTimeout: c.ModelTimeout, ToolTimeout: c.ToolTimeout,
		HeartbeatInterval: c.HeartbeatInterval, RetryDelay: c.RetryDelay,
		RecoveryBatchSize: c.RecoveryBatchSize,
	}
}

func (c AgentConfig) HistoryConfig() answerapplication.ConversationHistoryConfig {
	return answerapplication.ConversationHistoryConfig{
		MaxTurns: c.ConversationHistoryMaxTurns, MaxTokens: c.ConversationHistoryMaxTokens,
		InputTokenBudget: c.InputTokenBudget,
	}
}

func (c AgentConfig) ModelCapacityConfig(retryInterval time.Duration) (agentapplication.ModelCapacityConfig, error) {
	config := agentapplication.ModelCapacityConfig{
		MaxConcurrencyGlobal:   c.ModelMaxConcurrency,
		MaxConcurrencyPerOwner: c.ModelMaxConcurrencyPerOwner,
		LeaseTTL:               c.ModelCapacityLeaseTTL, RetryInterval: retryInterval,
		WaitTimeout: c.ModelCapacityWaitTimeout,
	}
	if !config.IsValid() {
		return agentapplication.ModelCapacityConfig{}, ErrInvalidAgentModelCapacity
	}
	return config, nil
}

func loadAgentModelTemperature() (float64, error) {
	raw := strings.TrimSpace(os.Getenv("AGENT_MODEL_TEMPERATURE"))
	if raw == "" {
		return defaultAgentModelTemperature, nil
	}
	value, err := strconv.ParseFloat(raw, 64)
	if err != nil || math.IsNaN(value) || math.IsInf(value, 0) || value < 0 || value > 2 {
		return 0, ErrInvalidAgentModelTemperature
	}
	return value, nil
}
