package document

import (
	"context"
	"errors"
	"testing"

	documentdomain "rag-reasoning-platform/backend/internal/domain/document"
)

type fakeProcessingJobClaimer struct {
	claimNextFunc      func(context.Context) (documentdomain.ProcessingJob, error)
	markSucceededFunc  func(context.Context, int64) error
	markFailedFunc     func(context.Context, int64, string) error
	claimNextCalls     int
	markSucceededCalls int
	markFailedCalls    int
}

func (f *fakeProcessingJobClaimer) ClaimNextProcessingJob(
	ctx context.Context,
) (documentdomain.ProcessingJob, error) {
	f.claimNextCalls++
	return f.claimNextFunc(ctx)
}

func (f *fakeProcessingJobClaimer) MarkProcessingJobSucceeded(
	ctx context.Context,
	jobID int64,
) error {
	f.markSucceededCalls++
	if f.markSucceededFunc == nil {
		return nil
	}
	return f.markSucceededFunc(ctx, jobID)
}

func (f *fakeProcessingJobClaimer) MarkProcessingJobFailed(
	ctx context.Context,
	jobID int64,
	errorMessage string,
) error {
	f.markFailedCalls++
	if f.markFailedFunc == nil {
		return nil
	}
	return f.markFailedFunc(ctx, jobID, errorMessage)
}

func TestWorkerClaimNextReturnsIdleWhenQueueIsEmpty(t *testing.T) {
	claimer := &fakeProcessingJobClaimer{
		claimNextFunc: func(
			context.Context,
		) (documentdomain.ProcessingJob, error) {
			return documentdomain.ProcessingJob{},
				documentdomain.ErrNoQueuedProcessingJob
		},
	}
	worker := NewWorker(claimer, nil, nil, nil, testWorkerProcessingTimeout)

	job, claimed, err := worker.ClaimNext(context.Background())

	if err != nil {
		t.Fatalf("ClaimNext() error = %v, want nil", err)
	}
	if claimed {
		t.Fatal("ClaimNext() claimed = true, want false")
	}
	if job != (documentdomain.ProcessingJob{}) {
		t.Fatalf("ClaimNext() job = %+v, want zero value", job)
	}
	if claimer.claimNextCalls != 1 {
		t.Fatalf(
			"ClaimNextProcessingJob() calls = %d, want 1",
			claimer.claimNextCalls,
		)
	}
}

func TestWorkerClaimNextReturnsClaimedJob(t *testing.T) {
	expectedJob := documentdomain.ProcessingJob{
		ID:           17,
		DocumentID:   7,
		Status:       documentdomain.ProcessingJobStatusProcessing,
		AttemptCount: 1,
	}
	claimer := &fakeProcessingJobClaimer{
		claimNextFunc: func(
			context.Context,
		) (documentdomain.ProcessingJob, error) {
			return expectedJob, nil
		},
	}
	worker := NewWorker(claimer, nil, nil, nil, testWorkerProcessingTimeout)

	job, claimed, err := worker.ClaimNext(context.Background())

	if err != nil {
		t.Fatalf("ClaimNext() error = %v, want nil", err)
	}
	if !claimed {
		t.Fatal("ClaimNext() claimed = false, want true")
	}
	if job != expectedJob {
		t.Fatalf(
			"ClaimNext() job = %+v, want %+v",
			job,
			expectedJob,
		)
	}
	if claimer.claimNextCalls != 1 {
		t.Fatalf(
			"ClaimNextProcessingJob() calls = %d, want 1",
			claimer.claimNextCalls,
		)
	}
}

func TestWorkerClaimNextPreservesUnexpectedError(t *testing.T) {
	databaseError := errors.New("database unavailable")
	claimer := &fakeProcessingJobClaimer{
		claimNextFunc: func(
			context.Context,
		) (documentdomain.ProcessingJob, error) {
			return documentdomain.ProcessingJob{}, databaseError
		},
	}
	worker := NewWorker(claimer, nil, nil, nil, testWorkerProcessingTimeout)

	job, claimed, err := worker.ClaimNext(context.Background())

	if !errors.Is(err, databaseError) {
		t.Fatalf(
			"ClaimNext() error = %v, want wrapped database error",
			err,
		)
	}
	if claimed {
		t.Fatal("ClaimNext() claimed = true, want false")
	}
	if job != (documentdomain.ProcessingJob{}) {
		t.Fatalf("ClaimNext() job = %+v, want zero value", job)
	}
	if claimer.claimNextCalls != 1 {
		t.Fatalf(
			"ClaimNextProcessingJob() calls = %d, want 1",
			claimer.claimNextCalls,
		)
	}
}
