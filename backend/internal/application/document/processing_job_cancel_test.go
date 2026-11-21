package document

import (
	"context"
	"errors"
	"testing"

	accessdomain "rag-reasoning-platform/backend/internal/domain/access"
	documentdomain "rag-reasoning-platform/backend/internal/domain/document"
)

// fakeProcessingJobCanceler 只模拟取消服务依赖的领域端口。
type fakeProcessingJobCanceler struct {
	cancelFunc  func(context.Context, accessdomain.OwnerScope, int64) (documentdomain.ProcessingJob, error)
	cancelCalls int
}

func (f *fakeProcessingJobCanceler) CancelProcessingJob(
	ctx context.Context,
	scope accessdomain.OwnerScope,
	jobID int64,
) (documentdomain.ProcessingJob, error) {
	f.cancelCalls++
	return f.cancelFunc(ctx, scope, jobID)
}

func TestProcessingJobCancelServiceRejectsInvalidID(t *testing.T) {
	jobs := &fakeProcessingJobCanceler{
		cancelFunc: func(context.Context, accessdomain.OwnerScope, int64) (documentdomain.ProcessingJob, error) {
			t.Fatal("CancelProcessingJob() must not be called")
			return documentdomain.ProcessingJob{}, nil
		},
	}
	service := NewProcessingJobCancelService(jobs)

	_, err := service.Cancel(context.Background(), testOwnerScope(t), 0)

	if !errors.Is(err, ErrInvalidProcessingJobID) {
		t.Fatalf("Cancel() error = %v, want ErrInvalidProcessingJobID", err)
	}
	if jobs.cancelCalls != 0 {
		t.Fatalf("CancelProcessingJob() calls = %d, want 0", jobs.cancelCalls)
	}
}

func TestProcessingJobCancelServicePreservesRepositoryResult(t *testing.T) {
	expectedJob := documentdomain.ProcessingJob{
		ID:         19,
		DocumentID: 7,
		Status:     documentdomain.ProcessingJobStatusCanceled,
	}
	jobs := &fakeProcessingJobCanceler{
		cancelFunc: func(
			_ context.Context,
			scope accessdomain.OwnerScope,
			jobID int64,
		) (documentdomain.ProcessingJob, error) {
			if scope.OwnerUserID() != testOwnerUserID {
				t.Fatalf("owner = %d, want %d", scope.OwnerUserID(), testOwnerUserID)
			}
			if jobID != expectedJob.ID {
				t.Fatalf("job ID = %d, want %d", jobID, expectedJob.ID)
			}
			return expectedJob, nil
		},
	}

	actualJob, err := NewProcessingJobCancelService(jobs).Cancel(
		context.Background(),
		testOwnerScope(t),
		expectedJob.ID,
	)

	if err != nil {
		t.Fatalf("Cancel() error = %v, want nil", err)
	}
	if actualJob != expectedJob {
		t.Fatalf("Cancel() job = %+v, want %+v", actualJob, expectedJob)
	}
}

func TestProcessingJobCancelServicePreservesDomainError(t *testing.T) {
	jobs := &fakeProcessingJobCanceler{
		cancelFunc: func(context.Context, accessdomain.OwnerScope, int64) (documentdomain.ProcessingJob, error) {
			return documentdomain.ProcessingJob{},
				documentdomain.ErrProcessingJobProcessingCannotCancel
		},
	}

	_, err := NewProcessingJobCancelService(jobs).Cancel(
		context.Background(),
		testOwnerScope(t),
		19,
	)

	if !errors.Is(err, documentdomain.ErrProcessingJobProcessingCannotCancel) {
		t.Fatalf("Cancel() error = %v, want processing conflict", err)
	}
}
