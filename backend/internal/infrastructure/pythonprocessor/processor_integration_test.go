package pythonprocessor

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"testing"
	"time"

	documentdomain "rag-reasoning-platform/backend/internal/domain/document"
)

// TestProcessorCallsRealPythonCLI 验证生产 Processor 能够启动项目中的
// Python CLI，并把真实文字 PDF 转换为带物理页码的统一文本块。
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
	aiRoot := filepath.Join(repositoryRoot, "ai")
	pythonSourceRoot := filepath.Join(repositoryRoot, "ai", "src")
	sourcePath := filepath.Join(t.TempDir(), "processor-test.pdf")
	writeTextPDFTestFixture(
		t,
		pythonExecutable,
		aiRoot,
		sourcePath,
	)

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

	result, err := processor.Process(ctx, testDocument())
	if err != nil {
		t.Fatalf("Process() error = %v, want nil", err)
	}
	if len(result.Chunks) != 2 {
		t.Fatalf("chunk count = %d, want 2", len(result.Chunks))
	}

	assertTextPDFChunk(
		t,
		result.Chunks[0],
		0,
		"first page content",
		1,
	)
	assertTextPDFChunk(
		t,
		result.Chunks[1],
		1,
		"second page content",
		2,
	)
}

func writeTextPDFTestFixture(
	t *testing.T,
	pythonExecutable string,
	aiRoot string,
	outputPath string,
) {
	t.Helper()

	const script = `
from pathlib import Path
import sys

from tests.pdf_test_support import write_text_pdf

write_text_pdf(
    Path(sys.argv[1]),
    ["first page content", "second page content"],
)
`

	command := exec.Command(
		pythonExecutable,
		"-c",
		script,
		outputPath,
	)
	command.Dir = aiRoot
	command.Env = append(
		os.Environ(),
		"PYTHONPATH="+aiRoot,
	)
	output, err := command.CombinedOutput()
	if err != nil {
		t.Fatalf(
			"generate text PDF fixture: %v; output=%q",
			err,
			string(output),
		)
	}
}

func assertTextPDFChunk(
	t *testing.T,
	chunk documentdomain.ChunkInput,
	wantIndex int,
	wantContent string,
	wantPage int,
) {
	t.Helper()

	if chunk.Index != wantIndex || chunk.Content != wantContent {
		t.Fatalf(
			"chunk = %+v, want index %d and content %q",
			chunk,
			wantIndex,
			wantContent,
		)
	}
	if chunk.PageStart == nil || chunk.PageEnd == nil {
		t.Fatalf("chunk page range = %+v, want page %d", chunk, wantPage)
	}
	if *chunk.PageStart != wantPage || *chunk.PageEnd != wantPage {
		t.Fatalf(
			"chunk page range = %d-%d, want %d-%d",
			*chunk.PageStart,
			*chunk.PageEnd,
			wantPage,
			wantPage,
		)
	}
}
