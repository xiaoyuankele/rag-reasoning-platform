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
		Type:              answerapplication.JobEventRequeued,
		JobID:             42,
		Status:            answerapplication.JobStatusQueued,
		AttemptCount:      2,
		RetryCount:        1,
		QueueWait:         300 * time.Millisecond,
		ExecutionDuration: 2 * time.Second,
		TotalDuration:     5 * time.Second,
		QueueStats: &answerapplication.JobQueueStats{
			QueuedCount:             3,
			ReadyQueuedCount:        2,
			ProcessingCount:         1,
			MaxOwnerProcessingCount: 1,
			OldestReadyWait:         4 * time.Second,
		},
		NextAttemptAt: &nextAttemptAt,
		ErrorCode:     answerapplication.JobErrorCodeTemporarilyUnavailable,
		ErrorCategory: answerapplication.JobErrorCategoryGenerationUnavailable,
		Err:           errors.New("provider timeout"),
	})

	var entry map[string]any
	if err := json.Unmarshal(bytes.TrimSpace(output.Bytes()), &entry); err != nil {
		t.Fatalf("decode answer job log: %v", err)
	}
	assertStringLogField(t, entry, "level", "WARN")
	assertStringLogField(t, entry, "event", "answer_job_requeued")
	assertNumericLogField(t, entry, "answer_job_id", 42)
	assertNumericLogField(t, entry, "attempt_count", 2)
	assertNumericLogField(t, entry, "retry_count", 1)
	assertNumericLogField(t, entry, "queue_wait_ms", 300)
	assertNumericLogField(t, entry, "execution_duration_ms", 2000)
	assertNumericLogField(t, entry, "total_ms", 5000)
	assertNumericLogField(t, entry, "queued_count", 3)
	assertNumericLogField(t, entry, "ready_queued_count", 2)
	assertNumericLogField(t, entry, "processing_count", 1)
	assertNumericLogField(t, entry, "max_owner_processing_count", 1)
	assertNumericLogField(t, entry, "oldest_ready_wait_ms", 4000)
	assertStringLogField(t, entry, "error_category", "generation_unavailable")

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
