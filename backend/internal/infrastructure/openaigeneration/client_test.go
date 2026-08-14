package openaigeneration

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"testing"

	generationdomain "rag-reasoning-platform/backend/internal/domain/generation"
)

func TestClientGenerateSendsContractAndReturnsAnswer(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(
		response http.ResponseWriter,
		request *http.Request,
	) {
		if request.Method != http.MethodPost {
			t.Fatalf("method = %s, want POST", request.Method)
		}
		if request.URL.Path != "/v1/chat/completions" {
			t.Fatalf("path = %s, want /v1/chat/completions", request.URL.Path)
		}
		if request.Header.Get("Authorization") != "Bearer test-api-key" {
			t.Fatal("Authorization header does not contain expected bearer token")
		}

		var payload chatCompletionRequest
		if err := json.NewDecoder(request.Body).Decode(&payload); err != nil {
			t.Fatalf("decode request: %v", err)
		}
		if payload.Model != "test-model" || payload.Stream ||
			payload.MaxCompletionTokens != 256 || payload.Temperature != 0.1 {
			t.Fatalf("request payload = %+v, want configured generation values", payload)
		}
		if len(payload.Messages) != 2 ||
			payload.Messages[0].Role != "system" ||
			payload.Messages[0].Content != "answer from evidence" ||
			payload.Messages[1].Role != "user" ||
			payload.Messages[1].Content != "question and sources" {
			t.Fatalf("messages = %+v, want system and user messages", payload.Messages)
		}

		response.Header().Set("Content-Type", "application/json")
		_, _ = response.Write([]byte(`{
			"choices": [{
				"message": {"role": "assistant", "content": " evidence answer [1] "},
				"finish_reason": "stop"
			}],
			"usage": {
				"prompt_tokens": 30,
				"completion_tokens": 8,
				"total_tokens": 38
			}
		}`))
	}))
	defer server.Close()

	client, err := NewClient(
		" test-api-key ",
		server.URL+"/v1/chat/completions",
		server.Client(),
	)
	if err != nil {
		t.Fatalf("NewClient() error = %v, want nil", err)
	}

	result, err := client.Generate(
		context.Background(),
		generationdomain.GenerateRequest{
			SystemInstruction: "answer from evidence",
			UserPrompt:        "question and sources",
			Model:             "test-model",
			MaxOutputTokens:   256,
			Temperature:       0.1,
		},
	)
	if err != nil {
		t.Fatalf("Generate() error = %v, want nil", err)
	}
	if result.Text != "evidence answer [1]" {
		t.Fatalf("Text = %q, want trimmed answer", result.Text)
	}
	if result.PromptTokens != 30 ||
		result.CompletionTokens != 8 ||
		result.TotalTokens != 38 {
		t.Fatalf("usage = %+v, want provider token usage", result)
	}
}

func TestClientGenerateMapsProviderErrors(t *testing.T) {
	testCases := []struct {
		name       string
		statusCode int
		body       string
		wantError  error
	}{
		{name: "bad request", statusCode: http.StatusBadRequest, body: `{"error":{"message":"bad input"}}`, wantError: generationdomain.ErrGenerationRequestRejected},
		{name: "unauthorized", statusCode: http.StatusUnauthorized, body: `{"error":{"message":"bad key"}}`, wantError: generationdomain.ErrGenerationAuthentication},
		{name: "rate limited", statusCode: http.StatusTooManyRequests, body: `{"error":{"message":"slow down"}}`, wantError: generationdomain.ErrGenerationRateLimited},
		{name: "quota exceeded", statusCode: http.StatusTooManyRequests, body: `{"error":{"message":"no quota","code":"Arrearage"}}`, wantError: generationdomain.ErrGenerationQuotaExceeded},
		{name: "server unavailable", statusCode: http.StatusServiceUnavailable, body: `{"error":{"message":"later"}}`, wantError: generationdomain.ErrGenerationUnavailable},
	}

	for _, testCase := range testCases {
		t.Run(testCase.name, func(t *testing.T) {
			server := httptest.NewServer(http.HandlerFunc(func(
				response http.ResponseWriter,
				_ *http.Request,
			) {
				response.WriteHeader(testCase.statusCode)
				_, _ = response.Write([]byte(testCase.body))
			}))
			defer server.Close()

			client, err := NewClient("test-key", server.URL, server.Client())
			if err != nil {
				t.Fatalf("NewClient() error = %v, want nil", err)
			}

			_, err = client.Generate(
				context.Background(),
				validGenerateRequest(),
			)
			if !errors.Is(err, testCase.wantError) {
				t.Fatalf("Generate() error = %v, want %v", err, testCase.wantError)
			}
		})
	}
}

func TestClientGenerateRejectsInvalidResponses(t *testing.T) {
	for _, testCase := range []struct {
		name string
		body string
	}{
		{name: "invalid JSON", body: `{`},
		{name: "missing choices", body: `{"choices":[]}`},
		{name: "blank answer", body: `{"choices":[{"message":{"content":"  "}}]}`},
	} {
		t.Run(testCase.name, func(t *testing.T) {
			server := httptest.NewServer(http.HandlerFunc(func(
				response http.ResponseWriter,
				_ *http.Request,
			) {
				_, _ = response.Write([]byte(testCase.body))
			}))
			defer server.Close()

			client, err := NewClient("test-key", server.URL, server.Client())
			if err != nil {
				t.Fatalf("NewClient() error = %v, want nil", err)
			}

			_, err = client.Generate(context.Background(), validGenerateRequest())
			if !errors.Is(err, generationdomain.ErrInvalidGenerationResponse) {
				t.Fatalf(
					"Generate() error = %v, want ErrInvalidGenerationResponse",
					err,
				)
			}
		})
	}
}

func TestClientGenerateRejectsInvalidRequestWithoutHTTP(t *testing.T) {
	requestCount := 0
	server := httptest.NewServer(http.HandlerFunc(func(
		_ http.ResponseWriter,
		_ *http.Request,
	) {
		requestCount++
	}))
	defer server.Close()

	client, err := NewClient("test-key", server.URL, server.Client())
	if err != nil {
		t.Fatalf("NewClient() error = %v, want nil", err)
	}

	invalidRequest := validGenerateRequest()
	invalidRequest.UserPrompt = " "
	_, err = client.Generate(context.Background(), invalidRequest)
	if !errors.Is(err, generationdomain.ErrGenerationRequestRejected) {
		t.Fatalf("Generate() error = %v, want request rejected", err)
	}
	if requestCount != 0 {
		t.Fatalf("request count = %d, want 0", requestCount)
	}
}

func TestNewCompatibleClientValidatesDependencies(t *testing.T) {
	testCases := []struct {
		name         string
		providerName string
		apiKey       string
		endpoint     string
		httpClient   HTTPDoer
	}{
		{name: "missing provider", apiKey: "key", endpoint: DefaultEndpoint, httpClient: http.DefaultClient},
		{name: "missing API key", providerName: "Provider", endpoint: DefaultEndpoint, httpClient: http.DefaultClient},
		{name: "invalid endpoint", providerName: "Provider", apiKey: "key", endpoint: "not-a-url", httpClient: http.DefaultClient},
		{name: "missing HTTP client", providerName: "Provider", apiKey: "key", endpoint: DefaultEndpoint},
	}

	for _, testCase := range testCases {
		t.Run(testCase.name, func(t *testing.T) {
			client, err := NewCompatibleClient(
				testCase.providerName,
				testCase.apiKey,
				testCase.endpoint,
				testCase.httpClient,
			)
			if err == nil || client != nil {
				t.Fatalf("NewCompatibleClient() = (%v, %v), want (nil, error)", client, err)
			}
		})
	}
}

func validGenerateRequest() generationdomain.GenerateRequest {
	return generationdomain.GenerateRequest{
		SystemInstruction: "answer from evidence",
		UserPrompt:        "question and sources",
		Model:             "test-model",
		MaxOutputTokens:   256,
		Temperature:       0.1,
	}
}
