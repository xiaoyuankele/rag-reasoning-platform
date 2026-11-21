package observability

import (
	"bytes"
	"encoding/json"
	"log/slog"
	"testing"
	"time"

	embeddingapplication "rag-reasoning-platform/backend/internal/application/embedding"
)

func TestEmbeddingProviderAdmissionLoggerWritesSafeCapacityFields(t *testing.T) {
	var output bytes.Buffer
	logger := slog.New(slog.NewJSONHandler(&output, nil))
	observer := NewEmbeddingProviderAdmissionLogger(logger)
	ctx := WithRequestID(t.Context(), "semantic-request-42")

	observer.ObserveEmbeddingProviderAdmissionEvent(
		ctx,
		embeddingapplication.EmbeddingProviderAdmissionEvent{
			Type:                 embeddingapplication.EmbeddingProviderAdmissionEventReleased,
			Origin:               embeddingapplication.EmbeddingProviderCallOriginOnline,
			Outcome:              embeddingapplication.EmbeddingProviderAdmissionOutcomeSucceeded,
			WaitDuration:         25 * time.Millisecond,
			ExecutionDuration:    1250 * time.Millisecond,
			OriginInFlight:       1,
			OriginMaxConcurrency: 2,
			InFlight:             2,
			MaxConcurrency:       4,
		},
	)

	var entry map[string]any
	if err := json.Unmarshal(bytes.TrimSpace(output.Bytes()), &entry); err != nil {
		t.Fatalf("decode provider admission log: %v; output = %q", err, output.String())
	}

	assertStringLogField(t, entry, "level", "INFO")
	assertStringLogField(t, entry, "event", "embedding_provider_request_released")
	assertStringLogField(t, entry, "origin", "online")
	assertStringLogField(t, entry, "outcome", "succeeded")
	assertStringLogField(t, entry, "request_id", "semantic-request-42")
	assertNumericLogField(t, entry, "wait_duration_ms", 25)
	assertNumericLogField(t, entry, "execution_duration_ms", 1250)
	assertNumericLogField(t, entry, "origin_in_flight", 1)
	assertNumericLogField(t, entry, "origin_max_concurrency", 2)
	assertNumericLogField(t, entry, "in_flight", 2)
	assertNumericLogField(t, entry, "max_concurrency", 4)

	for _, forbiddenField := range []string{
		"query",
		"input",
		"vector",
		"response",
		"api_key",
	} {
		if _, exists := entry[forbiddenField]; exists {
			t.Fatalf("provider admission log contains forbidden field %q", forbiddenField)
		}
	}
}

func TestEmbeddingProviderAdmissionLoggerWarnsOnCapacityTimeout(t *testing.T) {
	var output bytes.Buffer
	logger := slog.New(slog.NewJSONHandler(&output, nil))
	observer := NewEmbeddingProviderAdmissionLogger(logger)

	observer.ObserveEmbeddingProviderAdmissionEvent(
		t.Context(),
		embeddingapplication.EmbeddingProviderAdmissionEvent{
			Type:                 embeddingapplication.EmbeddingProviderAdmissionEventRejected,
			Origin:               embeddingapplication.EmbeddingProviderCallOriginOnline,
			Outcome:              embeddingapplication.EmbeddingProviderAdmissionOutcomeCapacityTimeout,
			WaitDuration:         2 * time.Second,
			OriginInFlight:       2,
			OriginMaxConcurrency: 2,
			InFlight:             4,
			MaxConcurrency:       4,
		},
	)

	var entry map[string]any
	if err := json.Unmarshal(bytes.TrimSpace(output.Bytes()), &entry); err != nil {
		t.Fatalf("decode provider admission warning: %v", err)
	}
	assertStringLogField(t, entry, "level", "WARN")
	assertStringLogField(t, entry, "event", "embedding_provider_request_rejected")
	assertStringLogField(t, entry, "outcome", "capacity_timeout")
}
