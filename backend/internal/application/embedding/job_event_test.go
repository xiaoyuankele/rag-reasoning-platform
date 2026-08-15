package embedding

import (
	"context"
	"testing"

	embeddingdomain "rag-reasoning-platform/backend/internal/domain/embedding"
)

// recordingJobEventObserver 是 Worker 测试使用的 Fake，只保存事件而不写日志。
type recordingJobEventObserver struct {
	events []JobEvent
}

func newRecordingJobEventObserver() *recordingJobEventObserver {
	return &recordingJobEventObserver{events: make([]JobEvent, 0, 2)}
}

func (o *recordingJobEventObserver) ObserveEmbeddingJobEvent(
	_ context.Context,
	event JobEvent,
) {
	o.events = append(o.events, event)
}

func assertJobEventIdentity(
	t *testing.T,
	event JobEvent,
	wantType JobEventType,
	wantStatus embeddingdomain.JobStatus,
	wantJob embeddingdomain.Job,
) {
	t.Helper()

	if event.Type != wantType || event.Status != wantStatus {
		t.Fatalf(
			"event type/status = (%q, %q), want (%q, %q)",
			event.Type,
			event.Status,
			wantType,
			wantStatus,
		)
	}
	if event.JobID != wantJob.ID || event.DocumentID != wantJob.DocumentID {
		t.Fatalf(
			"event IDs = job %d/document %d, want job %d/document %d",
			event.JobID,
			event.DocumentID,
			wantJob.ID,
			wantJob.DocumentID,
		)
	}
	if event.ModelName != wantJob.ModelName ||
		event.Dimensions != wantJob.Dimensions ||
		event.AttemptCount != wantJob.AttemptCount {
		t.Fatalf(
			"event model/dimensions/attempt = (%q, %d, %d), want (%q, %d, %d)",
			event.ModelName,
			event.Dimensions,
			event.AttemptCount,
			wantJob.ModelName,
			wantJob.Dimensions,
			wantJob.AttemptCount,
		)
	}
}
