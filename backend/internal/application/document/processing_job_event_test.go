package document

import (
	"context"
	"sync"
	"testing"
	"time"

	documentdomain "rag-reasoning-platform/backend/internal/domain/document"
)

func TestProcessingJobQueueWaitUsesDatabaseStartedAt(t *testing.T) {
	createdAt := time.Date(2026, 8, 20, 9, 0, 0, 0, time.UTC)
	startedAt := createdAt.Add(1500 * time.Millisecond)
	claimedAt := startedAt.Add(250 * time.Millisecond)

	wait := processingJobQueueWait(
		documentdomain.ProcessingJob{
			CreatedAt: createdAt,
			StartedAt: &startedAt,
		},
		claimedAt,
	)

	if wait != 1500*time.Millisecond {
		t.Fatalf("queue wait = %s, want 1.5s", wait)
	}
}

func TestProcessingJobQueueWaitFallsBackAndRejectsInvalidTime(t *testing.T) {
	createdAt := time.Date(2026, 8, 20, 9, 0, 0, 0, time.UTC)

	if wait := processingJobQueueWait(
		(documentdomain.ProcessingJob{CreatedAt: createdAt}),
		createdAt.Add(2*time.Second),
	); wait != 2*time.Second {
		t.Fatalf("fallback queue wait = %s, want 2s", wait)
	}

	invalidStartedAt := createdAt.Add(-time.Second)
	if wait := processingJobQueueWait(
		(documentdomain.ProcessingJob{
			CreatedAt: createdAt,
			StartedAt: &invalidStartedAt,
		}),
		createdAt,
	); wait != 0 {
		t.Fatalf("invalid queue wait = %s, want 0", wait)
	}
}

// recordingProcessingJobEventObserver 是 Worker 测试使用的 Fake 观察器。
// 它不写真实日志，只按发生顺序保存事件，供测试核对生命周期。
type recordingProcessingJobEventObserver struct {
	mutex  sync.Mutex
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
	o.mutex.Lock()
	defer o.mutex.Unlock()
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
