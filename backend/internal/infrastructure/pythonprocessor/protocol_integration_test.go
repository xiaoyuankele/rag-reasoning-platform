package pythonprocessor

import (
	"bytes"
	"context"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"testing"
	"time"

	documentdomain "rag-reasoning-platform/backend/internal/domain/document"
)

// TestPythonCLIContractRoundTrip 真实启动 Python CLI，验证 Go 编码的请求
// 能被 Python 读取，并且 Python 响应能被 Go 重新解码。
func TestPythonCLIContractRoundTrip(t *testing.T) {
	if os.Getenv("RUN_PYTHON_TESTS") != "1" {
		t.Skip("set RUN_PYTHON_TESTS=1 to run Go/Python contract tests")
	}

	pythonExecutable := os.Getenv("PYTHON_EXECUTABLE")
	if pythonExecutable == "" {
		pythonExecutable = "python"
	}
	if _, err := exec.LookPath(pythonExecutable); err != nil {
		t.Fatalf("find Python executable %q: %v", pythonExecutable, err)
	}

	_, currentFile, _, ok := runtime.Caller(0)
	if !ok {
		t.Fatal("resolve integration test file path")
	}
	repositoryRoot := filepath.Clean(
		filepath.Join(filepath.Dir(currentFile), "..", "..", "..", ".."),
	)
	aiRoot := filepath.Join(repositoryRoot, "ai")
	pythonSourceRoot := filepath.Join(aiRoot, "src")

	sourcePath := filepath.Join(t.TempDir(), "contract-test.pdf")
	if err := os.WriteFile(sourcePath, []byte("%PDF-1.7\n%%EOF"), 0o600); err != nil {
		t.Fatalf("write contract test source: %v", err)
	}

	request, err := newProcessRequest(
		"job-contract-test",
		documentdomain.Document{
			ID:           42,
			OriginalName: "contract-test.pdf",
			MIMEType:     "application/pdf",
		},
		sourcePath,
		1000,
		50*1024*1024,
		500,
	)
	if err != nil {
		t.Fatalf("new process request: %v", err)
	}

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	var stdin bytes.Buffer
	if err := encodeProcessRequest(ctx, &stdin, request); err != nil {
		t.Fatalf("encode process request: %v", err)
	}

	command := exec.CommandContext(
		ctx,
		pythonExecutable,
		"-m",
		"rag_ai.document_processor_cli",
	)
	command.Dir = aiRoot
	command.Env = append(
		os.Environ(),
		"PYTHONPATH="+pythonSourceRoot,
	)
	command.Stdin = &stdin

	var stdout bytes.Buffer
	var stderr bytes.Buffer
	command.Stdout = &stdout
	command.Stderr = &stderr

	if err := command.Run(); err != nil {
		t.Fatalf(
			"run Python CLI: %v; stderr=%q",
			err,
			stderr.String(),
		)
	}
	if stderr.Len() != 0 {
		t.Fatalf("Python CLI stderr = %q, want empty", stderr.String())
	}

	result, err := decodeProcessResponse(
		ctx,
		&stdout,
		request.RequestID,
	)
	var failure *ProcessingFailureError
	if !errors.As(err, &failure) {
		t.Fatalf("decode response error = %v, want ProcessingFailureError", err)
	}
	if failure.Code != "invalid_content" {
		t.Fatalf("failure code = %q, want invalid_content", failure.Code)
	}
	if failure.Retryable {
		t.Fatal("invalid content failure must not be retryable")
	}
	if len(result.Chunks) != 0 {
		t.Fatalf("result chunks = %+v, want empty", result.Chunks)
	}
}
