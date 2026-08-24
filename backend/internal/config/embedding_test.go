package config

import (
	"errors"
	"testing"
	"time"
)

func TestLoadEmbeddingUsesDefaults(t *testing.T) {
	clearEmbeddingEnvironment(t)

	embeddingConfig, err := LoadEmbedding()
	if err != nil {
		t.Fatalf("LoadEmbedding() error = %v, want nil", err)
	}

	if embeddingConfig.WorkerEnabled {
		t.Fatal("WorkerEnabled = true, want false by default")
	}
	if embeddingConfig.WorkerConcurrency != defaultEmbeddingWorkerConcurrency {
		t.Fatalf(
			"WorkerConcurrency = %d, want %d",
			embeddingConfig.WorkerConcurrency,
			defaultEmbeddingWorkerConcurrency,
		)
	}
	if embeddingConfig.ProviderMaxConcurrency != defaultEmbeddingProviderConcurrency ||
		embeddingConfig.WorkerProviderConcurrency != defaultEmbeddingWorkerProviderConcurrency ||
		embeddingConfig.OnlineProviderConcurrency != defaultEmbeddingOnlineProviderConcurrency ||
		embeddingConfig.OnlineQueueWaitTimeout != defaultEmbeddingOnlineWaitTimeout {
		t.Fatalf(
			"provider gate config = %d/%d/%d/%s, want %d/%d/%d/%s",
			embeddingConfig.ProviderMaxConcurrency,
			embeddingConfig.WorkerProviderConcurrency,
			embeddingConfig.OnlineProviderConcurrency,
			embeddingConfig.OnlineQueueWaitTimeout,
			defaultEmbeddingProviderConcurrency,
			defaultEmbeddingWorkerProviderConcurrency,
			defaultEmbeddingOnlineProviderConcurrency,
			defaultEmbeddingOnlineWaitTimeout,
		)
	}
	if embeddingConfig.SemanticSearchEnabled {
		t.Fatal("SemanticSearchEnabled = true, want false by default")
	}
	if embeddingConfig.APIKey != "" {
		t.Fatal("APIKey must be empty when it was not configured")
	}
	if embeddingConfig.Provider != EmbeddingProviderDashScope ||
		embeddingConfig.Endpoint != defaultDashScopeEmbeddingEndpoint ||
		embeddingConfig.ModelName != defaultDashScopeEmbeddingModel ||
		embeddingConfig.Dimensions != defaultEmbeddingDimensions {
		t.Fatalf("default model configuration was not loaded")
	}
	if embeddingConfig.BatchSize != defaultDashScopeEmbeddingBatchSize ||
		embeddingConfig.HTTPTimeout != defaultEmbeddingHTTPTimeout ||
		embeddingConfig.ProcessingTimeout != defaultEmbeddingProcessingTimeout ||
		embeddingConfig.PollInterval != defaultEmbeddingPollInterval ||
		embeddingConfig.MaxAttempts != defaultEmbeddingMaxAttempts ||
		embeddingConfig.RetryBaseDelay != defaultEmbeddingRetryBaseDelay ||
		embeddingConfig.RetryMaxDelay != defaultEmbeddingRetryMaxDelay ||
		embeddingConfig.ActiveJobsPerUserLimit != defaultEmbeddingActiveOwnerLimit ||
		embeddingConfig.ActiveJobsGlobalLimit != defaultEmbeddingActiveGlobalLimit ||
		embeddingConfig.OwnerInFlightLimit != defaultEmbeddingOwnerInFlightLimit ||
		embeddingConfig.OwnerBorrowedLimit != defaultEmbeddingOwnerBorrowedLimit ||
		embeddingConfig.StarvationThreshold != defaultEmbeddingStarvationThreshold {
		t.Fatalf("default worker configuration was not loaded")
	}
}

func TestLoadEmbeddingUsesOpenAIEnvironment(t *testing.T) {
	clearEmbeddingEnvironment(t)
	t.Setenv("EMBEDDING_WORKER_ENABLED", "true")
	t.Setenv("EMBEDDING_WORKER_CONCURRENCY", "2")
	t.Setenv("EMBEDDING_PROVIDER_MAX_CONCURRENCY", "6")
	t.Setenv("EMBEDDING_WORKER_PROVIDER_CONCURRENCY", "2")
	t.Setenv("EMBEDDING_ONLINE_PROVIDER_CONCURRENCY", "3")
	t.Setenv("EMBEDDING_ONLINE_QUEUE_WAIT_TIMEOUT", "750ms")
	t.Setenv("EMBEDDING_PROVIDER", " OPENAI ")
	t.Setenv("OPENAI_API_KEY", " test-api-key ")
	t.Setenv("OPENAI_EMBEDDING_ENDPOINT", " https://example.com/v1/embeddings ")
	t.Setenv("EMBEDDING_MODEL", " custom-embedding-model ")
	t.Setenv("EMBEDDING_DIMENSIONS", " 1536 ")
	t.Setenv("EMBEDDING_BATCH_SIZE", "16")
	t.Setenv("EMBEDDING_HTTP_TIMEOUT", "20s")
	t.Setenv("EMBEDDING_PROCESSING_TIMEOUT", "3m")
	t.Setenv("EMBEDDING_POLL_INTERVAL", "500ms")
	t.Setenv("EMBEDDING_MAX_ATTEMPTS", "4")
	t.Setenv("EMBEDDING_RETRY_BASE_DELAY", "3s")
	t.Setenv("EMBEDDING_RETRY_MAX_DELAY", "1m")
	t.Setenv("EMBEDDING_MAX_ACTIVE_JOBS_PER_USER", "25")
	t.Setenv("EMBEDDING_MAX_ACTIVE_JOBS_GLOBAL", "200")
	t.Setenv("EMBEDDING_MAX_IN_FLIGHT_PER_OWNER", "2")
	t.Setenv("EMBEDDING_MAX_BORROWED_IN_FLIGHT_PER_OWNER", "3")
	t.Setenv("EMBEDDING_STARVATION_THRESHOLD", "90s")

	embeddingConfig, err := LoadEmbedding()
	if err != nil {
		t.Fatalf("LoadEmbedding() error = %v, want nil", err)
	}

	if !embeddingConfig.WorkerEnabled ||
		embeddingConfig.WorkerConcurrency != 2 ||
		embeddingConfig.ProviderMaxConcurrency != 6 ||
		embeddingConfig.WorkerProviderConcurrency != 2 ||
		embeddingConfig.OnlineProviderConcurrency != 3 ||
		embeddingConfig.OnlineQueueWaitTimeout != 750*time.Millisecond ||
		embeddingConfig.Provider != EmbeddingProviderOpenAI ||
		embeddingConfig.APIKey != "test-api-key" ||
		embeddingConfig.Endpoint != "https://example.com/v1/embeddings" ||
		embeddingConfig.ModelName != "custom-embedding-model" ||
		embeddingConfig.Dimensions != 1536 ||
		embeddingConfig.BatchSize != 16 ||
		embeddingConfig.HTTPTimeout != 20*time.Second ||
		embeddingConfig.ProcessingTimeout != 3*time.Minute ||
		embeddingConfig.PollInterval != 500*time.Millisecond ||
		embeddingConfig.MaxAttempts != 4 ||
		embeddingConfig.RetryBaseDelay != 3*time.Second ||
		embeddingConfig.RetryMaxDelay != time.Minute ||
		embeddingConfig.ActiveJobsPerUserLimit != 25 ||
		embeddingConfig.ActiveJobsGlobalLimit != 200 ||
		embeddingConfig.OwnerInFlightLimit != 2 ||
		embeddingConfig.OwnerBorrowedLimit != 3 ||
		embeddingConfig.StarvationThreshold != 90*time.Second {
		t.Fatal("LoadEmbedding() did not preserve configured values")
	}
}

func TestLoadEmbeddingUsesDashScopeEnvironment(t *testing.T) {
	clearEmbeddingEnvironment(t)
	t.Setenv("EMBEDDING_WORKER_ENABLED", "true")
	t.Setenv("EMBEDDING_PROVIDER", "dashscope")
	t.Setenv("DASHSCOPE_API_KEY", " dashscope-key ")
	t.Setenv(
		"DASHSCOPE_EMBEDDING_ENDPOINT",
		" https://example.com/compatible-mode/v1/embeddings ",
	)

	embeddingConfig, err := LoadEmbedding()
	if err != nil {
		t.Fatalf("LoadEmbedding() error = %v, want nil", err)
	}

	if embeddingConfig.Provider != EmbeddingProviderDashScope ||
		embeddingConfig.APIKey != "dashscope-key" ||
		embeddingConfig.Endpoint != "https://example.com/compatible-mode/v1/embeddings" ||
		embeddingConfig.ModelName != defaultDashScopeEmbeddingModel ||
		embeddingConfig.BatchSize != defaultDashScopeEmbeddingBatchSize {
		t.Fatalf("DashScope config = %+v, want provider defaults and configured credentials", embeddingConfig)
	}
}

func TestLoadEmbeddingRejectsInvalidDimensions(t *testing.T) {
	testCases := []string{"not-a-number", "0", "768", "4097"}

	for _, value := range testCases {
		t.Run(value, func(t *testing.T) {
			clearEmbeddingEnvironment(t)
			t.Setenv("EMBEDDING_DIMENSIONS", value)

			embeddingConfig, err := LoadEmbedding()
			if err == nil {
				t.Fatalf("LoadEmbedding() error = nil for EMBEDDING_DIMENSIONS=%q", value)
			}
			if embeddingConfig != (EmbeddingConfig{}) {
				t.Fatal("LoadEmbedding() must return zero config after validation failure")
			}
		})
	}
}

func TestLoadEmbeddingRequiresAPIKeyWhenRemoteCapabilityIsEnabled(t *testing.T) {
	tests := []struct {
		name        string
		environment string
	}{
		{name: "worker", environment: "EMBEDDING_WORKER_ENABLED"},
		{name: "semantic search", environment: "SEMANTIC_SEARCH_ENABLED"},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			clearEmbeddingEnvironment(t)
			t.Setenv(test.environment, "true")

			_, err := LoadEmbedding()
			if !errors.Is(err, ErrEmbeddingAPIKeyRequired) {
				t.Fatalf("LoadEmbedding() error = %v, want ErrEmbeddingAPIKeyRequired", err)
			}
		})
	}
}

func TestLoadEmbeddingEnablesSemanticSearch(t *testing.T) {
	clearEmbeddingEnvironment(t)
	t.Setenv("SEMANTIC_SEARCH_ENABLED", "true")
	t.Setenv("DASHSCOPE_API_KEY", "test-api-key")

	embeddingConfig, err := LoadEmbedding()
	if err != nil {
		t.Fatalf("LoadEmbedding() error = %v, want nil", err)
	}
	if !embeddingConfig.SemanticSearchEnabled {
		t.Fatal("SemanticSearchEnabled = false, want true")
	}
	if embeddingConfig.WorkerEnabled {
		t.Fatal("WorkerEnabled = true, want independent false value")
	}
}

func TestLoadEmbeddingRejectsUnsupportedProvider(t *testing.T) {
	clearEmbeddingEnvironment(t)
	t.Setenv("EMBEDDING_PROVIDER", "unknown-provider")

	_, err := LoadEmbedding()
	if !errors.Is(err, ErrUnsupportedEmbeddingProvider) {
		t.Fatalf("LoadEmbedding() error = %v, want ErrUnsupportedEmbeddingProvider", err)
	}
}

func TestLoadEmbeddingUsesSelectedProviderAPIKey(t *testing.T) {
	clearEmbeddingEnvironment(t)
	t.Setenv("EMBEDDING_WORKER_ENABLED", "true")
	t.Setenv("EMBEDDING_PROVIDER", "dashscope")
	t.Setenv("OPENAI_API_KEY", "openai-key-must-not-be-used")

	_, err := LoadEmbedding()
	if !errors.Is(err, ErrEmbeddingAPIKeyRequired) {
		t.Fatalf("LoadEmbedding() error = %v, want missing DashScope key", err)
	}
}

func TestLoadEmbeddingEnforcesProviderBatchLimit(t *testing.T) {
	clearEmbeddingEnvironment(t)
	t.Setenv("EMBEDDING_PROVIDER", "dashscope")
	t.Setenv("EMBEDDING_BATCH_SIZE", "11")

	if _, err := LoadEmbedding(); err == nil {
		t.Fatal("LoadEmbedding() error = nil for DashScope batch size above 10")
	}
}

func TestLoadEmbeddingRejectsInvalidWorkerEnabled(t *testing.T) {
	clearEmbeddingEnvironment(t)
	t.Setenv("EMBEDDING_WORKER_ENABLED", "sometimes")

	if _, err := LoadEmbedding(); err == nil {
		t.Fatal("LoadEmbedding() error = nil for invalid worker enabled value")
	}
}

func TestLoadEmbeddingRejectsInvalidWorkerConcurrency(t *testing.T) {
	testCases := []string{"not-a-number", "0", "5"}

	for _, value := range testCases {
		t.Run(value, func(t *testing.T) {
			clearEmbeddingEnvironment(t)
			t.Setenv("EMBEDDING_WORKER_CONCURRENCY", value)

			if _, err := LoadEmbedding(); err == nil {
				t.Fatalf(
					"LoadEmbedding() error = nil for EMBEDDING_WORKER_CONCURRENCY=%q",
					value,
				)
			}
		})
	}
}

func TestLoadEmbeddingRejectsInvalidProviderGateConfiguration(t *testing.T) {
	tests := []struct {
		name        string
		environment string
		value       string
	}{
		{name: "non-numeric concurrency", environment: "EMBEDDING_PROVIDER_MAX_CONCURRENCY", value: "many"},
		{name: "zero concurrency", environment: "EMBEDDING_PROVIDER_MAX_CONCURRENCY", value: "0"},
		{name: "concurrency above maximum", environment: "EMBEDDING_PROVIDER_MAX_CONCURRENCY", value: "33"},
		{name: "zero worker provider concurrency", environment: "EMBEDDING_WORKER_PROVIDER_CONCURRENCY", value: "0"},
		{name: "worker provider concurrency above maximum", environment: "EMBEDDING_WORKER_PROVIDER_CONCURRENCY", value: "33"},
		{name: "zero online provider concurrency", environment: "EMBEDDING_ONLINE_PROVIDER_CONCURRENCY", value: "0"},
		{name: "online provider concurrency above maximum", environment: "EMBEDDING_ONLINE_PROVIDER_CONCURRENCY", value: "33"},
		{name: "invalid wait timeout", environment: "EMBEDDING_ONLINE_QUEUE_WAIT_TIMEOUT", value: "soon"},
		{name: "zero wait timeout", environment: "EMBEDDING_ONLINE_QUEUE_WAIT_TIMEOUT", value: "0s"},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			clearEmbeddingEnvironment(t)
			t.Setenv(test.environment, test.value)
			if _, err := LoadEmbedding(); err == nil {
				t.Fatalf(
					"LoadEmbedding() error = nil for %s=%q",
					test.environment,
					test.value,
				)
			}
		})
	}
}

func TestLoadEmbeddingRejectsProviderCapacityBelowEnabledWorkerPool(t *testing.T) {
	clearEmbeddingEnvironment(t)
	t.Setenv("EMBEDDING_WORKER_ENABLED", "true")
	t.Setenv("DASHSCOPE_API_KEY", "test-api-key")
	t.Setenv("EMBEDDING_WORKER_CONCURRENCY", "3")
	t.Setenv("EMBEDDING_PROVIDER_MAX_CONCURRENCY", "5")
	t.Setenv("EMBEDDING_WORKER_PROVIDER_CONCURRENCY", "2")

	_, err := LoadEmbedding()
	if !errors.Is(err, ErrInvalidEmbeddingProviderConcurrency) {
		t.Fatalf(
			"LoadEmbedding() error = %v, want ErrInvalidEmbeddingProviderConcurrency",
			err,
		)
	}
}

func TestLoadEmbeddingRejectsClassCapacityAboveGlobalProviderCapacity(t *testing.T) {
	clearEmbeddingEnvironment(t)
	t.Setenv("EMBEDDING_PROVIDER_MAX_CONCURRENCY", "3")

	_, err := LoadEmbedding()
	if !errors.Is(err, ErrInvalidEmbeddingProviderAllocation) {
		t.Fatalf(
			"LoadEmbedding() error = %v, want ErrInvalidEmbeddingProviderAllocation",
			err,
		)
	}
}

func TestLoadEmbeddingRejectsInvalidSemanticSearchEnabled(t *testing.T) {
	clearEmbeddingEnvironment(t)
	t.Setenv("SEMANTIC_SEARCH_ENABLED", "sometimes")

	if _, err := LoadEmbedding(); err == nil {
		t.Fatal("LoadEmbedding() error = nil for invalid semantic search enabled value")
	}
}

func TestLoadEmbeddingRejectsReversedRetryDelays(t *testing.T) {
	clearEmbeddingEnvironment(t)
	t.Setenv("EMBEDDING_RETRY_BASE_DELAY", "2m")
	t.Setenv("EMBEDDING_RETRY_MAX_DELAY", "1m")

	_, err := LoadEmbedding()
	if !errors.Is(err, ErrInvalidEmbeddingRetryDelays) {
		t.Fatalf("LoadEmbedding() error = %v, want ErrInvalidEmbeddingRetryDelays", err)
	}
}

func TestLoadEmbeddingRejectsInvalidActiveJobLimits(t *testing.T) {
	t.Run("invalid values", func(t *testing.T) {
		for _, testCase := range []struct {
			name  string
			value string
		}{
			{name: "non-numeric", value: "many"},
			{name: "zero", value: "0"},
			{name: "above maximum", value: "10001"},
		} {
			t.Run(testCase.name, func(t *testing.T) {
				clearEmbeddingEnvironment(t)
				t.Setenv("EMBEDDING_MAX_ACTIVE_JOBS_PER_USER", testCase.value)
				if _, err := LoadEmbedding(); err == nil {
					t.Fatalf("LoadEmbedding() error = nil for per-user limit %q", testCase.value)
				}
			})
		}
	})

	t.Run("global smaller than per-user", func(t *testing.T) {
		clearEmbeddingEnvironment(t)
		t.Setenv("EMBEDDING_MAX_ACTIVE_JOBS_PER_USER", "20")
		t.Setenv("EMBEDDING_MAX_ACTIVE_JOBS_GLOBAL", "10")
		_, err := LoadEmbedding()
		if !errors.Is(err, ErrInvalidEmbeddingActiveJobLimits) {
			t.Fatalf("LoadEmbedding() error = %v, want ErrInvalidEmbeddingActiveJobLimits", err)
		}
	})
}

func TestLoadEmbeddingRejectsInvalidOwnerScheduling(t *testing.T) {
	t.Run("invalid individual values", func(t *testing.T) {
		testCases := []struct {
			name        string
			environment string
			value       string
		}{
			{name: "non-numeric base", environment: "EMBEDDING_MAX_IN_FLIGHT_PER_OWNER", value: "one"},
			{name: "zero base", environment: "EMBEDDING_MAX_IN_FLIGHT_PER_OWNER", value: "0"},
			{name: "base above maximum", environment: "EMBEDDING_MAX_IN_FLIGHT_PER_OWNER", value: "65"},
			{name: "non-numeric borrowed", environment: "EMBEDDING_MAX_BORROWED_IN_FLIGHT_PER_OWNER", value: "two"},
			{name: "zero borrowed", environment: "EMBEDDING_MAX_BORROWED_IN_FLIGHT_PER_OWNER", value: "0"},
			{name: "borrowed above maximum", environment: "EMBEDDING_MAX_BORROWED_IN_FLIGHT_PER_OWNER", value: "65"},
			{name: "invalid starvation threshold", environment: "EMBEDDING_STARVATION_THRESHOLD", value: "later"},
			{name: "zero starvation threshold", environment: "EMBEDDING_STARVATION_THRESHOLD", value: "0s"},
		}

		for _, testCase := range testCases {
			t.Run(testCase.name, func(t *testing.T) {
				clearEmbeddingEnvironment(t)
				t.Setenv(testCase.environment, testCase.value)
				if _, err := LoadEmbedding(); err == nil {
					t.Fatalf(
						"LoadEmbedding() error = nil for %s=%q",
						testCase.environment,
						testCase.value,
					)
				}
			})
		}
	})

	t.Run("borrowed limit below base", func(t *testing.T) {
		clearEmbeddingEnvironment(t)
		t.Setenv("EMBEDDING_MAX_IN_FLIGHT_PER_OWNER", "3")
		t.Setenv("EMBEDDING_MAX_BORROWED_IN_FLIGHT_PER_OWNER", "2")

		_, err := LoadEmbedding()
		if !errors.Is(err, ErrInvalidEmbeddingOwnerSchedulingLimits) {
			t.Fatalf(
				"LoadEmbedding() error = %v, want ErrInvalidEmbeddingOwnerSchedulingLimits",
				err,
			)
		}
	})
}

func clearEmbeddingEnvironment(t *testing.T) {
	t.Helper()

	for _, name := range []string{
		"EMBEDDING_WORKER_ENABLED",
		"EMBEDDING_WORKER_CONCURRENCY",
		"EMBEDDING_PROVIDER_MAX_CONCURRENCY",
		"EMBEDDING_WORKER_PROVIDER_CONCURRENCY",
		"EMBEDDING_ONLINE_PROVIDER_CONCURRENCY",
		"EMBEDDING_ONLINE_QUEUE_WAIT_TIMEOUT",
		"SEMANTIC_SEARCH_ENABLED",
		"EMBEDDING_PROVIDER",
		"DASHSCOPE_API_KEY",
		"DASHSCOPE_EMBEDDING_ENDPOINT",
		"OPENAI_API_KEY",
		"OPENAI_EMBEDDING_ENDPOINT",
		"EMBEDDING_MODEL",
		"EMBEDDING_DIMENSIONS",
		"EMBEDDING_BATCH_SIZE",
		"EMBEDDING_HTTP_TIMEOUT",
		"EMBEDDING_PROCESSING_TIMEOUT",
		"EMBEDDING_POLL_INTERVAL",
		"EMBEDDING_MAX_ATTEMPTS",
		"EMBEDDING_RETRY_BASE_DELAY",
		"EMBEDDING_RETRY_MAX_DELAY",
		"EMBEDDING_MAX_ACTIVE_JOBS_PER_USER",
		"EMBEDDING_MAX_ACTIVE_JOBS_GLOBAL",
		"EMBEDDING_MAX_IN_FLIGHT_PER_OWNER",
		"EMBEDDING_MAX_BORROWED_IN_FLIGHT_PER_OWNER",
		"EMBEDDING_STARVATION_THRESHOLD",
	} {
		t.Setenv(name, "")
	}
}
