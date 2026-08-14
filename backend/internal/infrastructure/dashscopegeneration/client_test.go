package dashscopegeneration

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	generationdomain "rag-reasoning-platform/backend/internal/domain/generation"
)

// TestClientSendsDashScopeThinkingOption 验证非 OpenAI 标准字段只由
// DashScope 适配器显式加入请求体，并保持为顶层布尔值。
func TestClientSendsDashScopeThinkingOption(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(
		response http.ResponseWriter,
		request *http.Request,
	) {
		var payload map[string]any
		if err := json.NewDecoder(request.Body).Decode(&payload); err != nil {
			t.Fatalf("decode request: %v", err)
		}
		if enableThinking, ok := payload["enable_thinking"].(bool); !ok || enableThinking {
			t.Fatalf(
				"enable_thinking = %#v, want boolean false",
				payload["enable_thinking"],
			)
		}

		response.Header().Set("Content-Type", "application/json")
		_, _ = response.Write([]byte(`{
			"choices":[{"message":{"content":"answer [1]"}}],
			"usage":{"prompt_tokens":10,"completion_tokens":3,"total_tokens":13}
		}`))
	}))
	defer server.Close()

	client, err := NewClient(
		"test-api-key",
		server.URL,
		server.Client(),
		false,
	)
	if err != nil {
		t.Fatalf("NewClient() error = %v, want nil", err)
	}

	result, err := client.Generate(
		context.Background(),
		generationdomain.GenerateRequest{
			SystemInstruction: "answer from evidence",
			UserPrompt:        "question and sources",
			Model:             "qwen3.6-flash",
			MaxOutputTokens:   256,
			Temperature:       0.1,
		},
	)
	if err != nil {
		t.Fatalf("Generate() error = %v, want nil", err)
	}
	if result.Text != "answer [1]" {
		t.Fatalf("Text = %q, want answer [1]", result.Text)
	}
}
