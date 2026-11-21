package dashscopeagent

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	agentapplication "rag-reasoning-platform/backend/internal/application/agent"
)

func TestClientForcesDashScopeThinkingOff(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(response http.ResponseWriter, request *http.Request) {
		if request.Header.Get("Authorization") != "Bearer dashscope-key" {
			t.Fatalf("Authorization = %q", request.Header.Get("Authorization"))
		}
		var payload map[string]any
		if err := json.NewDecoder(request.Body).Decode(&payload); err != nil {
			t.Fatalf("decode request: %v", err)
		}
		if thinking, ok := payload["enable_thinking"].(bool); !ok || thinking {
			t.Fatalf("enable_thinking = %#v, want false", payload["enable_thinking"])
		}
		if payload["tool_choice"] != "auto" || payload["parallel_tool_calls"] != false {
			t.Fatalf("tool options = choice:%#v parallel:%#v", payload["tool_choice"], payload["parallel_tool_calls"])
		}
		_, _ = response.Write([]byte(`{
			"choices":[{"message":{"role":"assistant","content":"{\"text\":\"done\",\"evidence_refs\":[\"step-1:item-1\"]}"},"finish_reason":"stop"}],
			"usage":{"prompt_tokens":5,"completion_tokens":2,"total_tokens":7}
		}`))
	}))
	defer server.Close()
	client, err := NewClient(" dashscope-key ", server.URL, server.Client(), "qwen-plus", 64, 0.1)
	if err != nil {
		t.Fatalf("NewClient() error = %v", err)
	}
	decision, err := client.Decide(t.Context(), dashScopeModelRequest())
	if err != nil || decision.Final == nil || decision.Final.Text != "done" {
		t.Fatalf("Decide() = %+v, %v", decision, err)
	}
}

func dashScopeModelRequest() agentapplication.ModelRequest {
	return agentapplication.ModelRequest{
		RunID: 1,
		Context: agentapplication.AgentContext{
			SystemInstruction: "Use evidence.", CurrentQuestion: "question",
		},
		Tools: []agentapplication.ToolDefinition{
			{Name: agentapplication.ToolSearchDocuments, Description: "search", Parameters: json.RawMessage(`{"type":"object"}`)},
			{Name: agentapplication.ToolReadDocumentEvidence, Description: "read", Parameters: json.RawMessage(`{"type":"object"}`)},
		},
		Remaining: agentapplication.RemainingBudget{
			Steps: 5, Tokens: 100, DeadlineAt: time.Now().Add(time.Minute),
		},
	}
}
