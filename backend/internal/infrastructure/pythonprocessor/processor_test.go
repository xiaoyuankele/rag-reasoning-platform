package pythonprocessor

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	documentdomain "rag-reasoning-platform/backend/internal/domain/document"
)

const helperProcessEnvironment = "GO_WANT_PYTHON_PROCESSOR_HELPER"

type fakeStoredFileMaterializer struct {
	absolutePath string
	err          error
	releaseErr   error
	storagePath  string
	releaseCount int
}

func (r *fakeStoredFileMaterializer) Materialize(
	ctx context.Context,
	storagePath string,
) (string, func() error, error) {
	if err := ctx.Err(); err != nil {
		return "", nil, err
	}
	r.storagePath = storagePath
	if r.err != nil {
		return "", nil, r.err
	}

	return r.absolutePath, func() error {
		r.releaseCount++
		return r.releaseErr
	}, nil
}

// TestPythonProcessorHelperProcess 不是普通业务测试，而是由下面的测试作为
// 子进程重新启动当前 go test 可执行文件。这样无需伪造 exec.Cmd，也不依赖
// 测试机器已经安装某个特定的 Python 包。
func TestPythonProcessorHelperProcess(t *testing.T) {
	if os.Getenv(helperProcessEnvironment) != "1" {
		return
	}

	mode := os.Getenv("PYTHON_PROCESSOR_HELPER_MODE")
	if strings.HasPrefix(mode, "stream_") {
		runPythonProcessorStreamHelper(mode)
		os.Exit(0)
	}

	var request processRequest
	if err := json.NewDecoder(os.Stdin).Decode(&request); err != nil {
		fmt.Fprintf(os.Stderr, "decode helper request: %v", err)
		os.Exit(2)
	}

	switch mode {
	case "success":
		writeHelperResponse(processResponse{
			ContractVersion: contractVersionV1,
			RequestID:       request.RequestID,
			Status:          processStatusSucceeded,
			Chunks: []processChunk{
				{Index: 0, Content: "first chunk"},
				{Index: 1, Content: "second chunk"},
			},
		})

	case "structured_failure":
		retryable := false
		writeHelperResponse(processResponse{
			ContractVersion: contractVersionV1,
			RequestID:       request.RequestID,
			Status:          processStatusFailed,
			Error: &processFailure{
				Code:      "parse_failed",
				Message:   "test parser rejected content",
				Retryable: &retryable,
			},
		})

	case "invalid_response":
		_, _ = io.WriteString(os.Stdout, "not-json")

	case "crash":
		_, _ = io.WriteString(os.Stderr, "helper process crashed")
		os.Exit(7)

	case "wait":
		time.Sleep(30 * time.Second)

	case "large_stdout":
		_, _ = io.WriteString(os.Stdout, strings.Repeat("x", 4096))

	case "large_stderr":
		_, _ = io.WriteString(os.Stderr, strings.Repeat("x", 4096))
		writeHelperResponse(processResponse{
			ContractVersion: contractVersionV1,
			RequestID:       request.RequestID,
			Status:          processStatusSucceeded,
			Chunks: []processChunk{
				{Index: 0, Content: "content"},
			},
		})

	default:
		_, _ = io.WriteString(os.Stderr, "unknown helper mode")
		os.Exit(3)
	}

	os.Exit(0)
}

func runPythonProcessorStreamHelper(mode string) {
	decoder := json.NewDecoder(os.Stdin)
	for {
		var request processRequest
		if err := decoder.Decode(&request); errors.Is(err, io.EOF) {
			return
		} else if err != nil {
			fmt.Fprintf(os.Stderr, "decode stream helper request: %v", err)
			os.Exit(2)
		}

		switch mode {
		case "stream_success":
			writeHelperResponse(processResponse{
				ContractVersion: contractVersionV1,
				RequestID:       request.RequestID,
				Status:          processStatusSucceeded,
				Chunks: []processChunk{
					{Index: 0, Content: request.Document.OriginalName},
				},
			})
		case "stream_crash":
			_, _ = io.WriteString(os.Stderr, "stream helper process crashed")
			os.Exit(7)
		case "stream_wait":
			time.Sleep(30 * time.Second)
		case "stream_invalid_response":
			_, _ = io.WriteString(os.Stdout, "not-json\n")
		case "stream_large_stdout":
			_, _ = io.WriteString(os.Stdout, strings.Repeat("x", 4096)+"\n")
		default:
			_, _ = io.WriteString(os.Stderr, "unknown stream helper mode")
			os.Exit(3)
		}
	}
}

func writeHelperResponse(response processResponse) {
	if err := json.NewEncoder(os.Stdout).Encode(response); err != nil {
		fmt.Fprintf(os.Stderr, "encode helper response: %v", err)
		os.Exit(4)
	}
}

func newTestProcessor(
	t *testing.T,
	mode string,
) (*Processor, *fakeStoredFileMaterializer) {
	t.Helper()

	sourcePath := filepath.Join(t.TempDir(), "document.pdf")
	resolver := &fakeStoredFileMaterializer{absolutePath: sourcePath}
	processor, err := NewProcessor(
		resolver,
		os.Args[0],
		t.TempDir(),
		50*1024*1024,
		500,
	)
	if err != nil {
		t.Fatalf("NewProcessor() error = %v, want nil", err)
	}

	processor.newCommand = func(ctx context.Context) *exec.Cmd {
		command := exec.CommandContext(
			ctx,
			os.Args[0],
			"-test.run=^TestPythonProcessorHelperProcess$",
		)
		command.Env = append(
			os.Environ(),
			helperProcessEnvironment+"=1",
			"PYTHON_PROCESSOR_HELPER_MODE="+mode,
		)
		return command
	}

	return processor, resolver
}

func newTestProcessPool(
	t *testing.T,
	mode string,
	poolSize int,
	maxDocuments int,
) (*ProcessPool, *fakeStoredFileMaterializer) {
	t.Helper()

	sourcePath := filepath.Join(t.TempDir(), "document.pdf")
	resolver := &fakeStoredFileMaterializer{absolutePath: sourcePath}
	pool, err := NewProcessPool(
		resolver,
		os.Args[0],
		t.TempDir(),
		50*1024*1024,
		500,
		poolSize,
		maxDocuments,
	)
	if err != nil {
		t.Fatalf("NewProcessPool() error = %v, want nil", err)
	}

	for _, worker := range pool.workers {
		worker.newCommand = newStreamHelperCommandFactory(mode)
	}
	return pool, resolver
}

func newStreamHelperCommandFactory(mode string) streamCommandFactory {
	return func() *exec.Cmd {
		command := exec.Command(
			os.Args[0],
			"-test.run=^TestPythonProcessorHelperProcess$",
		)
		command.Env = append(
			os.Environ(),
			helperProcessEnvironment+"=1",
			"PYTHON_PROCESSOR_HELPER_MODE="+mode,
		)
		return command
	}
}

func testDocument() documentdomain.Document {
	return documentdomain.Document{
		ID:           42,
		OriginalName: "research.pdf",
		StoragePath:  "documents/document-42.pdf",
		MIMEType:     "application/pdf",
	}
}

func TestNewProcessorValidatesDependencies(t *testing.T) {
	validResolver := &fakeStoredFileMaterializer{}
	validExecutable := os.Args[0]
	validSourceRoot := t.TempDir()

	tests := []struct {
		name       string
		resolver   StoredFileMaterializer
		executable string
		sourceRoot string
		wantErr    error
	}{
		{
			name:       "source materializer is required",
			executable: validExecutable,
			sourceRoot: validSourceRoot,
			wantErr:    ErrSourceMaterializerRequired,
		},
		{
			name:       "executable is required",
			resolver:   validResolver,
			sourceRoot: validSourceRoot,
			wantErr:    ErrPythonExecutableRequired,
		},
		{
			name:       "source root is required",
			resolver:   validResolver,
			executable: validExecutable,
			wantErr:    ErrPythonSourceRootRequired,
		},
		{
			name:       "executable must exist",
			resolver:   validResolver,
			executable: "definitely-missing-python-executable",
			sourceRoot: validSourceRoot,
			wantErr:    exec.ErrNotFound,
		},
		{
			name:       "source root must be a directory",
			resolver:   validResolver,
			executable: validExecutable,
			sourceRoot: filepath.Join(validSourceRoot, "missing"),
			wantErr:    os.ErrNotExist,
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			processor, err := NewProcessor(
				test.resolver,
				test.executable,
				test.sourceRoot,
				50*1024*1024,
				500,
			)

			if processor != nil {
				t.Fatal("NewProcessor() returned processor, want nil")
			}
			if !errors.Is(err, test.wantErr) {
				t.Fatalf(
					"NewProcessor() error = %v, want %v",
					err,
					test.wantErr,
				)
			}
		})
	}
}

func TestProcessorProcessReturnsChunks(t *testing.T) {
	processor, resolver := newTestProcessor(t, "success")

	result, err := processor.Process(context.Background(), testDocument())
	if err != nil {
		t.Fatalf("Process() error = %v, want nil", err)
	}
	if resolver.storagePath != testDocument().StoragePath {
		t.Fatalf(
			"resolved storage path = %q, want %q",
			resolver.storagePath,
			testDocument().StoragePath,
		)
	}
	if resolver.releaseCount != 1 {
		t.Fatalf("materialized source release count = %d, want 1", resolver.releaseCount)
	}
	if len(result.Chunks) != 2 {
		t.Fatalf("chunk count = %d, want 2", len(result.Chunks))
	}
	if result.Chunks[0].Index != 0 || result.Chunks[0].Content != "first chunk" {
		t.Fatalf("first chunk = %+v, want index 0 and expected content", result.Chunks[0])
	}
	if result.Chunks[1].Index != 1 || result.Chunks[1].Content != "second chunk" {
		t.Fatalf("second chunk = %+v, want index 1 and expected content", result.Chunks[1])
	}
}

func TestProcessorProcessPreservesStructuredFailure(t *testing.T) {
	processor, _ := newTestProcessor(t, "structured_failure")

	result, err := processor.Process(context.Background(), testDocument())
	var failure *ProcessingFailureError
	if !errors.As(err, &failure) {
		t.Fatalf("Process() error = %v, want ProcessingFailureError", err)
	}
	if failure.Code != "parse_failed" {
		t.Fatalf("failure code = %q, want parse_failed", failure.Code)
	}
	if failure.Retryable {
		t.Fatal("failure retryable = true, want false")
	}
	if len(result.Chunks) != 0 {
		t.Fatalf("result chunks = %+v, want empty", result.Chunks)
	}
}

func TestProcessorProcessRejectsInvalidResponse(t *testing.T) {
	processor, _ := newTestProcessor(t, "invalid_response")

	_, err := processor.Process(context.Background(), testDocument())
	if !errors.Is(err, ErrInvalidProcessResponse) {
		t.Fatalf(
			"Process() error = %v, want ErrInvalidProcessResponse",
			err,
		)
	}
}

func TestProcessorProcessReportsProcessFailure(t *testing.T) {
	processor, _ := newTestProcessor(t, "crash")

	_, err := processor.Process(context.Background(), testDocument())
	if !errors.Is(err, ErrPythonProcessFailed) {
		t.Fatalf("Process() error = %v, want ErrPythonProcessFailed", err)
	}
	if !strings.Contains(err.Error(), "helper process crashed") {
		t.Fatalf("Process() error = %q, want bounded stderr", err)
	}
}

func TestProcessorProcessHonorsContextDeadline(t *testing.T) {
	processor, _ := newTestProcessor(t, "wait")
	ctx, cancel := context.WithTimeout(context.Background(), 100*time.Millisecond)
	defer cancel()

	_, err := processor.Process(ctx, testDocument())
	if !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("Process() error = %v, want context.DeadlineExceeded", err)
	}
}

func TestProcessorProcessRejectsOversizedOutput(t *testing.T) {
	tests := []struct {
		name string
		mode string
	}{
		{name: "stdout", mode: "large_stdout"},
		{name: "stderr", mode: "large_stderr"},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			processor, _ := newTestProcessor(t, test.mode)
			processor.maxStdoutBytes = 128
			processor.maxStderrBytes = 128

			_, err := processor.Process(context.Background(), testDocument())
			if !errors.Is(err, ErrPythonProcessOutputTooLarge) {
				t.Fatalf(
					"Process() error = %v, want ErrPythonProcessOutputTooLarge",
					err,
				)
			}
		})
	}
}

func TestProcessorProcessPreservesMaterializationError(t *testing.T) {
	expectedError := errors.New("materialization failed")
	processor, resolver := newTestProcessor(t, "success")
	resolver.err = expectedError

	_, err := processor.Process(context.Background(), testDocument())
	if !errors.Is(err, expectedError) {
		t.Fatalf("Process() error = %v, want %v", err, expectedError)
	}
	if resolver.releaseCount != 0 {
		t.Fatalf("release count = %d, want 0", resolver.releaseCount)
	}
}

func TestProcessorProcessReturnsMaterializedSourceCleanupError(t *testing.T) {
	expectedError := errors.New("remove temporary source failed")
	processor, materializer := newTestProcessor(t, "success")
	materializer.releaseErr = expectedError

	result, err := processor.Process(context.Background(), testDocument())
	if !errors.Is(err, expectedError) {
		t.Fatalf("Process() error = %v, want %v", err, expectedError)
	}
	if len(result.Chunks) != 0 {
		t.Fatalf("Process() chunks = %+v, want empty", result.Chunks)
	}
	if materializer.releaseCount != 1 {
		t.Fatalf("release count = %d, want 1", materializer.releaseCount)
	}
}

func TestLimitedBuffer(t *testing.T) {
	buffer := newLimitedBuffer(5)

	written, err := buffer.Write([]byte("abcdef"))
	if !errors.Is(err, errLimitedBufferFull) {
		t.Fatalf("Write() error = %v, want errLimitedBufferFull", err)
	}
	if written != 5 {
		t.Fatalf("Write() bytes = %d, want 5", written)
	}
	if buffer.String() != "abcde" {
		t.Fatalf("buffer = %q, want abcde", buffer.String())
	}
	if !buffer.Exceeded() {
		t.Fatal("Exceeded() = false, want true")
	}
}
