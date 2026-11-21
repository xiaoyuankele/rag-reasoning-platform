package main

import (
	"os"
	"path/filepath"
	"testing"

	"rag-reasoning-platform/backend/internal/config"
)

func TestWriteApplicationReadyFileSupportsDisabledMarker(t *testing.T) {
	cleanup, err := writeApplicationReadyFile(
		"",
		config.ApplicationRoleDocumentWorker,
	)
	if err != nil {
		t.Fatalf("writeApplicationReadyFile() error = %v, want nil", err)
	}
	if err := cleanup(); err != nil {
		t.Fatalf("cleanup() error = %v, want nil", err)
	}
}

func TestWriteApplicationReadyFileWritesRoleAndCleansUp(t *testing.T) {
	path := filepath.Join(t.TempDir(), "worker.ready")

	cleanup, err := writeApplicationReadyFile(
		path,
		config.ApplicationRoleEmbeddingWorker,
	)
	if err != nil {
		t.Fatalf("writeApplicationReadyFile() error = %v, want nil", err)
	}

	content, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("ReadFile() error = %v", err)
	}
	if string(content) != "embedding-worker\n" {
		t.Fatalf("ready file content = %q, want role and newline", content)
	}

	if err := cleanup(); err != nil {
		t.Fatalf("cleanup() error = %v, want nil", err)
	}
	if _, err := os.Stat(path); !os.IsNotExist(err) {
		t.Fatalf("Stat() error = %v, want file removed", err)
	}
}

func TestWriteApplicationReadyFileRejectsMissingParent(t *testing.T) {
	path := filepath.Join(t.TempDir(), "missing", "worker.ready")

	cleanup, err := writeApplicationReadyFile(
		path,
		config.ApplicationRoleAnswerWorker,
	)
	if err == nil {
		t.Fatal("writeApplicationReadyFile() error = nil, want error")
	}
	if cleanup != nil {
		t.Fatal("writeApplicationReadyFile() cleanup is non-nil on failure")
	}
}
