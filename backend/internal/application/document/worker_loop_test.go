package document

import (
	"context"
	"errors"
	"testing"
	"time"
)

type fakeRunOnceWorker struct {
	runOnceFunc func(context.Context) (bool, error)
	runCalls    int
}

func (f *fakeRunOnceWorker) RunOnce(
	ctx context.Context,
) (bool, error) {
	f.runCalls++
	return f.runOnceFunc(ctx)
}

func TestNewWorkerLoopRejectsInvalidDependencies(t *testing.T) {
	validWorker := &fakeRunOnceWorker{
		runOnceFunc: func(context.Context) (bool, error) {
			return false, nil
		},
	}
	validReporter := func(error) {}

	tests := []struct {
		name         string
		worker       runOnceWorker
		pollInterval time.Duration
		reportError  func(error)
		wantErr      error
	}{
		{
			name:         "worker is required",
			pollInterval: time.Second,
			reportError:  validReporter,
			wantErr:      ErrWorkerLoopWorkerRequired,
		},
		{
			name:         "zero poll interval is rejected",
			worker:       validWorker,
			pollInterval: 0,
			reportError:  validReporter,
			wantErr:      ErrInvalidWorkerPollInterval,
		},
		{
			name:         "negative poll interval is rejected",
			worker:       validWorker,
			pollInterval: -time.Second,
			reportError:  validReporter,
			wantErr:      ErrInvalidWorkerPollInterval,
		},
		{
			name:         "error reporter is required",
			worker:       validWorker,
			pollInterval: time.Second,
			wantErr:      ErrWorkerErrorReporterRequired,
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			loop, err := NewWorkerLoop(
				test.worker,
				test.pollInterval,
				test.reportError,
			)

			if loop != nil {
				t.Fatalf("NewWorkerLoop() loop = %+v, want nil", loop)
			}
			if !errors.Is(err, test.wantErr) {
				t.Fatalf(
					"NewWorkerLoop() error = %v, want %v",
					err,
					test.wantErr,
				)
			}
		})
	}
}

func TestNewWorkerLoopStoresDependencies(t *testing.T) {
	worker := &fakeRunOnceWorker{
		runOnceFunc: func(context.Context) (bool, error) {
			return false, nil
		},
	}
	pollInterval := 250 * time.Millisecond
	reported := false
	reporter := func(error) {
		reported = true
	}

	loop, err := NewWorkerLoop(worker, pollInterval, reporter)
	if err != nil {
		t.Fatalf("NewWorkerLoop() error = %v, want nil", err)
	}
	if loop == nil {
		t.Fatal("NewWorkerLoop() loop = nil, want WorkerLoop")
	}
	if loop.worker != worker {
		t.Fatal("NewWorkerLoop() did not store worker")
	}
	if loop.pollInterval != pollInterval {
		t.Fatalf(
			"pollInterval = %v, want %v",
			loop.pollInterval,
			pollInterval,
		)
	}

	loop.reportError(errors.New("test error"))
	if !reported {
		t.Fatal("NewWorkerLoop() did not store error reporter")
	}
}

func TestWorkerLoopStopsBeforeRunningWhenContextIsCanceled(t *testing.T) {
	worker := &fakeRunOnceWorker{
		runOnceFunc: func(context.Context) (bool, error) {
			t.Fatal("RunOnce() must not be called after cancellation")
			return false, nil
		},
	}
	loop, err := NewWorkerLoop(worker, time.Hour, func(error) {})
	if err != nil {
		t.Fatalf("NewWorkerLoop() error = %v, want nil", err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	loop.Run(ctx)

	if worker.runCalls != 0 {
		t.Fatalf("RunOnce() calls = %d, want 0", worker.runCalls)
	}
}

func TestWorkerLoopImmediatelyContinuesAfterHandledJob(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	worker := &fakeRunOnceWorker{}
	worker.runOnceFunc = func(context.Context) (bool, error) {
		if worker.runCalls == 1 {
			return true, nil
		}

		cancel()
		return false, nil
	}
	loop, err := NewWorkerLoop(worker, time.Hour, func(error) {})
	if err != nil {
		t.Fatalf("NewWorkerLoop() error = %v, want nil", err)
	}

	loop.Run(ctx)

	if worker.runCalls != 2 {
		t.Fatalf("RunOnce() calls = %d, want 2", worker.runCalls)
	}
}

func TestWorkerLoopWaitsWhenQueueIsEmpty(t *testing.T) {
	firstRun := make(chan struct{})
	worker := &fakeRunOnceWorker{
		runOnceFunc: func(context.Context) (bool, error) {
			close(firstRun)
			return false, nil
		},
	}
	loop, err := NewWorkerLoop(worker, time.Hour, func(error) {})
	if err != nil {
		t.Fatalf("NewWorkerLoop() error = %v, want nil", err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() {
		loop.Run(ctx)
		close(done)
	}()

	<-firstRun
	cancel()
	<-done

	if worker.runCalls != 1 {
		t.Fatalf("RunOnce() calls = %d, want 1", worker.runCalls)
	}
}

func TestWorkerLoopReportsErrorAndStopsAfterCancellation(t *testing.T) {
	runErr := errors.New("run failure")
	worker := &fakeRunOnceWorker{
		runOnceFunc: func(context.Context) (bool, error) {
			return false, runErr
		},
	}
	ctx, cancel := context.WithCancel(context.Background())
	reportedErrors := make([]error, 0, 1)
	loop, err := NewWorkerLoop(
		worker,
		time.Hour,
		func(err error) {
			reportedErrors = append(reportedErrors, err)
			cancel()
		},
	)
	if err != nil {
		t.Fatalf("NewWorkerLoop() error = %v, want nil", err)
	}

	loop.Run(ctx)

	if len(reportedErrors) != 1 {
		t.Fatalf("reported errors = %d, want 1", len(reportedErrors))
	}
	if !errors.Is(reportedErrors[0], runErr) {
		t.Fatalf("reported error = %v, want run error", reportedErrors[0])
	}
	if worker.runCalls != 1 {
		t.Fatalf("RunOnce() calls = %d, want 1", worker.runCalls)
	}
}

func TestWorkerLoopDoesNotReportExpectedCancellation(t *testing.T) {
	started := make(chan struct{})
	worker := &fakeRunOnceWorker{
		runOnceFunc: func(ctx context.Context) (bool, error) {
			close(started)
			<-ctx.Done()
			return false, ctx.Err()
		},
	}
	reportedErrors := make([]error, 0)
	loop, err := NewWorkerLoop(
		worker,
		time.Hour,
		func(err error) {
			reportedErrors = append(reportedErrors, err)
		},
	)
	if err != nil {
		t.Fatalf("NewWorkerLoop() error = %v, want nil", err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() {
		loop.Run(ctx)
		close(done)
	}()

	<-started
	cancel()
	<-done

	if len(reportedErrors) != 0 {
		t.Fatalf("reported errors = %d, want 0", len(reportedErrors))
	}
}
