package filestorage

import (
	"context"
	"io"
	"strings"
	"testing"

	documentapplication "rag-reasoning-platform/backend/internal/application/document"
)

// storageFactory 让同一套行为测试可以复用于 LocalStorage 和未来对象存储实现。
// 每个子测试都创建独立存储，避免用例之间共享文件或对象状态。
type storageFactory func(t *testing.T) documentapplication.FileStorage

func runDocumentFileStorageContract(t *testing.T, create storageFactory) {
	t.Helper()

	t.Run("save metadata and open content", func(t *testing.T) {
		storage := create(t)
		storedFile, err := storage.Save(
			context.Background(),
			"contract.md",
			strings.NewReader("# shared storage contract\n"),
		)
		if err != nil {
			t.Fatalf("Save() error = %v, want nil", err)
		}
		if storedFile.StoragePath == "" {
			t.Fatal("Save() storage path is empty")
		}
		if storedFile.MIMEType != "text/markdown" {
			t.Fatalf("Save() MIME = %q, want text/markdown", storedFile.MIMEType)
		}
		if storedFile.SizeBytes != int64(len("# shared storage contract\n")) {
			t.Fatalf("Save() size = %d, want exact content size", storedFile.SizeBytes)
		}
		if len(storedFile.SHA256) != 64 {
			t.Fatalf("Save() SHA-256 length = %d, want 64", len(storedFile.SHA256))
		}

		openedFile, err := storage.Open(
			context.Background(),
			storedFile.StoragePath,
		)
		if err != nil {
			t.Fatalf("Open() error = %v, want nil", err)
		}
		content, readErr := io.ReadAll(openedFile)
		closeErr := openedFile.Close()
		if readErr != nil {
			t.Fatalf("read opened file: %v", readErr)
		}
		if closeErr != nil {
			t.Fatalf("close opened file: %v", closeErr)
		}
		if string(content) != "# shared storage contract\n" {
			t.Fatalf("opened content = %q, want original content", content)
		}
	})

	t.Run("keys are unique and delete is idempotent", func(t *testing.T) {
		storage := create(t)
		first, err := storage.Save(
			context.Background(),
			"same-name.txt",
			strings.NewReader("first"),
		)
		if err != nil {
			t.Fatalf("save first file: %v", err)
		}
		second, err := storage.Save(
			context.Background(),
			"same-name.txt",
			strings.NewReader("second"),
		)
		if err != nil {
			t.Fatalf("save second file: %v", err)
		}
		if first.StoragePath == second.StoragePath {
			t.Fatalf("storage keys are equal: %q", first.StoragePath)
		}

		if err := storage.Delete(context.Background(), first.StoragePath); err != nil {
			t.Fatalf("Delete() error = %v, want nil", err)
		}
		if err := storage.Delete(context.Background(), first.StoragePath); err != nil {
			t.Fatalf("second Delete() error = %v, want idempotent nil", err)
		}
		if _, err := storage.Open(context.Background(), first.StoragePath); err == nil {
			t.Fatal("Open() after Delete() error = nil, want failure")
		}

		if err := storage.Delete(context.Background(), second.StoragePath); err != nil {
			t.Fatalf("delete second file: %v", err)
		}
	})
}

func TestLocalStorageSatisfiesDocumentFileStorageContract(t *testing.T) {
	runDocumentFileStorageContract(t, func(t *testing.T) documentapplication.FileStorage {
		t.Helper()
		storage, err := NewLocalStorage(t.TempDir(), 1024*1024)
		if err != nil {
			t.Fatalf("NewLocalStorage() error = %v, want nil", err)
		}
		return storage
	})
}
