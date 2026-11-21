package observability

import (
	"bytes"
	"context"
	"encoding/json"
	"log/slog"
	"testing"
	"time"

	documentapplication "rag-reasoning-platform/backend/internal/application/document"
)

func TestUploadAdmissionLoggerWritesSafeCapacityAndFileFields(t *testing.T) {
	var output bytes.Buffer
	logger := slog.New(slog.NewJSONHandler(&output, nil))
	observer := NewUploadAdmissionLogger(logger)
	ctx := WithRequestID(context.Background(), "request-upload-1")

	observer.ObserveUploadAdmissionEvent(ctx, documentapplication.UploadAdmissionEvent{
		Type:                 documentapplication.UploadAdmissionEventReleased,
		Outcome:              documentapplication.UploadAdmissionOutcomeSucceeded,
		WaitDuration:         12 * time.Millisecond,
		ExecutionDuration:    345 * time.Millisecond,
		OwnerInFlight:        0,
		OwnerMaxConcurrency:  1,
		GlobalInFlight:       2,
		GlobalMaxConcurrency: 4,
		BytesRead:            2048,
		Duplicate:            true,
	})

	var entry map[string]any
	if err := json.Unmarshal(output.Bytes(), &entry); err != nil {
		t.Fatalf("decode log entry: %v", err)
	}
	assertNumericLogField(t, entry, "wait_duration_ms", 12)
	assertNumericLogField(t, entry, "execution_duration_ms", 345)
	assertNumericLogField(t, entry, "owner_max_concurrency", 1)
	assertNumericLogField(t, entry, "global_max_concurrency", 4)
	assertNumericLogField(t, entry, "file_bytes", 2048)
	if entry["request_id"] != "request-upload-1" ||
		entry["outcome"] != "succeeded" ||
		entry["duplicate"] != true {
		t.Fatalf("unexpected upload log entry: %+v", entry)
	}
	for _, forbidden := range []string{
		"file_name",
		"content",
		"sha256",
		"storage_path",
		"owner_user_id",
	} {
		if _, exists := entry[forbidden]; exists {
			t.Fatalf("log unexpectedly contains %q: %+v", forbidden, entry)
		}
	}
}

func TestUploadAdmissionLoggerWarnsOnCapacityTimeout(t *testing.T) {
	var output bytes.Buffer
	logger := slog.New(slog.NewJSONHandler(&output, &slog.HandlerOptions{
		Level: slog.LevelDebug,
	}))
	observer := NewUploadAdmissionLogger(logger)

	observer.ObserveUploadAdmissionEvent(
		context.Background(),
		documentapplication.UploadAdmissionEvent{
			Type:                 documentapplication.UploadAdmissionEventRejected,
			Outcome:              documentapplication.UploadAdmissionOutcomeGlobalCapacityTimeout,
			WaitDuration:         2 * time.Second,
			OwnerInFlight:        0,
			OwnerMaxConcurrency:  1,
			GlobalInFlight:       4,
			GlobalMaxConcurrency: 4,
		},
	)

	var entry map[string]any
	if err := json.Unmarshal(output.Bytes(), &entry); err != nil {
		t.Fatalf("decode log entry: %v", err)
	}
	if entry["level"] != "WARN" ||
		entry["event"] != "upload_request_rejected" ||
		entry["outcome"] != "global_capacity_timeout" {
		t.Fatalf("unexpected rejected upload log entry: %+v", entry)
	}
}
