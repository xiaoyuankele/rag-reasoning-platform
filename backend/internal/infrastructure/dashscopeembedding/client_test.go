package dashscopeembedding

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	embeddingdomain "rag-reasoning-platform/backend/internal/domain/embedding"
)

func TestClientUsesCompatibleEmbeddingContract(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(
		response http.ResponseWriter,
		request *http.Request,
	) {
		if request.Method != http.MethodPost {
			t.Fatalf("method = %s, want POST", request.Method)
		}
		if request.Header.Get("Authorization") != "Bearer test-key" {
			t.Fatal("Authorization header does not contain expected bearer token")
		}
		response.Header().Set("Content-Type", "application/json")
		_, _ = response.Write([]byte(`{
			"data": [{"index": 0, "embedding": [0.1, 0.2]}],
			"usage": {"prompt_tokens": 3, "total_tokens": 3}
		}`))
	}))
	defer server.Close()

	client, err := NewClient("test-key", server.URL, server.Client())
	if err != nil {
		t.Fatalf("NewClient() error = %v, want nil", err)
	}

	result, err := client.Embed(context.Background(), embeddingdomain.EmbedRequest{
		Inputs:     []string{"测试文本"},
		Model:      "text-embedding-v4",
		Dimensions: 2,
	})
	if err != nil {
		t.Fatalf("Embed() error = %v, want nil", err)
	}
	if len(result.Vectors) != 1 || len(result.Vectors[0]) != 2 {
		t.Fatalf("vectors = %v, want one two-dimensional vector", result.Vectors)
	}
}

func TestClientMapsTopLevelQuotaError(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(
		response http.ResponseWriter,
		_ *http.Request,
	) {
		response.WriteHeader(http.StatusTooManyRequests)
		_, _ = response.Write([]byte(`{
			"code": "AllocationQuota.FreeTierOnly",
			"message": "free allocation is exhausted"
		}`))
	}))
	defer server.Close()

	client, err := NewClient("test-key", server.URL, server.Client())
	if err != nil {
		t.Fatalf("NewClient() error = %v, want nil", err)
	}

	_, err = client.Embed(context.Background(), embeddingdomain.EmbedRequest{
		Inputs:     []string{"test"},
		Model:      "text-embedding-v4",
		Dimensions: 2,
	})
	if !errors.Is(err, embeddingdomain.ErrEmbeddingQuotaExceeded) {
		t.Fatalf("Embed() error = %v, want ErrEmbeddingQuotaExceeded", err)
	}
	if !strings.Contains(err.Error(), "DashScope returned HTTP 429") {
		t.Fatalf("Embed() error = %q, want provider name and status", err)
	}
}
