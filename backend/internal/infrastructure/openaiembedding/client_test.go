package openaiembedding

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"testing"

	embeddingdomain "rag-reasoning-platform/backend/internal/domain/embedding"
)

func TestClientEmbedSendsContractAndRestoresInputOrder(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(
		response http.ResponseWriter,
		request *http.Request,
	) {
		if request.Method != http.MethodPost {
			t.Fatalf("method = %s, want POST", request.Method)
		}
		if request.URL.Path != "/v1/embeddings" {
			t.Fatalf("path = %s, want /v1/embeddings", request.URL.Path)
		}
		if request.Header.Get("Authorization") != "Bearer test-api-key" {
			t.Fatal("Authorization header does not contain expected bearer token")
		}
		if request.Header.Get("Content-Type") != "application/json" {
			t.Fatalf(
				"Content-Type = %q, want application/json",
				request.Header.Get("Content-Type"),
			)
		}

		var payload embeddingRequest
		if err := json.NewDecoder(request.Body).Decode(&payload); err != nil {
			t.Fatalf("decode request: %v", err)
		}
		if len(payload.Input) != 2 ||
			payload.Input[0] != "first chunk" ||
			payload.Input[1] != "second chunk" ||
			payload.Model != "test-model" ||
			payload.Dimensions != 2 ||
			payload.EncodingFormat != "float" {
			t.Fatalf("request payload = %+v, want configured batch", payload)
		}

		response.Header().Set("Content-Type", "application/json")
		_, _ = response.Write([]byte(`{
			"data": [
				{"index": 1, "embedding": [0.3, 0.4]},
				{"index": 0, "embedding": [0.1, 0.2]}
			],
			"usage": {"prompt_tokens": 7, "total_tokens": 7}
		}`))
	}))
	defer server.Close()

	client, err := NewClient(
		" test-api-key ",
		server.URL+"/v1/embeddings",
		server.Client(),
	)
	if err != nil {
		t.Fatalf("NewClient() error = %v, want nil", err)
	}

	result, err := client.Embed(context.Background(), embeddingdomain.EmbedRequest{
		Inputs:     []string{"first chunk", "second chunk"},
		Model:      "test-model",
		Dimensions: 2,
	})
	if err != nil {
		t.Fatalf("Embed() error = %v, want nil", err)
	}

	if len(result.Vectors) != 2 ||
		result.Vectors[0][0] != 0.1 ||
		result.Vectors[1][0] != 0.3 {
		t.Fatalf("vectors = %v, want vectors restored by response index", result.Vectors)
	}
	if result.PromptTokens != 7 || result.TotalTokens != 7 {
		t.Fatalf("usage = (%d, %d), want (7, 7)", result.PromptTokens, result.TotalTokens)
	}
}

func TestClientEmbedMapsProviderErrors(t *testing.T) {
	testCases := []struct {
		name       string
		statusCode int
		wantError  error
	}{
		{name: "bad request", statusCode: http.StatusBadRequest, wantError: embeddingdomain.ErrEmbeddingRequestRejected},
		{name: "unauthorized", statusCode: http.StatusUnauthorized, wantError: embeddingdomain.ErrEmbeddingAuthentication},
		{name: "forbidden", statusCode: http.StatusForbidden, wantError: embeddingdomain.ErrEmbeddingAuthentication},
		{name: "rate limited", statusCode: http.StatusTooManyRequests, wantError: embeddingdomain.ErrEmbeddingRateLimited},
		{name: "server error", statusCode: http.StatusServiceUnavailable, wantError: embeddingdomain.ErrEmbeddingUnavailable},
	}

	for _, testCase := range testCases {
		t.Run(testCase.name, func(t *testing.T) {
			server := httptest.NewServer(http.HandlerFunc(func(
				response http.ResponseWriter,
				_ *http.Request,
			) {
				response.WriteHeader(testCase.statusCode)
				_, _ = response.Write([]byte(`{"error":{"message":"test provider error"}}`))
			}))
			defer server.Close()

			client, err := NewClient("test-key", server.URL, server.Client())
			if err != nil {
				t.Fatalf("NewClient() error = %v, want nil", err)
			}

			_, err = client.Embed(context.Background(), embeddingdomain.EmbedRequest{
				Inputs:     []string{"test"},
				Model:      "test-model",
				Dimensions: 2,
			})
			if !errors.Is(err, testCase.wantError) {
				t.Fatalf("Embed() error = %v, want %v", err, testCase.wantError)
			}
		})
	}
}

func TestClientEmbedMapsOpenAIQuotaExhaustion(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(
		response http.ResponseWriter,
		_ *http.Request,
	) {
		response.WriteHeader(http.StatusTooManyRequests)
		_, _ = response.Write([]byte(`{
			"error": {
				"message": "account has no credits remaining",
				"type": "insufficient_quota",
				"code": "insufficient_quota"
			}
		}`))
	}))
	defer server.Close()

	client, err := NewClient("test-key", server.URL, server.Client())
	if err != nil {
		t.Fatalf("NewClient() error = %v, want nil", err)
	}

	_, err = client.Embed(context.Background(), embeddingdomain.EmbedRequest{
		Inputs:     []string{"test"},
		Model:      "test-model",
		Dimensions: 2,
	})
	if !errors.Is(err, embeddingdomain.ErrEmbeddingQuotaExceeded) {
		t.Fatalf("Embed() error = %v, want ErrEmbeddingQuotaExceeded", err)
	}
}

func TestClientEmbedRejectsInvalidResponse(t *testing.T) {
	testCases := []struct {
		name string
		body string
	}{
		{
			name: "missing vector",
			body: `{"data": []}`,
		},
		{
			name: "wrong dimensions",
			body: `{"data": [{"index": 0, "embedding": [0.1]}]}`,
		},
		{
			name: "invalid index",
			body: `{"data": [{"index": 1, "embedding": [0.1, 0.2]}]}`,
		},
	}

	for _, testCase := range testCases {
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

			_, err = client.Embed(context.Background(), embeddingdomain.EmbedRequest{
				Inputs:     []string{"test"},
				Model:      "test-model",
				Dimensions: 2,
			})
			if !errors.Is(err, embeddingdomain.ErrInvalidEmbeddingResponse) {
				t.Fatalf(
					"Embed() error = %v, want ErrInvalidEmbeddingResponse",
					err,
				)
			}
		})
	}
}

func TestNewClientValidatesDependencies(t *testing.T) {
	testCases := []struct {
		name       string
		apiKey     string
		endpoint   string
		httpClient HTTPDoer
	}{
		{name: "missing API key", endpoint: DefaultEndpoint, httpClient: http.DefaultClient},
		{name: "invalid endpoint", apiKey: "test-key", endpoint: "not-a-url", httpClient: http.DefaultClient},
		{name: "missing HTTP client", apiKey: "test-key", endpoint: DefaultEndpoint},
	}

	for _, testCase := range testCases {
		t.Run(testCase.name, func(t *testing.T) {
			client, err := NewClient(
				testCase.apiKey,
				testCase.endpoint,
				testCase.httpClient,
			)
			if err == nil || client != nil {
				t.Fatalf("NewClient() = (%v, %v), want (nil, error)", client, err)
			}
		})
	}
}

func TestClientEmbedRejectsEmptyInputWithoutHTTPRequest(t *testing.T) {

	requestCount := 0
	server := httptest.NewServer(http.HandlerFunc(func(
		response http.ResponseWriter,
		request *http.Request,
	) {
		//统计发送请求的次数
		requestCount++
	}))

	defer server.Close()

	client, err := NewClient(
		" test-api-key ",
		server.URL+"/v1/embeddings",
		server.Client(),
	)

	if err != nil {
		t.Fatalf("NewClient() error = %v, want nil", err)
	}

	_, err = client.Embed(context.Background(), embeddingdomain.EmbedRequest{
		Inputs:     nil,
		Model:      "test-model",
		Dimensions: 2,
	})

	if !errors.Is(err, embeddingdomain.ErrEmbeddingRequestRejected) {
		t.Fatalf(
			"Embed(), error = %v, want ErrEmbeddingRequestRejected",
			err,
		)
	}

	if requestCount != 0 {
		t.Fatalf(
			"HTTP Request count = %d want 0",
			requestCount,
		)
	}

}
