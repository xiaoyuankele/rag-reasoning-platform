package main

import (
	"testing"
	"time"

	"rag-reasoning-platform/backend/internal/config"
)

// TestNewEmbeddingClientSupportsConfiguredProviders 只验证组合选择和构造，
// 不发送真实 HTTP 请求，因此不会访问远程服务或产生费用。
func TestNewEmbeddingClientSupportsConfiguredProviders(t *testing.T) {
	tests := []struct {
		name     string
		provider config.EmbeddingProvider
	}{
		{name: "DashScope", provider: config.EmbeddingProviderDashScope},
		{name: "OpenAI", provider: config.EmbeddingProviderOpenAI},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			client, err := newEmbeddingClient(config.EmbeddingConfig{
				Provider:    test.provider,
				APIKey:      "test-api-key",
				Endpoint:    "https://example.com/v1/embeddings",
				HTTPTimeout: time.Second,
			})
			if err != nil {
				t.Fatalf("newEmbeddingClient() error = %v, want nil", err)
			}
			if client == nil {
				t.Fatal("newEmbeddingClient() client = nil, want implementation")
			}
		})
	}
}

func TestNewEmbeddingClientRejectsUnsupportedProvider(t *testing.T) {
	client, err := newEmbeddingClient(config.EmbeddingConfig{
		Provider:    config.EmbeddingProvider("unsupported"),
		APIKey:      "test-api-key",
		Endpoint:    "https://example.com/v1/embeddings",
		HTTPTimeout: time.Second,
	})
	if err == nil {
		t.Fatal("newEmbeddingClient() error = nil, want unsupported provider error")
	}
	if client != nil {
		t.Fatal("newEmbeddingClient() client must be nil after failure")
	}
}
