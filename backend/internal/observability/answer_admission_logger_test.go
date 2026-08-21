package observability

import (
	"bytes"
	"encoding/json"
	"log/slog"
	"testing"
	"time"

	answerapplication "rag-reasoning-platform/backend/internal/application/answer"
)

func TestAnswerAdmissionLoggerWritesSafeCapacityFields(t *testing.T) {
	var output bytes.Buffer
	logger := slog.New(slog.NewJSONHandler(&output, nil))
	observer := NewAnswerAdmissionLogger(logger)
	ctx := WithRequestID(t.Context(), "answer-capacity-42")

	observer.ObserveAnswerAdmissionEvent(ctx, answerapplication.AnswerAdmissionEvent{
		Type:              answerapplication.AnswerAdmissionEventReleased,
		Outcome:           answerapplication.AnswerAdmissionOutcomeSucceeded,
		WaitDuration:      25 * time.Millisecond,
		ExecutionDuration: 1500 * time.Millisecond,
		InFlight:          1,
		MaxConcurrency:    2,
	})

	var entry map[string]any
	if err := json.Unmarshal(bytes.TrimSpace(output.Bytes()), &entry); err != nil {
		t.Fatalf("decode admission log: %v; output = %q", err, output.String())
	}

	assertStringLogField(t, entry, "level", "INFO")
	assertStringLogField(t, entry, "event", "answer_request_released")
	assertStringLogField(t, entry, "outcome", "succeeded")
	assertStringLogField(t, entry, "request_id", "answer-capacity-42")
	assertNumericLogField(t, entry, "wait_duration_ms", 25)
	assertNumericLogField(t, entry, "execution_duration_ms", 1500)
	assertNumericLogField(t, entry, "in_flight", 1)
	assertNumericLogField(t, entry, "max_concurrency", 2)

	for _, forbiddenField := range []string{
		"query",
		"prompt",
		"answer",
		"evidence",
		"api_key",
	} {
		if _, exists := entry[forbiddenField]; exists {
			t.Fatalf("admission log contains forbidden field %q", forbiddenField)
		}
	}
}

func TestAnswerAdmissionLoggerUsesWarningForCapacityTimeout(t *testing.T) {
	var output bytes.Buffer
	logger := slog.New(slog.NewJSONHandler(&output, nil))
	observer := NewAnswerAdmissionLogger(logger)

	observer.ObserveAnswerAdmissionEvent(t.Context(), answerapplication.AnswerAdmissionEvent{
		Type:           answerapplication.AnswerAdmissionEventRejected,
		Outcome:        answerapplication.AnswerAdmissionOutcomeCapacityTimeout,
		WaitDuration:   3 * time.Second,
		InFlight:       2,
		MaxConcurrency: 2,
	})

	var entry map[string]any
	if err := json.Unmarshal(bytes.TrimSpace(output.Bytes()), &entry); err != nil {
		t.Fatalf("decode admission warning: %v", err)
	}
	assertStringLogField(t, entry, "level", "WARN")
	assertStringLogField(t, entry, "event", "answer_request_rejected")
	assertStringLogField(t, entry, "outcome", "capacity_timeout")
}
