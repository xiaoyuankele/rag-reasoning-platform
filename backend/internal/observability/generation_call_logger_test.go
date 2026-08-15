package observability

import (
	"bytes"
	"encoding/json"
	"errors"
	"log/slog"
	"testing"
	"time"

	answerapplication "rag-reasoning-platform/backend/internal/application/answer"
)

func TestGenerationEventLevel(t *testing.T) {
	testCases := []struct {
		eventType answerapplication.GenerationEventType
		want      slog.Level
	}{
		{answerapplication.GenerationEventSkipped, slog.LevelInfo},
		{answerapplication.GenerationEventStarted, slog.LevelInfo},
		{answerapplication.GenerationEventSucceeded, slog.LevelInfo},
		{answerapplication.GenerationEventFailed, slog.LevelError},
	}

	for _, testCase := range testCases {
		if got := generationEventLevel(testCase.eventType); got != testCase.want {
			t.Fatalf("generationEventLevel(%q) = %s, want %s", testCase.eventType, got, testCase.want)
		}
	}
}

func TestGenerationCallLoggerWritesRequestCostAndScopeFields(t *testing.T) {
	var output bytes.Buffer
	logger := slog.New(slog.NewJSONHandler(&output, nil))
	observer := NewGenerationCallLogger(logger)
	documentID := int64(72)
	ctx := WithRequestID(t.Context(), "answer-request-42")

	observer.ObserveGenerationEvent(
		ctx,
		answerapplication.GenerationEvent{
			Type:             answerapplication.GenerationEventSucceeded,
			ModelName:        "qwen3.6-flash",
			ResponseLanguage: answerapplication.ResponseLanguageChinese,
			DocumentID:       &documentID,
			RequestedTopK:    5,
			EvidenceCount:    4,
			ProviderDuration: 1250 * time.Millisecond,
			PromptTokens:     800,
			CompletionTokens: 120,
			TotalTokens:      920,
		},
	)

	var entry map[string]any
	if err := json.Unmarshal(bytes.TrimSpace(output.Bytes()), &entry); err != nil {
		t.Fatalf("decode generation log: %v; output = %q", err, output.String())
	}

	assertStringLogField(t, entry, "level", "INFO")
	assertStringLogField(t, entry, "event", "answer_generation_succeeded")
	assertStringLogField(t, entry, "request_id", "answer-request-42")
	assertStringLogField(t, entry, "model_name", "qwen3.6-flash")
	assertStringLogField(t, entry, "response_language", "zh")
	assertNumericLogField(t, entry, "document_id", 72)
	assertNumericLogField(t, entry, "requested_top_k", 5)
	assertNumericLogField(t, entry, "evidence_count", 4)
	assertNumericLogField(t, entry, "provider_duration_ms", 1250)
	assertNumericLogField(t, entry, "prompt_tokens", 800)
	assertNumericLogField(t, entry, "completion_tokens", 120)
	assertNumericLogField(t, entry, "total_tokens", 920)

	for _, forbiddenField := range []string{"query", "prompt", "evidence", "answer", "api_key"} {
		if _, exists := entry[forbiddenField]; exists {
			t.Fatalf("generation log contains forbidden field %q", forbiddenField)
		}
	}
}

func TestGenerationCallLoggerWritesClassifiedFailure(t *testing.T) {
	var output bytes.Buffer
	logger := slog.New(slog.NewJSONHandler(&output, nil))
	observer := NewGenerationCallLogger(logger)
	providerError := errors.New("remote generation failed")

	observer.ObserveGenerationEvent(
		t.Context(),
		answerapplication.GenerationEvent{
			Type:             answerapplication.GenerationEventFailed,
			ModelName:        "qwen3.6-flash",
			ResponseLanguage: answerapplication.ResponseLanguageEnglish,
			RequestedTopK:    5,
			EvidenceCount:    3,
			ProviderDuration: 400 * time.Millisecond,
			ErrorCategory:    answerapplication.GenerationErrorCategoryProviderUnavailable,
			Err:              providerError,
		},
	)

	var entry map[string]any
	if err := json.Unmarshal(bytes.TrimSpace(output.Bytes()), &entry); err != nil {
		t.Fatalf("decode generation failure log: %v; output = %q", err, output.String())
	}

	assertStringLogField(t, entry, "level", "ERROR")
	assertStringLogField(t, entry, "event", "answer_generation_failed")
	assertStringLogField(t, entry, "error_category", "provider_unavailable")
	if entry["error"] != providerError.Error() {
		t.Fatalf("error = %#v, want %q", entry["error"], providerError.Error())
	}
}
