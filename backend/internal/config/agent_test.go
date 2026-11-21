package config

import (
	"errors"
	"strings"
	"testing"
	"time"

	agentapplication "rag-reasoning-platform/backend/internal/application/agent"
	agentdomain "rag-reasoning-platform/backend/internal/domain/agent"
)

func TestLoadAgentUsesCompleteSafeDefaultsAndTypedPolicies(t *testing.T) {
	clearAgentEnvironment(t)

	config, err := LoadAgent()
	if err != nil {
		t.Fatalf("LoadAgent() error = %v", err)
	}
	if config.Enabled || config.WorkerEnabled || config.APIKey != "" ||
		config.ProviderName != AgentProviderDashScope || config.Endpoint != defaultDashScopeAgentEndpoint ||
		config.ModelName != defaultAgentModel || config.ModelHTTPTimeout != 35*time.Second ||
		config.ModelMaxOutputTokens != 2048 || config.ModelTemperature != 0.1 ||
		config.ModelMaxConcurrency != 10 || config.ModelMaxConcurrencyPerOwner != 2 ||
		config.ModelCapacityLeaseTTL != 45*time.Second || config.ModelCapacityWaitTimeout != 2*time.Second ||
		config.MaxSteps != 5 || config.MaxTotalTokens != 48_000 || config.RunTimeout != 120*time.Second ||
		config.MaxQueuedPerOwner != 5 || config.MaxQueuedGlobal != 500 ||
		config.OwnerInFlightLimit != 1 || config.OwnerBorrowedLimit != 1 ||
		config.StarvationThreshold != 30*time.Second || config.WorkerConcurrency != 10 ||
		config.PollInterval != time.Second || config.ModelTimeout != 30*time.Second ||
		config.ToolTimeout != 15*time.Second || config.RetryDelay != 2*time.Second ||
		config.RecoveryBatchSize != 100 || strings.TrimSpace(config.WorkerID) == "" ||
		config.LeaseDuration != 60*time.Second || config.HeartbeatInterval != 15*time.Second ||
		config.ConversationHistoryMaxTurns != 6 || config.ConversationHistoryMaxTokens != 3000 ||
		config.InputTokenBudget != 16_000 {
		t.Fatalf("default Agent config = %+v", config)
	}
	capacity, err := config.ModelCapacityConfig(25 * time.Millisecond)
	if err != nil || !capacity.IsValid() || !config.RunPolicy().IsValid() ||
		!config.AdmissionLimits().IsValid() || !config.SchedulingPolicy().IsValid() ||
		!config.LeasePolicy().IsValid() || !config.WorkerPolicy().IsValid() ||
		!config.HistoryConfig().IsValid() || config.LeasePolicy().MaxAttempts != agentdomain.MaxAutomaticAttempts {
		t.Fatalf("typed policies invalid: capacity=%+v err=%v config=%+v", capacity, err, config)
	}
}

func TestLoadAgentFlagsAreIndependentAndOnlyWorkerRequiresKey(t *testing.T) {
	for _, test := range []struct {
		name          string
		enabled       string
		workerEnabled string
		apiKey        string
		want          AgentConfig
		wantKeyError  bool
	}{
		{name: "both disabled", enabled: "false", workerEnabled: "false", want: AgentConfig{}},
		{name: "API only", enabled: " true ", workerEnabled: "false", want: AgentConfig{Enabled: true}},
		{name: "Worker missing key", enabled: "false", workerEnabled: "true", wantKeyError: true},
		{name: "Worker only", enabled: "false", workerEnabled: " TRUE ", apiKey: " key ", want: AgentConfig{WorkerEnabled: true}},
		{name: "both enabled", enabled: "true", workerEnabled: "true", apiKey: "key", want: AgentConfig{Enabled: true, WorkerEnabled: true}},
	} {
		t.Run(test.name, func(t *testing.T) {
			clearAgentEnvironment(t)
			t.Setenv("AGENT_ENABLED", test.enabled)
			t.Setenv("AGENT_WORKER_ENABLED", test.workerEnabled)
			t.Setenv("DASHSCOPE_API_KEY", test.apiKey)
			got, err := LoadAgent()
			if test.wantKeyError {
				if !errors.Is(err, ErrAgentAPIKeyRequired) || got != (AgentConfig{}) {
					t.Fatalf("LoadAgent() = %+v, %v", got, err)
				}
				return
			}
			if err != nil || got.Enabled != test.want.Enabled || got.WorkerEnabled != test.want.WorkerEnabled {
				t.Fatalf("LoadAgent() = %+v, %v", got, err)
			}
		})
	}
}

func TestLoadAgentUsesAllRuntimeEnvironmentValuesWithoutAnswerEnablement(t *testing.T) {
	clearAgentEnvironment(t)
	values := map[string]string{
		"AGENT_ENABLED": "true", "AGENT_WORKER_ENABLED": "true", "DASHSCOPE_API_KEY": " agent-secret ",
		"DASHSCOPE_AGENT_ENDPOINT": " https://agent.example/chat ", "AGENT_MODEL": " agent-model ",
		"AGENT_MODEL_HTTP_TIMEOUT": "40s", "AGENT_MODEL_MAX_OUTPUT_TOKENS": "4096", "AGENT_MODEL_TEMPERATURE": "0.25",
		"AGENT_MODEL_MAX_CONCURRENCY": "12", "AGENT_MODEL_MAX_CONCURRENCY_PER_OWNER": "3",
		"AGENT_MODEL_CAPACITY_LEASE_TTL": "50s", "AGENT_MODEL_CAPACITY_WAIT_TIMEOUT": "3s",
		"AGENT_MAX_STEPS": "4", "AGENT_MAX_TOTAL_TOKENS": "5000", "AGENT_RUN_TIMEOUT": "2m10s",
		"AGENT_MAX_QUEUED_PER_OWNER": "6", "AGENT_MAX_QUEUED_GLOBAL": "600",
		"AGENT_OWNER_IN_FLIGHT_LIMIT": "1", "AGENT_OWNER_BORROWED_LIMIT": "1", "AGENT_STARVATION_THRESHOLD": "45s",
		"AGENT_WORKER_CONCURRENCY": "12", "AGENT_POLL_INTERVAL": "2s", "AGENT_MODEL_TIMEOUT": "35s",
		"AGENT_TOOL_TIMEOUT": "20s", "AGENT_RETRY_DELAY": "3s", "AGENT_RECOVERY_BATCH_SIZE": "50",
		"AGENT_WORKER_ID": " worker-1 ", "AGENT_LEASE_DURATION": "70s", "AGENT_HEARTBEAT_INTERVAL": "20s",
		"CONVERSATION_HISTORY_MAX_TURNS": "8", "CONVERSATION_HISTORY_MAX_TOKENS": "4000",
		"ANSWER_INPUT_TOKEN_BUDGET": "18000", "ANSWER_ENABLED": "not-a-boolean",
	}
	for name, value := range values {
		t.Setenv(name, value)
	}
	config, err := LoadAgent()
	if err != nil {
		t.Fatalf("LoadAgent() error = %v", err)
	}
	if !config.Enabled || !config.WorkerEnabled || config.APIKey != "agent-secret" ||
		config.Endpoint != "https://agent.example/chat" || config.ModelName != "agent-model" ||
		config.ModelHTTPTimeout != 40*time.Second || config.ModelMaxOutputTokens != 4096 ||
		config.ModelTemperature != 0.25 || config.ModelMaxConcurrency != 12 ||
		config.ModelMaxConcurrencyPerOwner != 3 || config.ModelCapacityLeaseTTL != 50*time.Second ||
		config.ModelCapacityWaitTimeout != 3*time.Second || config.MaxSteps != 4 || config.MaxTotalTokens != 5000 ||
		config.RunTimeout != 130*time.Second || config.MaxQueuedPerOwner != 6 || config.MaxQueuedGlobal != 600 ||
		config.StarvationThreshold != 45*time.Second || config.WorkerConcurrency != 12 ||
		config.PollInterval != 2*time.Second || config.ModelTimeout != 35*time.Second ||
		config.ToolTimeout != 20*time.Second || config.RetryDelay != 3*time.Second ||
		config.RecoveryBatchSize != 50 || config.WorkerID != "worker-1" ||
		config.LeaseDuration != 70*time.Second || config.HeartbeatInterval != 20*time.Second ||
		config.ConversationHistoryMaxTurns != 8 || config.ConversationHistoryMaxTokens != 4000 ||
		config.InputTokenBudget != 18000 {
		t.Fatalf("environment Agent config = %+v", config)
	}
}

func TestLoadAgentRejectsInvalidScalarValuesFailClosed(t *testing.T) {
	for _, test := range []struct {
		name  string
		key   string
		value string
	}{
		{name: "API flag", key: "AGENT_ENABLED", value: "sometimes"},
		{name: "Worker flag", key: "AGENT_WORKER_ENABLED", value: "sometimes"},
		{name: "HTTP timeout", key: "AGENT_MODEL_HTTP_TIMEOUT", value: "0s"},
		{name: "output zero", key: "AGENT_MODEL_MAX_OUTPUT_TOKENS", value: "0"},
		{name: "output too high", key: "AGENT_MODEL_MAX_OUTPUT_TOKENS", value: "8193"},
		{name: "temperature NaN", key: "AGENT_MODEL_TEMPERATURE", value: "NaN"},
		{name: "temperature infinity", key: "AGENT_MODEL_TEMPERATURE", value: "+Inf"},
		{name: "temperature high", key: "AGENT_MODEL_TEMPERATURE", value: "2.1"},
		{name: "model concurrency", key: "AGENT_MODEL_MAX_CONCURRENCY", value: "129"},
		{name: "owner concurrency", key: "AGENT_MODEL_MAX_CONCURRENCY_PER_OWNER", value: "17"},
		{name: "capacity TTL", key: "AGENT_MODEL_CAPACITY_LEASE_TTL", value: "0s"},
		{name: "capacity wait", key: "AGENT_MODEL_CAPACITY_WAIT_TIMEOUT", value: "0s"},
		{name: "steps", key: "AGENT_MAX_STEPS", value: "6"},
		{name: "tokens", key: "AGENT_MAX_TOTAL_TOKENS", value: "48001"},
		{name: "run timeout", key: "AGENT_RUN_TIMEOUT", value: "0s"},
		{name: "owner queue", key: "AGENT_MAX_QUEUED_PER_OWNER", value: "101"},
		{name: "global queue", key: "AGENT_MAX_QUEUED_GLOBAL", value: "10001"},
		{name: "base scheduling", key: "AGENT_OWNER_IN_FLIGHT_LIMIT", value: "0"},
		{name: "borrowed scheduling", key: "AGENT_OWNER_BORROWED_LIMIT", value: "0"},
		{name: "starvation", key: "AGENT_STARVATION_THRESHOLD", value: "0s"},
		{name: "Worker concurrency", key: "AGENT_WORKER_CONCURRENCY", value: "33"},
		{name: "poll", key: "AGENT_POLL_INTERVAL", value: "0s"},
		{name: "model timeout", key: "AGENT_MODEL_TIMEOUT", value: "0s"},
		{name: "tool timeout", key: "AGENT_TOOL_TIMEOUT", value: "0s"},
		{name: "retry", key: "AGENT_RETRY_DELAY", value: "0s"},
		{name: "recovery", key: "AGENT_RECOVERY_BATCH_SIZE", value: "101"},
		{name: "lease", key: "AGENT_LEASE_DURATION", value: "0s"},
		{name: "heartbeat", key: "AGENT_HEARTBEAT_INTERVAL", value: "0s"},
		{name: "history turns", key: "CONVERSATION_HISTORY_MAX_TURNS", value: "17"},
		{name: "history tokens", key: "CONVERSATION_HISTORY_MAX_TOKENS", value: "16001"},
		{name: "input budget", key: "ANSWER_INPUT_TOKEN_BUDGET", value: "131073"},
	} {
		t.Run(test.name, func(t *testing.T) {
			clearAgentEnvironment(t)
			t.Setenv(test.key, test.value)
			config, err := LoadAgent()
			if err == nil || config != (AgentConfig{}) {
				t.Fatalf("LoadAgent(%s=%q) = %+v, %v", test.key, test.value, config, err)
			}
		})
	}
}

func TestLoadAgentRejectsCrossFieldRelations(t *testing.T) {
	for _, test := range []struct {
		name   string
		values map[string]string
		want   error
	}{
		{name: "provider owner above global", values: map[string]string{"AGENT_MODEL_MAX_CONCURRENCY": "1", "AGENT_MODEL_MAX_CONCURRENCY_PER_OWNER": "2"}, want: ErrInvalidAgentModelCapacity},
		{name: "owner queue above global", values: map[string]string{"AGENT_MAX_QUEUED_PER_OWNER": "6", "AGENT_MAX_QUEUED_GLOBAL": "5"}, want: ErrInvalidAgentQueueCapacity},
		{name: "base scheduling not one", values: map[string]string{"AGENT_OWNER_IN_FLIGHT_LIMIT": "2"}, want: ErrInvalidAgentSchedulingPolicy},
		{name: "borrowed scheduling not one", values: map[string]string{"AGENT_OWNER_BORROWED_LIMIT": "2"}, want: ErrInvalidAgentSchedulingPolicy},
		{name: "model timeout equals capacity TTL", values: map[string]string{"AGENT_MODEL_TIMEOUT": "45s"}, want: ErrInvalidAgentRuntimeTiming},
		{name: "capacity TTL above lease", values: map[string]string{"AGENT_MODEL_CAPACITY_LEASE_TTL": "61s"}, want: ErrInvalidAgentRuntimeTiming},
		{name: "lease equals Run timeout", values: map[string]string{"AGENT_LEASE_DURATION": "120s"}, want: ErrInvalidAgentRuntimeTiming},
		{name: "heartbeat equals lease", values: map[string]string{"AGENT_HEARTBEAT_INTERVAL": "60s"}, want: ErrInvalidAgentRuntimeTiming},
		{name: "output equals total budget", values: map[string]string{"AGENT_MODEL_MAX_OUTPUT_TOKENS": "2048", "AGENT_MAX_TOTAL_TOKENS": "2048"}, want: ErrInvalidAgentOutputBudget},
		{name: "history leaves too little space", values: map[string]string{"CONVERSATION_HISTORY_MAX_TOKENS": "3000", "ANSWER_INPUT_TOKEN_BUDGET": "4999"}, want: ErrInvalidAgentHistoryBudget},
	} {
		t.Run(test.name, func(t *testing.T) {
			clearAgentEnvironment(t)
			for name, value := range test.values {
				t.Setenv(name, value)
			}
			config, err := LoadAgent()
			if !errors.Is(err, test.want) || config != (AgentConfig{}) {
				t.Fatalf("LoadAgent() = %+v, %v want %v", config, err, test.want)
			}
		})
	}
}

func TestAgentConfigDoesNotLeakSensitiveKeyInErrors(t *testing.T) {
	clearAgentEnvironment(t)
	const secret = "agent-api-key-secret-marker"
	t.Setenv("AGENT_WORKER_ENABLED", "true")
	t.Setenv("DASHSCOPE_API_KEY", secret)
	t.Setenv("AGENT_MODEL_TIMEOUT", "45s")
	config, err := LoadAgent()
	if !errors.Is(err, ErrInvalidAgentRuntimeTiming) || config != (AgentConfig{}) || strings.Contains(err.Error(), secret) {
		t.Fatalf("LoadAgent() = %+v, %v", config, err)
	}
}

func TestAgentModelCapacityConfigRejectsInvalidSharedRetry(t *testing.T) {
	clearAgentEnvironment(t)
	config, err := LoadAgent()
	if err != nil {
		t.Fatalf("LoadAgent() error = %v", err)
	}
	if got, err := config.ModelCapacityConfig(0); !errors.Is(err, ErrInvalidAgentModelCapacity) || got != (agentapplication.ModelCapacityConfig{}) {
		t.Fatalf("ModelCapacityConfig(0) = %+v, %v", got, err)
	}
}

func clearAgentEnvironment(t *testing.T) {
	t.Helper()
	for _, name := range []string{
		"AGENT_ENABLED", "AGENT_WORKER_ENABLED", "DASHSCOPE_API_KEY", "DASHSCOPE_AGENT_ENDPOINT", "AGENT_MODEL",
		"AGENT_MODEL_HTTP_TIMEOUT", "AGENT_MODEL_MAX_OUTPUT_TOKENS", "AGENT_MODEL_TEMPERATURE",
		"AGENT_MODEL_MAX_CONCURRENCY", "AGENT_MODEL_MAX_CONCURRENCY_PER_OWNER",
		"AGENT_MODEL_CAPACITY_LEASE_TTL", "AGENT_MODEL_CAPACITY_WAIT_TIMEOUT",
		"AGENT_MAX_STEPS", "AGENT_MAX_TOTAL_TOKENS", "AGENT_RUN_TIMEOUT",
		"AGENT_MAX_QUEUED_PER_OWNER", "AGENT_MAX_QUEUED_GLOBAL", "AGENT_OWNER_IN_FLIGHT_LIMIT",
		"AGENT_OWNER_BORROWED_LIMIT", "AGENT_STARVATION_THRESHOLD", "AGENT_WORKER_CONCURRENCY",
		"AGENT_POLL_INTERVAL", "AGENT_MODEL_TIMEOUT", "AGENT_TOOL_TIMEOUT", "AGENT_RETRY_DELAY",
		"AGENT_RECOVERY_BATCH_SIZE", "AGENT_WORKER_ID", "AGENT_LEASE_DURATION", "AGENT_HEARTBEAT_INTERVAL",
		"CONVERSATION_HISTORY_MAX_TURNS", "CONVERSATION_HISTORY_MAX_TOKENS", "ANSWER_INPUT_TOKEN_BUDGET",
		"ANSWER_ENABLED",
	} {
		t.Setenv(name, "")
	}
}
