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

func TestAnswerJobLoggerWritesSafeRetryFields(t *testing.T) {
	var output bytes.Buffer
	observer := NewAnswerJobLogger(slog.New(slog.NewJSONHandler(&output, nil)))
	nextAttemptAt := time.Date(2026, 8, 25, 12, 0, 0, 0, time.UTC)

	observer.ObserveAnswerJobEvent(t.Context(), answerapplication.JobEvent{
		Type:          answerapplication.JobEventRequeued,
		JobID:         42,
		Status:        answerapplication.JobStatusQueued,
		AttemptCount:  1,
		Duration:      2 * time.Second,
		NextAttemptAt: &nextAttemptAt,
		ErrorCode:     answerapplication.JobErrorCodeTemporarilyUnavailable,
		Err:           errors.New("provider timeout"),
	})

	var entry map[string]any
	if err := json.Unmarshal(bytes.TrimSpace(output.Bytes()), &entry); err != nil {
		t.Fatalf("decode answer job log: %v", err)
	}
	assertStringLogField(t, entry, "level", "WARN")
	assertStringLogField(t, entry, "event", "answer_job_requeued")
	assertNumericLogField(t, entry, "answer_job_id", 42)
	assertNumericLogField(t, entry, "attempt_count", 1)
	assertNumericLogField(t, entry, "duration_ms", 2000)

	for _, forbidden := range []string{
		"owner_id",
		"owner_user_id",
		"query",
		"prompt",
		"answer",
		"sources",
	} {
		if _, ok := entry[forbidden]; ok {
			t.Fatalf("answer job log contains forbidden field %q", forbidden)
		}
	}
}
