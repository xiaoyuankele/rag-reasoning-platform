package pythonprocessor

import (
	"context"
	"errors"
	"os"
	"os/exec"
	"sync/atomic"
	"testing"
	"time"
)

func TestNewProcessPoolRejectsInvalidConfiguration(t *testing.T) {
	validResolver := &fakeStoredFileMaterializer{}
	tests := []struct {
		name         string
		poolSize     int
		maxDocuments int
		wantErr      error
	}{
		{
			name:         "zero pool size",
			poolSize:     0,
			maxDocuments: 20,
			wantErr:      ErrInvalidPythonProcessPoolConfiguration,
		},
		{
			name:         "pool size above maximum",
			poolSize:     5,
			maxDocuments: 20,
			wantErr:      ErrInvalidPythonProcessPoolConfiguration,
		},
		{
			name:         "zero max documents",
			poolSize:     2,
			maxDocuments: 0,
			wantErr:      ErrInvalidPythonProcessPoolConfiguration,
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			pool, err := NewProcessPool(
				validResolver,
				os.Args[0],
				t.TempDir(),
				50*1024*1024,
				500,
				test.poolSize,
				test.maxDocuments,
			)
			if pool != nil {
				t.Fatal("NewProcessPool() returned pool, want nil")
			}
			if !errors.Is(err, test.wantErr) {
				t.Fatalf("NewProcessPool() error = %v, want %v", err, test.wantErr)
			}
		})
	}
}

func TestProcessPoolReusesAndRecyclesProcess(t *testing.T) {
	pool, materializer := newTestProcessPool(t, "stream_success", 1, 2)
	t.Cleanup(func() {
		if err := pool.Close(); err != nil {
			t.Errorf("Close() error = %v, want nil", err)
		}
	})

	for documentID := int64(1); documentID <= 2; documentID++ {
		document := testDocument()
		document.ID = documentID
		result, err := pool.Process(context.Background(), document)
		if err != nil {
			t.Fatalf("Process(document %d) error = %v, want nil", documentID, err)
		}
		if len(result.Chunks) != 1 || result.Chunks[0].Content != document.OriginalName {
			t.Fatalf("Process(document %d) chunks = %+v", documentID, result.Chunks)
		}
	}

	worker := pool.workers[0]
	if worker.starts != 1 {
		t.Fatalf("process starts after two documents = %d, want 1", worker.starts)
	}
	if worker.command != nil {
		t.Fatal("process must be recycled after reaching max documents")
	}

	document := testDocument()
	document.ID = 3
	if _, err := pool.Process(context.Background(), document); err != nil {
		t.Fatalf("Process(document 3) error = %v, want nil", err)
	}
	if worker.starts != 2 {
		t.Fatalf("process starts after recycle = %d, want 2", worker.starts)
	}
	if materializer.releaseCount != 3 {
		t.Fatalf(
			"materialized source release count = %d, want 3",
			materializer.releaseCount,
		)
	}
}

func TestProcessPoolReplacesCrashedProcessForNextRequest(t *testing.T) {
	pool, _ := newTestProcessPool(t, "stream_success", 1, 20)
	t.Cleanup(func() { _ = pool.Close() })

	worker := pool.workers[0]
	var commandCount atomic.Int32
	worker.newCommand = func() *exec.Cmd {
		mode := "stream_success"
		if commandCount.Add(1) == 1 {
			mode = "stream_crash"
		}
		return newStreamHelperCommandFactory(mode)()
	}

	_, err := pool.Process(context.Background(), testDocument())
	if !errors.Is(err, ErrPythonProcessFailed) {
		t.Fatalf("first Process() error = %v, want ErrPythonProcessFailed", err)
	}

	document := testDocument()
	document.ID = 43
	result, err := pool.Process(context.Background(), document)
	if err != nil {
		t.Fatalf("second Process() error = %v, want nil", err)
	}
	if len(result.Chunks) != 1 {
		t.Fatalf("second Process() chunks = %+v, want one chunk", result.Chunks)
	}
	if worker.starts != 2 {
		t.Fatalf("process starts = %d, want 2", worker.starts)
	}
}

func TestProcessPoolCancelsRequestAndRestartsProcess(t *testing.T) {
	pool, _ := newTestProcessPool(t, "stream_success", 1, 20)
	t.Cleanup(func() { _ = pool.Close() })

	worker := pool.workers[0]
	var commandCount atomic.Int32
	worker.newCommand = func() *exec.Cmd {
		mode := "stream_success"
		if commandCount.Add(1) == 1 {
			mode = "stream_wait"
		}
		return newStreamHelperCommandFactory(mode)()
	}

	ctx, cancel := context.WithTimeout(context.Background(), 100*time.Millisecond)
	defer cancel()
	_, err := pool.Process(ctx, testDocument())
	if !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("timed Process() error = %v, want context deadline", err)
	}

	document := testDocument()
	document.ID = 43
	if _, err := pool.Process(context.Background(), document); err != nil {
		t.Fatalf("Process() after cancellation error = %v, want nil", err)
	}
	if worker.starts != 2 {
		t.Fatalf("process starts after cancellation = %d, want 2", worker.starts)
	}
}

func TestProcessPoolRejectsOversizedStreamResponse(t *testing.T) {
	pool, _ := newTestProcessPool(t, "stream_large_stdout", 1, 20)
	t.Cleanup(func() { _ = pool.Close() })
	pool.workers[0].maxStdout = 128

	_, err := pool.Process(context.Background(), testDocument())
	if !errors.Is(err, ErrPythonProcessOutputTooLarge) {
		t.Fatalf("Process() error = %v, want ErrPythonProcessOutputTooLarge", err)
	}
}

func TestProcessPoolCloseRejectsNewRequests(t *testing.T) {
	pool, _ := newTestProcessPool(t, "stream_success", 1, 20)
	if err := pool.Close(); err != nil {
		t.Fatalf("Close() error = %v, want nil", err)
	}
	if err := pool.Close(); err != nil {
		t.Fatalf("second Close() error = %v, want nil", err)
	}

	_, err := pool.Process(context.Background(), testDocument())
	if !errors.Is(err, ErrPythonProcessPoolClosed) {
		t.Fatalf("Process() error = %v, want ErrPythonProcessPoolClosed", err)
	}
}
