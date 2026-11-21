package embedding

import (
	"context"
	"errors"
	"testing"
)

type fakeExpiredEmbeddingJobRecoverer struct {
	requeueFunc func(context.Context, string) (int64, error)
	calls       int
}

func (f *fakeExpiredEmbeddingJobRecoverer) RequeueExpiredEmbeddingJobs(
	ctx context.Context,
	errorMessage string,
) (int64, error) {
	f.calls++
	return f.requeueFunc(ctx, errorMessage)
}

func TestExpiredEmbeddingJobRecoveryServiceRecoversJobs(t *testing.T) {
	expectedContext := context.Background()
	jobs := &fakeExpiredEmbeddingJobRecoverer{
		requeueFunc: func(
			ctx context.Context,
			errorMessage string,
		) (int64, error) {
			if ctx != expectedContext {
				t.Fatal("recovery repository received a different context")
			}
			if errorMessage != safeExpiredEmbeddingMessage {
				t.Fatalf("recovery message = %q, want %q", errorMessage, safeExpiredEmbeddingMessage)
			}
			return 2, nil
		},
	}

	service, err := NewExpiredJobRecoveryService(jobs)
	if err != nil {
		t.Fatalf("NewExpiredJobRecoveryService() error = %v", err)
	}

	recoveredCount, err := service.Recover(expectedContext)
	if err != nil {
		t.Fatalf("Recover() error = %v, want nil", err)
	}
	if recoveredCount != 2 || jobs.calls != 1 {
		t.Fatalf("Recover() result = (%d, %d calls), want (2, 1 call)", recoveredCount, jobs.calls)
	}
}

func TestExpiredEmbeddingJobRecoveryServiceWrapsRepositoryError(t *testing.T) {
	repositoryError := errors.New("database unavailable")
	jobs := &fakeExpiredEmbeddingJobRecoverer{
		requeueFunc: func(context.Context, string) (int64, error) {
			return 0, repositoryError
		},
	}
	service, err := NewExpiredJobRecoveryService(jobs)
	if err != nil {
		t.Fatalf("NewExpiredJobRecoveryService() error = %v", err)
	}

	recoveredCount, err := service.Recover(context.Background())
	if !errors.Is(err, repositoryError) {
		t.Fatalf("Recover() error = %v, want wrapped repository error", err)
	}
	if recoveredCount != 0 || jobs.calls != 1 {
		t.Fatalf("Recover() result = (%d, %d calls), want (0, 1 call)", recoveredCount, jobs.calls)
	}
}

func TestNewExpiredEmbeddingJobRecoveryServiceRejectsMissingDependencies(t *testing.T) {
	_, err := NewExpiredJobRecoveryService(nil)
	if !errors.Is(err, ErrEmbeddingRecoveryDependencies) {
		t.Fatalf("NewExpiredJobRecoveryService() error = %v, want ErrEmbeddingRecoveryDependencies", err)
	}
}
