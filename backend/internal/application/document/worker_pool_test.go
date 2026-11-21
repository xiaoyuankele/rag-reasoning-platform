package document

import (
	"context"
	"errors"
	"sync/atomic"
	"testing"
	"time"
)

type blockingWorkerLoop struct {
	started chan struct{}
	active  atomic.Int32
	maximum atomic.Int32
}

func newBlockingWorkerLoop(capacity int) *blockingWorkerLoop {
	return &blockingWorkerLoop{
		started: make(chan struct{}, capacity),
	}
}

func (l *blockingWorkerLoop) Run(ctx context.Context) {
	active := l.active.Add(1)
	for {
		maximum := l.maximum.Load()
		if active <= maximum || l.maximum.CompareAndSwap(maximum, active) {
			break
		}
	}

	l.started <- struct{}{}
	<-ctx.Done()
	l.active.Add(-1)
}

func TestNewWorkerPoolRejectsInvalidConfiguration(t *testing.T) {
	loop := newBlockingWorkerLoop(1)

	pool, err := NewWorkerPool(nil, 1)
	if !errors.Is(err, ErrWorkerPoolLoopRequired) || pool != nil {
		t.Fatalf("NewWorkerPool(nil, 1) = %+v, %v", pool, err)
	}

	pool, err = NewWorkerPool(loop, 0)
	if !errors.Is(err, ErrInvalidWorkerPoolConcurrency) || pool != nil {
		t.Fatalf("NewWorkerPool(loop, 0) = %+v, %v", pool, err)
	}
}

func TestWorkerPoolRunsConfiguredNumberAndWaitsForShutdown(t *testing.T) {
	const concurrency = 2
	loop := newBlockingWorkerLoop(concurrency)
	pool, err := NewWorkerPool(loop, concurrency)
	if err != nil {
		t.Fatalf("NewWorkerPool() error = %v, want nil", err)
	}

	ctx, cancel := context.WithCancel(context.Background())
	runReturned := make(chan struct{})
	go func() {
		pool.Run(ctx)
		close(runReturned)
	}()

	for range concurrency {
		select {
		case <-loop.started:
		case <-time.After(time.Second):
			t.Fatal("WorkerPool did not start configured loops")
		}
	}

	if maximum := loop.maximum.Load(); maximum != concurrency {
		t.Fatalf("maximum concurrent loops = %d, want %d", maximum, concurrency)
	}
	select {
	case <-runReturned:
		t.Fatal("WorkerPool.Run returned before context cancellation")
	default:
	}

	cancel()
	select {
	case <-runReturned:
	case <-time.After(time.Second):
		t.Fatal("WorkerPool.Run did not wait for loops to stop")
	}
	if active := loop.active.Load(); active != 0 {
		t.Fatalf("active loops after shutdown = %d, want 0", active)
	}
}
