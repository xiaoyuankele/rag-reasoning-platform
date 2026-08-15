package observability

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"log/slog"
	"testing"
	"time"

	documentapplication "rag-reasoning-platform/backend/internal/application/document"
	documentdomain "rag-reasoning-platform/backend/internal/domain/document"
)

func TestProcessingJobLoggerWritesStructuredLifecycleEvents(t *testing.T) {
	testCases := []struct {
		name         string
		event        documentapplication.ProcessingJobEvent
		wantLevel    string
		wantDuration bool
		wantError    bool
	}{
		{
			name: "started",
			event: documentapplication.ProcessingJobEvent{
				Type:         documentapplication.ProcessingJobEventStarted,
				JobID:        17,
				DocumentID:   7,
				AttemptCount: 1,
				Status:       documentdomain.ProcessingJobStatusProcessing,
			},
			wantLevel: "INFO",
		},
		{
			name: "succeeded",
			event: documentapplication.ProcessingJobEvent{
				Type:         documentapplication.ProcessingJobEventSucceeded,
				JobID:        17,
				DocumentID:   7,
				AttemptCount: 1,
				Status:       documentdomain.ProcessingJobStatusSucceeded,
				Duration:     1250 * time.Millisecond,
			},
			wantLevel:    "INFO",
			wantDuration: true,
		},
		{
			name: "failed",
			event: documentapplication.ProcessingJobEvent{
				Type:         documentapplication.ProcessingJobEventFailed,
				JobID:        18,
				DocumentID:   8,
				AttemptCount: 2,
				Status:       documentdomain.ProcessingJobStatusFailed,
				Duration:     2 * time.Second,
				Err:          errors.New("processor exited with code 1"),
			},
			wantLevel:    "ERROR",
			wantDuration: true,
			wantError:    true,
		},
		{
			name: "unfinished",
			event: documentapplication.ProcessingJobEvent{
				Type:         documentapplication.ProcessingJobEventUnfinished,
				JobID:        19,
				DocumentID:   9,
				AttemptCount: 3,
				Status:       documentdomain.ProcessingJobStatusProcessing,
				Duration:     3 * time.Second,
				Err:          errors.New("finalize task: database unavailable"),
			},
			wantLevel:    "ERROR",
			wantDuration: true,
			wantError:    true,
		},
	}

	for _, testCase := range testCases {
		t.Run(testCase.name, func(t *testing.T) {
			var output bytes.Buffer
			logger := slog.New(slog.NewJSONHandler(&output, nil))
			observer := NewProcessingJobLogger(logger)

			observer.ObserveProcessingJobEvent(
				context.Background(),
				testCase.event,
			)

			var entry map[string]any
			if err := json.Unmarshal(bytes.TrimSpace(output.Bytes()), &entry); err != nil {
				t.Fatalf("decode processing job log: %v; output = %q", err, output.String())
			}

			assertStringLogField(t, entry, "level", testCase.wantLevel)
			assertStringLogField(t, entry, "event", string(testCase.event.Type))
			assertStringLogField(t, entry, "status", string(testCase.event.Status))
			assertNumericLogField(t, entry, "processing_job_id", testCase.event.JobID)
			assertNumericLogField(t, entry, "document_id", testCase.event.DocumentID)
			assertNumericLogField(t, entry, "attempt_count", int64(testCase.event.AttemptCount))

			_, hasDuration := entry["duration_ms"]
			if hasDuration != testCase.wantDuration {
				t.Fatalf("duration_ms presence = %t, want %t", hasDuration, testCase.wantDuration)
			}
			_, hasError := entry["error"]
			if hasError != testCase.wantError {
				t.Fatalf("error presence = %t, want %t", hasError, testCase.wantError)
			}
		})
	}
}

func assertStringLogField(
	t *testing.T,
	entry map[string]any,
	field string,
	want string,
) {
	t.Helper()

	if got, ok := entry[field].(string); !ok || got != want {
		t.Fatalf("%s = %#v, want %q", field, entry[field], want)
	}
}

func assertNumericLogField(
	t *testing.T,
	entry map[string]any,
	field string,
	want int64,
) {
	t.Helper()

	got, ok := entry[field].(float64)
	if !ok || int64(got) != want {
		t.Fatalf("%s = %#v, want %d", field, entry[field], want)
	}
}
