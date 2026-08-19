package embedding

import (
	"context"
	"errors"
	"testing"

	accessdomain "rag-reasoning-platform/backend/internal/domain/access"
	embeddingdomain "rag-reasoning-platform/backend/internal/domain/embedding"
)

// fakeScopedJobCanceler 只模拟 CancelService 依赖的一个领域端口。
type fakeScopedJobCanceler struct {
	cancelFunc  func(context.Context, accessdomain.OwnerScope, int64) (embeddingdomain.Job, error)
	cancelCalls int
}

func (f *fakeScopedJobCanceler) CancelEmbeddingJob(
	ctx context.Context,
	scope accessdomain.OwnerScope,
	jobID int64,
) (embeddingdomain.Job, error) {
	f.cancelCalls++
	return f.cancelFunc(ctx, scope, jobID)
}

func TestCancelServiceRejectsInvalidJobID(t *testing.T) {
	jobs := &fakeScopedJobCanceler{
		cancelFunc: func(context.Context, accessdomain.OwnerScope, int64) (embeddingdomain.Job, error) {
			t.Fatal("CancelEmbeddingJob() must not be called")
			return embeddingdomain.Job{}, nil
		},
	}
	service := NewCancelService(jobs)

	_, err := service.Cancel(context.Background(), testEmbeddingOwnerScope(t), 0)

	if !errors.Is(err, ErrInvalidEmbeddingJobID) {
		t.Fatalf("Cancel() error = %v, want ErrInvalidEmbeddingJobID", err)
	}
	if jobs.cancelCalls != 0 {
		t.Fatalf("CancelEmbeddingJob() calls = %d, want 0", jobs.cancelCalls)
	}
}

func TestCancelServicePreservesRepositoryResult(t *testing.T) {
	expectedJob := embeddingdomain.Job{ID: 19, DocumentID: 7, Status: embeddingdomain.JobStatusCanceled}
	jobs := &fakeScopedJobCanceler{
		cancelFunc: func(
			_ context.Context,
			scope accessdomain.OwnerScope,
			jobID int64,
		) (embeddingdomain.Job, error) {
			if scope.OwnerUserID() != testEmbeddingOwnerUserID {
				t.Fatalf("owner = %d, want %d", scope.OwnerUserID(), testEmbeddingOwnerUserID)
			}
			if jobID != expectedJob.ID {
				t.Fatalf("job ID = %d, want %d", jobID, expectedJob.ID)
			}
			return expectedJob, nil
		},
	}
	service := NewCancelService(jobs)

	actualJob, err := service.Cancel(context.Background(), testEmbeddingOwnerScope(t), expectedJob.ID)

	if err != nil {
		t.Fatalf("Cancel() error = %v, want nil", err)
	}
	if actualJob != expectedJob {
		t.Fatalf("Cancel() job = %+v, want %+v", actualJob, expectedJob)
	}
}

func TestCancelServicePreservesDomainError(t *testing.T) {
	jobs := &fakeScopedJobCanceler{
		cancelFunc: func(context.Context, accessdomain.OwnerScope, int64) (embeddingdomain.Job, error) {
			return embeddingdomain.Job{}, embeddingdomain.ErrJobProcessingCannotCancel
		},
	}
	service := NewCancelService(jobs)

	_, err := service.Cancel(context.Background(), testEmbeddingOwnerScope(t), 19)

	if !errors.Is(err, embeddingdomain.ErrJobProcessingCannotCancel) {
		t.Fatalf("Cancel() error = %v, want ErrJobProcessingCannotCancel", err)
	}
}
