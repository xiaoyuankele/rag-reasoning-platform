package embedding

import (
	"context"
	"errors"
	"testing"
	"time"
)

type fakeInterruptedEmbeddingJobRecoverer struct {
	requeueFunc func(context.Context, time.Time, string) (int64, error)
	calls       int
}

func (f *fakeInterruptedEmbeddingJobRecoverer) RequeueInterruptedEmbeddingJobs(
	ctx context.Context,
	recoveredAt time.Time,
	errorMessage string,
) (int64, error) {
	f.calls++
	return f.requeueFunc(ctx, recoveredAt, errorMessage)
}

func TestInterruptedEmbeddingJobRecoveryServiceRecoversJobs(t *testing.T) {
	expectedContext := context.Background()
	recoveredAt := time.Date(2026, 8, 12, 12, 0, 0, 0, time.UTC)
	jobs := &fakeInterruptedEmbeddingJobRecoverer{
		requeueFunc: func(
			ctx context.Context,
			actualRecoveredAt time.Time,
			errorMessage string,
		) (int64, error) {
			if ctx != expectedContext {
				t.Fatal("recovery repository received a different context")
			}
			if !actualRecoveredAt.Equal(recoveredAt) {
				t.Fatalf("recovered at = %v, want %v", actualRecoveredAt, recoveredAt)
			}
			if errorMessage != safeInterruptedEmbeddingMessage {
				t.Fatalf("recovery message = %q, want %q", errorMessage, safeInterruptedEmbeddingMessage)
			}
			return 2, nil
		},
	}

	service, err := newInterruptedJobRecoveryService(
		jobs,
		func() time.Time { return recoveredAt },
	)
	if err != nil {
		t.Fatalf("newInterruptedJobRecoveryService() error = %v", err)
	}

	recoveredCount, err := service.Recover(expectedContext)
	if err != nil {
		t.Fatalf("Recover() error = %v, want nil", err)
	}
	if recoveredCount != 2 || jobs.calls != 1 {
		t.Fatalf("Recover() result = (%d, %d calls), want (2, 1 call)", recoveredCount, jobs.calls)
	}
}

func TestInterruptedEmbeddingJobRecoveryServiceWrapsRepositoryError(t *testing.T) {
	repositoryError := errors.New("database unavailable")
	jobs := &fakeInterruptedEmbeddingJobRecoverer{
		requeueFunc: func(context.Context, time.Time, string) (int64, error) {
			return 0, repositoryError
		},
	}
	service, err := NewInterruptedJobRecoveryService(jobs)
	if err != nil {
		t.Fatalf("NewInterruptedJobRecoveryService() error = %v", err)
	}

	recoveredCount, err := service.Recover(context.Background())
	if !errors.Is(err, repositoryError) {
		t.Fatalf("Recover() error = %v, want wrapped repository error", err)
	}
	if recoveredCount != 0 || jobs.calls != 1 {
		t.Fatalf("Recover() result = (%d, %d calls), want (0, 1 call)", recoveredCount, jobs.calls)
	}
}

func TestNewInterruptedEmbeddingJobRecoveryServiceRejectsMissingDependencies(t *testing.T) {
	_, err := NewInterruptedJobRecoveryService(nil)
	if !errors.Is(err, ErrEmbeddingRecoveryDependencies) {
		t.Fatalf("NewInterruptedJobRecoveryService() error = %v, want ErrEmbeddingRecoveryDependencies", err)
	}
}
