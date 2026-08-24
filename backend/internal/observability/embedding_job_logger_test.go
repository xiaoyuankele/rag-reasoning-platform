package observability

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"log/slog"
	"testing"
	"time"

	embeddingapplication "rag-reasoning-platform/backend/internal/application/embedding"
	embeddingdomain "rag-reasoning-platform/backend/internal/domain/embedding"
)

func TestEmbeddingJobEventLevel(t *testing.T) {
	testCases := []struct {
		eventType embeddingapplication.JobEventType
		want      slog.Level
	}{
		{embeddingapplication.JobEventStarted, slog.LevelInfo},
		{embeddingapplication.JobEventSucceeded, slog.LevelInfo},
		{embeddingapplication.JobEventRequeued, slog.LevelWarn},
		{embeddingapplication.JobEventInterrupted, slog.LevelWarn},
		{embeddingapplication.JobEventFailed, slog.LevelError},
		{embeddingapplication.JobEventUnfinished, slog.LevelError},
	}

	for _, testCase := range testCases {
		if got := embeddingJobEventLevel(testCase.eventType); got != testCase.want {
			t.Fatalf("embeddingJobEventLevel(%q) = %s, want %s", testCase.eventType, got, testCase.want)
		}
	}
}

func TestEmbeddingJobLoggerWritesRetryAndCostFields(t *testing.T) {
	var output bytes.Buffer
	logger := slog.New(slog.NewJSONHandler(&output, nil))
	observer := NewEmbeddingJobLogger(logger)
	nextAttemptAt := time.Date(2026, time.August, 15, 10, 1, 0, 0, time.UTC)
	finalizationDuration := 15 * time.Millisecond
	providerError := errors.New("provider rate limited")

	observer.ObserveEmbeddingJobEvent(
		context.Background(),
		embeddingapplication.JobEvent{
			Type:                 embeddingapplication.JobEventRequeued,
			JobID:                27,
			DocumentID:           31,
			ModelName:            "text-embedding-v4",
			Dimensions:           1024,
			AttemptCount:         2,
			Status:               embeddingdomain.JobStatusQueued,
			Duration:             2500 * time.Millisecond,
			ProviderDuration:     2 * time.Second,
			FinalizationDuration: &finalizationDuration,
			ProviderCallCount:    2,
			PromptTokens:         40,
			TotalTokens:          40,
			GeneratedVectorCount: 5,
			RetryCount:           1,
			NextAttemptAt:        &nextAttemptAt,
			ErrorCategory:        embeddingapplication.JobErrorCategoryProviderRateLimit,
			Err:                  providerError,
		},
	)

	var entry map[string]any
	if err := json.Unmarshal(bytes.TrimSpace(output.Bytes()), &entry); err != nil {
		t.Fatalf("decode embedding job log: %v; output = %q", err, output.String())
	}

	assertStringLogField(t, entry, "level", "WARN")
	assertStringLogField(t, entry, "event", "embedding_job_requeued")
	assertStringLogField(t, entry, "status", "queued")
	assertStringLogField(t, entry, "model_name", "text-embedding-v4")
	assertStringLogField(t, entry, "error_category", "provider_rate_limit")
	assertStringLogField(t, entry, "next_attempt_at", nextAttemptAt.Format(time.RFC3339Nano))
	assertNumericLogField(t, entry, "embedding_job_id", 27)
	assertNumericLogField(t, entry, "document_id", 31)
	assertNumericLogField(t, entry, "provider_call_count", 2)
	assertNumericLogField(t, entry, "finalization_duration_ms", 15)
	assertNumericLogField(t, entry, "prompt_tokens", 40)
	assertNumericLogField(t, entry, "generated_vector_count", 5)
	assertNumericLogField(t, entry, "retry_count", 1)
	if recovered, ok := entry["recovered"].(bool); !ok || recovered {
		t.Fatalf("recovered = %#v, want false", entry["recovered"])
	}
	if entry["error"] != providerError.Error() {
		t.Fatalf("error = %#v, want %q", entry["error"], providerError.Error())
	}
}
