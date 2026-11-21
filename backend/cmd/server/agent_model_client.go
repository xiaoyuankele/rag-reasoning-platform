package main

import (
	"fmt"
	"net/http"

	agentapplication "rag-reasoning-platform/backend/internal/application/agent"
	"rag-reasoning-platform/backend/internal/config"
	"rag-reasoning-platform/backend/internal/infrastructure/dashscopeagent"
)

type agentModelFactory func(config.AgentConfig) (agentapplication.AgentModel, error)

func newAgentModelClient(agentConfig config.AgentConfig) (agentapplication.AgentModel, error) {
	httpClient := &http.Client{Timeout: agentConfig.ModelHTTPTimeout}
	client, err := dashscopeagent.NewClient(
		agentConfig.APIKey,
		agentConfig.Endpoint,
		httpClient,
		agentConfig.ModelName,
		agentConfig.ModelMaxOutputTokens,
		agentConfig.ModelTemperature,
	)
	if err != nil {
		return nil, fmt.Errorf("create DashScope Agent model client: %w", err)
	}
	return client, nil
}
