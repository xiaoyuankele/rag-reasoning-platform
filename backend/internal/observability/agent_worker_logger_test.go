package observability

import (
	"context"
	"errors"
	"log/slog"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
)

type fakeObservedAgentWorker struct {
	handled       bool
	runErr        error
	recovered     int64
	recoveryErr   error
	runCalls      int
	recoveryCalls int
}

type concurrentObservedAgentWorker struct {
	runCalls      atomic.Int64
	recoveryCalls atomic.Int64
}

func (w *concurrentObservedAgentWorker) RunOnce(context.Context) (bool, error) {
	w.runCalls.Add(1)
	return true, nil
}

func (w *concurrentObservedAgentWorker) RecoverExpiredRuns(context.Context) (int64, error) {
	w.recoveryCalls.Add(1)
	return 1, nil
}

func (f *fakeObservedAgentWorker) RunOnce(context.Context) (bool, error) {
	f.runCalls++
	return f.handled, f.runErr
}

func (f *fakeObservedAgentWorker) RecoverExpiredRuns(context.Context) (int64, error) {
	f.recoveryCalls++
	return f.recovered, f.recoveryErr
}

func TestAgentWorkerLoggerDelegatesAndRedactsErrors(t *testing.T) {
	var output lockedAgentLogBuffer
	sentinel := errors.New("owner-42 prompt-secret evidence-secret provider-body-secret")
	worker := &fakeObservedAgentWorker{handled: true, runErr: sentinel, recovered: 3, recoveryErr: sentinel}
	logger := NewAgentWorkerLogger(worker, slog.New(slog.NewJSONHandler(&output, nil)))

	if recovered, err := logger.RecoverExpiredRuns(t.Context()); recovered != 3 || !errors.Is(err, sentinel) {
		t.Fatalf("RecoverExpiredRuns() = %d, %v", recovered, err)
	}
	if handled, err := logger.RunOnce(t.Context()); !handled || !errors.Is(err, sentinel) {
		t.Fatalf("RunOnce() = %t, %v", handled, err)
	}
	if worker.recoveryCalls != 1 || worker.runCalls != 1 {
		t.Fatalf("delegate calls recovery=%d run=%d", worker.recoveryCalls, worker.runCalls)
	}
	logs := output.String()
	if !strings.Contains(logs, `"event":"agent_worker_startup_recovery"`) ||
		!strings.Contains(logs, `"recovered_count":3`) ||
		!strings.Contains(logs, `"event":"agent_worker_iteration"`) ||
		!strings.Contains(logs, `"handled":true`) ||
		!strings.Contains(logs, `"error_category":"internal"`) {
		t.Fatalf("worker logs missing safe fields: %s", logs)
	}
	for _, forbidden := range []string{"owner-42", "prompt-secret", "evidence-secret", "provider-body-secret"} {
		if strings.Contains(logs, forbidden) {
			t.Fatalf("worker logs leaked %q: %s", forbidden, logs)
		}
	}
}

func TestAgentWorkerLoggerIsConcurrentSafe(t *testing.T) {
	var output lockedAgentLogBuffer
	worker := &concurrentObservedAgentWorker{}
	logger := NewAgentWorkerLogger(worker, slog.New(slog.NewJSONHandler(&output, nil)))
	var calls sync.WaitGroup
	for range 32 {
		calls.Add(1)
		go func() {
			defer calls.Done()
			_, _ = logger.RecoverExpiredRuns(t.Context())
			_, _ = logger.RunOnce(t.Context())
		}()
	}
	calls.Wait()
	if worker.runCalls.Load() != 32 || worker.recoveryCalls.Load() != 32 {
		t.Fatalf("concurrent delegate calls run=%d recovery=%d", worker.runCalls.Load(), worker.recoveryCalls.Load())
	}
	lines := strings.Split(strings.TrimSpace(output.String()), "\n")
	if len(lines) != 64 {
		t.Fatalf("concurrent worker log lines = %d, want 64", len(lines))
	}
}
