package pythonprocessor

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"runtime"
	"testing"
	"time"
)

// TestProcessorCallsRealPythonCLI 验证生产 Processor，而不只是协议辅助函数，
// 能够真实启动项目中的 Python CLI 并接收结构化失败响应。
func TestProcessorCallsRealPythonCLI(t *testing.T) {
	if os.Getenv("RUN_PYTHON_TESTS") != "1" {
		t.Skip("set RUN_PYTHON_TESTS=1 to run Go/Python integration tests")
	}

	pythonExecutable := os.Getenv("PYTHON_EXECUTABLE")
	if pythonExecutable == "" {
		pythonExecutable = "python"
	}

	_, currentFile, _, ok := runtime.Caller(0)
	if !ok {
		t.Fatal("resolve integration test file path")
	}
	repositoryRoot := filepath.Clean(
		filepath.Join(filepath.Dir(currentFile), "..", "..", "..", ".."),
	)
	pythonSourceRoot := filepath.Join(repositoryRoot, "ai", "src")
	sourcePath := filepath.Join(t.TempDir(), "processor-test.pdf")
	if err := os.WriteFile(sourcePath, []byte("%PDF-1.7\n%%EOF"), 0o600); err != nil {
		t.Fatalf("write processor test source: %v", err)
	}

	resolver := &fakeStoredFilePathResolver{absolutePath: sourcePath}
	processor, err := NewProcessor(
		resolver,
		pythonExecutable,
		pythonSourceRoot,
		50*1024*1024,
		500,
	)
	if err != nil {
		t.Fatalf("NewProcessor() error = %v, want nil", err)
	}

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	_, err = processor.Process(ctx, testDocument())
	var failure *ProcessingFailureError
	if !errors.As(err, &failure) {
		t.Fatalf("Process() error = %v, want ProcessingFailureError", err)
	}
	if failure.Code != "invalid_content" {
		t.Fatalf("failure code = %q, want invalid_content", failure.Code)
	}
}
