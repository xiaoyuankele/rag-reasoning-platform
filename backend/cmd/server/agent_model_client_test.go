package main

import (
	"testing"
	"time"

	"rag-reasoning-platform/backend/internal/config"
)

func TestNewAgentModelClientBuildsWithoutCallingProvider(t *testing.T) {
	client, err := newAgentModelClient(config.AgentConfig{
		APIKey: "fake-agent-key", Endpoint: "http://127.0.0.1:1/v1/chat/completions",
		ModelName: "fake-agent-model", ModelHTTPTimeout: 35 * time.Second,
		ModelMaxOutputTokens: 128, ModelTemperature: 0.1,
	})
	if err != nil || client == nil {
		t.Fatalf("newAgentModelClient() = %T, %v", client, err)
	}
}

func TestNewAgentModelClientRejectsMissingKey(t *testing.T) {
	if client, err := newAgentModelClient(config.AgentConfig{
		Endpoint: "http://127.0.0.1:1/v1/chat/completions", ModelName: "fake",
		ModelHTTPTimeout: 35 * time.Second, ModelMaxOutputTokens: 128,
	}); err == nil || client != nil {
		t.Fatalf("newAgentModelClient() = %T, %v", client, err)
	}
}
