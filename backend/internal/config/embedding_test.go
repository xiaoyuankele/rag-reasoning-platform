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
		embeddingConfig.RetryMaxDelay != defaultEmbeddingRetryMaxDelay {
		t.Fatalf("default worker configuration was not loaded")
	}
}

func TestLoadEmbeddingUsesOpenAIEnvironment(t *testing.T) {
	clearEmbeddingEnvironment(t)
	t.Setenv("EMBEDDING_WORKER_ENABLED", "true")
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

	embeddingConfig, err := LoadEmbedding()
	if err != nil {
		t.Fatalf("LoadEmbedding() error = %v, want nil", err)
	}

	if !embeddingConfig.WorkerEnabled ||
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
		embeddingConfig.RetryMaxDelay != time.Minute {
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

func clearEmbeddingEnvironment(t *testing.T) {
	t.Helper()

	for _, name := range []string{
		"EMBEDDING_WORKER_ENABLED",
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
	} {
		t.Setenv(name, "")
	}
}
