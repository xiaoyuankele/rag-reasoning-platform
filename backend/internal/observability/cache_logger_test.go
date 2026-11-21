package observability

import (
	"bytes"
	"encoding/json"
	"errors"
	"log/slog"
	"testing"
	"time"

	answerapplication "rag-reasoning-platform/backend/internal/application/answer"
	embeddingapplication "rag-reasoning-platform/backend/internal/application/embedding"
)

func TestQueryVectorCacheLoggerDoesNotExposeQuestionOrOwner(t *testing.T) {
	var output bytes.Buffer
	logger := slog.New(slog.NewJSONHandler(&output, &slog.HandlerOptions{Level: slog.LevelDebug}))
	observer := NewQueryVectorCacheLogger(logger)

	observer.ObserveQueryVectorCacheEvent(
		WithRequestID(t.Context(), "cache-request-1"),
		embeddingapplication.QueryVectorCacheEvent{
			Type:         embeddingapplication.QueryVectorCacheWaited,
			Provider:     "dashscope",
			ModelName:    "text-embedding-v4",
			Dimensions:   1536,
			WaitDuration: 125 * time.Millisecond,
		},
	)

	entry := decodeCacheLogEntry(t, output.Bytes())
	assertStringLogField(t, entry, "event", "query_vector_cache_waited")
	assertNumericLogField(t, entry, "dimensions", 1536)
	assertNumericLogField(t, entry, "wait_duration_ms", 125)
	assertCacheLogForbiddenFields(t, entry)
}

func TestAnswerCacheLoggerUsesWarningForFailure(t *testing.T) {
	var output bytes.Buffer
	logger := slog.New(slog.NewJSONHandler(&output, nil))
	observer := NewAnswerCacheLogger(logger)

	observer.ObserveAnswerCacheEvent(
		t.Context(),
		answerapplication.AnswerCacheEvent{
			Type:           answerapplication.AnswerCacheReadFailed,
			CorpusRevision: 12,
			Err:            errors.New("Redis unavailable"),
		},
	)

	entry := decodeCacheLogEntry(t, output.Bytes())
	assertStringLogField(t, entry, "level", "WARN")
	assertStringLogField(t, entry, "event", "answer_cache_read_failed")
	assertNumericLogField(t, entry, "corpus_revision", 12)
	assertCacheLogForbiddenFields(t, entry)
}

func decodeCacheLogEntry(t *testing.T, value []byte) map[string]any {
	t.Helper()
	var entry map[string]any
	if err := json.Unmarshal(bytes.TrimSpace(value), &entry); err != nil {
		t.Fatalf("decode cache log: %v; output = %q", err, value)
	}
	return entry
}

func assertCacheLogForbiddenFields(t *testing.T, entry map[string]any) {
	t.Helper()
	for _, field := range []string{
		"owner_user_id", "query", "question", "answer", "prompt", "api_key",
	} {
		if _, exists := entry[field]; exists {
			t.Fatalf("cache log contains forbidden field %q", field)
		}
	}
}
