package main

import (
	"testing"
	"time"

	"rag-reasoning-platform/backend/internal/config"
)

// TestNewGenerationClient 验证组合根能创建 Generator，但不发送真实 HTTP 请求。
func TestNewGenerationClient(t *testing.T) {
	client, err := newGenerationClient(config.GenerationConfig{
		APIKey:      "test-api-key",
		Endpoint:    "https://example.com/v1/chat/completions",
		HTTPTimeout: time.Second,
	})
	if err != nil {
		t.Fatalf("newGenerationClient() error = %v, want nil", err)
	}
	if client == nil {
		t.Fatal("newGenerationClient() client = nil, want implementation")
	}
}

func TestNewGenerationClientRejectsInvalidConfiguration(t *testing.T) {
	client, err := newGenerationClient(config.GenerationConfig{
		APIKey:      "",
		Endpoint:    "not-a-url",
		HTTPTimeout: time.Second,
	})
	if err == nil {
		t.Fatal("newGenerationClient() error = nil, want configuration error")
	}
	if client != nil {
		t.Fatal("newGenerationClient() client must be nil after failure")
	}
}
