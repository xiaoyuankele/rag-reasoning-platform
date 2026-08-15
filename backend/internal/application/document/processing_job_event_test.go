package document

import (
	"context"
	"testing"

	documentdomain "rag-reasoning-platform/backend/internal/domain/document"
)

// recordingProcessingJobEventObserver 是 Worker 测试使用的 Fake 观察器。
// 它不写真实日志，只按发生顺序保存事件，供测试核对生命周期。
type recordingProcessingJobEventObserver struct {
	events []ProcessingJobEvent
}

func newRecordingProcessingJobEventObserver() *recordingProcessingJobEventObserver {
	return &recordingProcessingJobEventObserver{
		events: make([]ProcessingJobEvent, 0, 2),
	}
}

// ObserveProcessingJobEvent 实现 ProcessingJobEventObserver 契约。
func (o *recordingProcessingJobEventObserver) ObserveProcessingJobEvent(
	_ context.Context,
	event ProcessingJobEvent,
) {
	o.events = append(o.events, event)
}

func assertProcessingJobEventIdentity(
	t *testing.T,
	event ProcessingJobEvent,
	wantType ProcessingJobEventType,
	wantStatus documentdomain.ProcessingJobStatus,
	wantJob documentdomain.ProcessingJob,
) {
	t.Helper()

	if event.Type != wantType {
		t.Fatalf("event type = %q, want %q", event.Type, wantType)
	}
	if event.Status != wantStatus {
		t.Fatalf("event status = %q, want %q", event.Status, wantStatus)
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
	if event.AttemptCount != wantJob.AttemptCount {
		t.Fatalf(
			"event attempt count = %d, want %d",
			event.AttemptCount,
			wantJob.AttemptCount,
		)
	}
}
