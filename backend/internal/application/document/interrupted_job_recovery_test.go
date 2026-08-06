package document

import (
	"context"
	"errors"
	"testing"
)

type fakeInterruptedProcessingJobRecoverer struct {
	markFailedFunc func(context.Context, string) (int64, error)
	calls          int
}

func (f *fakeInterruptedProcessingJobRecoverer) MarkInterruptedProcessingJobsFailed(
	ctx context.Context,
	errorMessage string,
) (int64, error) {
	f.calls++
	return f.markFailedFunc(ctx, errorMessage)
}

func TestInterruptedJobRecoveryServiceRecoversJobs(t *testing.T) {
	expectedContext := context.Background()
	jobs := &fakeInterruptedProcessingJobRecoverer{
		markFailedFunc: func(
			ctx context.Context,
			errorMessage string,
		) (int64, error) {
			if ctx != expectedContext {
				t.Fatal("recovery repository received a different context")
			}
			if errorMessage != safeInterruptedProcessingMessage {
				t.Fatalf(
					"recovery message = %q, want %q",
					errorMessage,
					safeInterruptedProcessingMessage,
				)
			}
			return 2, nil
		},
	}
	service := NewInterruptedJobRecoveryService(jobs)

	recoveredCount, err := service.Recover(expectedContext)

	if err != nil {
		t.Fatalf("Recover() error = %v, want nil", err)
	}
	if recoveredCount != 2 {
		t.Fatalf("Recover() count = %d, want 2", recoveredCount)
	}
	if jobs.calls != 1 {
		t.Fatalf("repository calls = %d, want 1", jobs.calls)
	}
}

func TestInterruptedJobRecoveryServiceWrapsRepositoryError(t *testing.T) {
	repositoryError := errors.New("database unavailable")
	jobs := &fakeInterruptedProcessingJobRecoverer{
		markFailedFunc: func(
			context.Context,
			string,
		) (int64, error) {
			return 0, repositoryError
		},
	}
	service := NewInterruptedJobRecoveryService(jobs)

	recoveredCount, err := service.Recover(context.Background())

	if !errors.Is(err, repositoryError) {
		t.Fatalf(
			"Recover() error = %v, want wrapped repository error",
			err,
		)
	}
	if recoveredCount != 0 {
		t.Fatalf("Recover() count = %d, want 0", recoveredCount)
	}
	if jobs.calls != 1 {
		t.Fatalf("repository calls = %d, want 1", jobs.calls)
	}
}
