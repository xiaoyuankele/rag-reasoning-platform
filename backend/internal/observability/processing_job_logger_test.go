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
		name        string
		event       documentapplication.ProcessingJobEvent
		wantLevel   string
		wantMetrics bool
		wantError   bool
	}{
		{
			name: "started",
			event: documentapplication.ProcessingJobEvent{
				Type:         documentapplication.ProcessingJobEventStarted,
				JobID:        17,
				DocumentID:   7,
				AttemptCount: 1,
				Status:       documentdomain.ProcessingJobStatusProcessing,
				QueueWait:    75 * time.Millisecond,
			},
			wantLevel: "INFO",
		},
		{
			name: "succeeded",
			event: documentapplication.ProcessingJobEvent{
				Type:              documentapplication.ProcessingJobEventSucceeded,
				JobID:             17,
				DocumentID:        7,
				AttemptCount:      1,
				Status:            documentdomain.ProcessingJobStatusSucceeded,
				QueueWait:         75 * time.Millisecond,
				ProcessorDuration: time.Second,
				TotalDuration:     1250 * time.Millisecond,
				FileBytes:         4096,
				ChunkCount:        2,
			},
			wantLevel:   "INFO",
			wantMetrics: true,
		},
		{
			name: "failed",
			event: documentapplication.ProcessingJobEvent{
				Type:              documentapplication.ProcessingJobEventFailed,
				JobID:             18,
				DocumentID:        8,
				AttemptCount:      2,
				Status:            documentdomain.ProcessingJobStatusFailed,
				QueueWait:         150 * time.Millisecond,
				ProcessorDuration: 1800 * time.Millisecond,
				TotalDuration:     2 * time.Second,
				FileBytes:         8192,
				ErrorCode:         documentdomain.ProcessingErrorCodeProcessor,
				Err:               errors.New("processor exited with code 1"),
			},
			wantLevel:   "ERROR",
			wantMetrics: true,
			wantError:   true,
		},
		{
			name: "unfinished",
			event: documentapplication.ProcessingJobEvent{
				Type:              documentapplication.ProcessingJobEventUnfinished,
				JobID:             19,
				DocumentID:        9,
				AttemptCount:      3,
				Status:            documentdomain.ProcessingJobStatusProcessing,
				QueueWait:         200 * time.Millisecond,
				ProcessorDuration: 2500 * time.Millisecond,
				TotalDuration:     3 * time.Second,
				FileBytes:         16384,
				ChunkCount:        4,
				ErrorCode:         documentdomain.ProcessingErrorCodeFinalization,
				Err:               errors.New("finalize task: database unavailable"),
			},
			wantLevel:   "ERROR",
			wantMetrics: true,
			wantError:   true,
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
			assertNumericLogField(t, entry, "queue_wait_ms", testCase.event.QueueWait.Milliseconds())

			_, hasTotal := entry["total_ms"]
			if hasTotal != testCase.wantMetrics {
				t.Fatalf("total_ms presence = %t, want %t", hasTotal, testCase.wantMetrics)
			}
			_, hasProcessor := entry["processor_ms"]
			if hasProcessor != testCase.wantMetrics {
				t.Fatalf("processor_ms presence = %t, want %t", hasProcessor, testCase.wantMetrics)
			}
			_, hasFileBytes := entry["file_bytes"]
			if hasFileBytes != testCase.wantMetrics {
				t.Fatalf("file_bytes presence = %t, want %t", hasFileBytes, testCase.wantMetrics)
			}
			_, hasChunkCount := entry["chunk_count"]
			if hasChunkCount != testCase.wantMetrics {
				t.Fatalf("chunk_count presence = %t, want %t", hasChunkCount, testCase.wantMetrics)
			}

			_, hasErrorCode := entry["error_code"]
			wantErrorCode := testCase.event.ErrorCode != ""
			if hasErrorCode != wantErrorCode {
				t.Fatalf("error_code presence = %t, want %t", hasErrorCode, wantErrorCode)
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
