package filestorage

import (
	"context"
	"errors"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// trackingCloser 用于验证 contextReadCloser 是否把 Close 调用
// 原样转交给底层资源。
type trackingCloser struct {
	closed   bool
	closeErr error
}

func (c *trackingCloser) Close() error {
	c.closed = true
	return c.closeErr
}

func TestContextReadCloserReadsAndCloses(t *testing.T) {
	closer := &trackingCloser{}
	reader := &contextReadCloser{
		ctx:    context.Background(),
		reader: strings.NewReader("stored text"),
		closer: closer,
	}

	content, err := io.ReadAll(reader)
	if err != nil {
		t.Fatalf("read context reader: %v", err)
	}
	if string(content) != "stored text" {
		t.Fatalf("content = %q, want %q", content, "stored text")
	}

	if err := reader.Close(); err != nil {
		t.Fatalf("close context reader: %v", err)
	}
	if !closer.closed {
		t.Fatal("underlying closer was not closed")
	}
}

func TestContextReadCloserStopsAfterContextCancellation(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	closer := &trackingCloser{}
	reader := &contextReadCloser{
		ctx:    ctx,
		reader: strings.NewReader("must not be read"),
		closer: closer,
	}
	cancel()

	buffer := make([]byte, 16)
	bytesRead, err := reader.Read(buffer)
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("Read() error = %v, want context.Canceled", err)
	}
	if bytesRead != 0 {
		t.Fatalf("Read() bytes = %d, want 0", bytesRead)
	}

	// 资源清理不能被取消的 context 阻止。
	if err := reader.Close(); err != nil {
		t.Fatalf("Close() after cancellation error = %v, want nil", err)
	}
	if !closer.closed {
		t.Fatal("Close() after cancellation did not close the resource")
	}
}

func TestContextReadCloserPreservesCloseError(t *testing.T) {
	expectedError := errors.New("close failed")
	reader := &contextReadCloser{
		ctx:    context.Background(),
		reader: strings.NewReader("content"),
		closer: &trackingCloser{closeErr: expectedError},
	}

	if err := reader.Close(); !errors.Is(err, expectedError) {
		t.Fatalf("Close() error = %v, want %v", err, expectedError)
	}
}

func TestLocalStorageOpenReadsStoredTextFile(t *testing.T) {
	storage, err := NewLocalStorage(t.TempDir(), 1024)
	if err != nil {
		t.Fatalf("create local storage: %v", err)
	}

	expectedContent := "# 标题\n\n真实 Markdown 内容"
	storedFile, err := storage.Save(
		context.Background(),
		"document.md",
		strings.NewReader(expectedContent),
	)
	if err != nil {
		t.Fatalf("save text file: %v", err)
	}

	openedFile, err := storage.Open(
		context.Background(),
		storedFile.StoragePath,
	)
	if err != nil {
		t.Fatalf("open stored text file: %v", err)
	}
	defer func() {
		if closeErr := openedFile.Close(); closeErr != nil {
			t.Errorf("close stored text file: %v", closeErr)
		}
	}()

	content, err := io.ReadAll(openedFile)
	if err != nil {
		t.Fatalf("read stored text file: %v", err)
	}
	if string(content) != expectedContent {
		t.Fatalf("content = %q, want %q", content, expectedContent)
	}
}

func TestLocalStorageOpenRejectsInvalidPaths(t *testing.T) {
	storage, err := NewLocalStorage(t.TempDir(), 1024)
	if err != nil {
		t.Fatalf("create local storage: %v", err)
	}

	invalidPaths := []string{
		"../outside.md",
		"documents/nested/document.md",
		"other/document.md",
		"documents/document.exe",
	}

	for _, storagePath := range invalidPaths {
		t.Run(storagePath, func(t *testing.T) {
			openedFile, err := storage.Open(
				context.Background(),
				storagePath,
			)
			if openedFile != nil {
				_ = openedFile.Close()
				t.Fatal("Open() returned a file for an invalid path")
			}
			if !errors.Is(err, ErrInvalidStoragePath) {
				t.Fatalf(
					"Open(%q) error = %v, want ErrInvalidStoragePath",
					storagePath,
					err,
				)
			}
		})
	}
}

func TestLocalStorageOpenPreservesNotExist(t *testing.T) {
	storage, err := NewLocalStorage(t.TempDir(), 1024)
	if err != nil {
		t.Fatalf("create local storage: %v", err)
	}

	openedFile, err := storage.Open(
		context.Background(),
		"documents/missing.md",
	)
	if openedFile != nil {
		_ = openedFile.Close()
		t.Fatal("Open() returned a file for a missing path")
	}
	if !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("Open() error = %v, want os.ErrNotExist", err)
	}
}

func TestLocalStorageOpenHonorsContextCancellation(t *testing.T) {
	storage, err := NewLocalStorage(t.TempDir(), 1024)
	if err != nil {
		t.Fatalf("create local storage: %v", err)
	}

	storedFile, err := storage.Save(
		context.Background(),
		"document.txt",
		strings.NewReader("stored text"),
	)
	if err != nil {
		t.Fatalf("save text file: %v", err)
	}

	t.Run("canceled before open", func(t *testing.T) {
		ctx, cancel := context.WithCancel(context.Background())
		cancel()

		openedFile, err := storage.Open(ctx, storedFile.StoragePath)
		if openedFile != nil {
			_ = openedFile.Close()
			t.Fatal("Open() returned a file for a canceled context")
		}
		if !errors.Is(err, context.Canceled) {
			t.Fatalf("Open() error = %v, want context.Canceled", err)
		}
	})

	t.Run("canceled after open", func(t *testing.T) {
		ctx, cancel := context.WithCancel(context.Background())
		openedFile, err := storage.Open(ctx, storedFile.StoragePath)
		if err != nil {
			t.Fatalf("open stored file: %v", err)
		}
		cancel()

		buffer := make([]byte, 16)
		bytesRead, readErr := openedFile.Read(buffer)
		if !errors.Is(readErr, context.Canceled) {
			t.Fatalf("Read() error = %v, want context.Canceled", readErr)
		}
		if bytesRead != 0 {
			t.Fatalf("Read() bytes = %d, want 0", bytesRead)
		}
		if closeErr := openedFile.Close(); closeErr != nil {
			t.Fatalf("Close() after cancellation error = %v", closeErr)
		}
	})
}

func TestLocalStorageResolveAbsolutePath(t *testing.T) {
	rootDirectory := t.TempDir()
	storage, err := NewLocalStorage(rootDirectory, 1024)
	if err != nil {
		t.Fatalf("create local storage: %v", err)
	}

	storedFile, err := storage.Save(
		context.Background(),
		"document.pdf",
		strings.NewReader("%PDF-1.7\n%%EOF"),
	)
	if err != nil {
		t.Fatalf("save PDF: %v", err)
	}

	absolutePath, err := storage.ResolveAbsolutePath(storedFile.StoragePath)
	if err != nil {
		t.Fatalf("resolve absolute path: %v", err)
	}

	expectedPath := filepath.Join(
		rootDirectory,
		filepath.FromSlash(storedFile.StoragePath),
	)
	if absolutePath != expectedPath {
		t.Fatalf(
			"absolute path = %q, want %q",
			absolutePath,
			expectedPath,
		)
	}
}

func TestLocalStorageResolveAbsolutePathRejectsInvalidPath(t *testing.T) {
	storage, err := NewLocalStorage(t.TempDir(), 1024)
	if err != nil {
		t.Fatalf("create local storage: %v", err)
	}

	absolutePath, err := storage.ResolveAbsolutePath("../outside.pdf")
	if absolutePath != "" {
		t.Fatalf("absolute path = %q, want empty", absolutePath)
	}
	if !errors.Is(err, ErrInvalidStoragePath) {
		t.Fatalf(
			"ResolveAbsolutePath() error = %v, want ErrInvalidStoragePath",
			err,
		)
	}
}

func TestLocalStorageMaterializeReturnsManagedFileWithoutDeletingIt(t *testing.T) {
	rootDirectory := t.TempDir()
	storage, err := NewLocalStorage(rootDirectory, 1024)
	if err != nil {
		t.Fatalf("create local storage: %v", err)
	}

	storedFile, err := storage.Save(
		context.Background(),
		"document.pdf",
		strings.NewReader("%PDF-1.7\n%%EOF"),
	)
	if err != nil {
		t.Fatalf("save PDF: %v", err)
	}

	localPath, release, err := storage.Materialize(
		context.Background(),
		storedFile.StoragePath,
	)
	if err != nil {
		t.Fatalf("Materialize() error = %v, want nil", err)
	}
	if !filepath.IsAbs(localPath) {
		t.Fatalf("materialized path = %q, want absolute", localPath)
	}
	if release == nil {
		t.Fatal("Materialize() release = nil")
	}
	if err := release(); err != nil {
		t.Fatalf("release materialized file: %v", err)
	}

	// LocalStorage 返回的是正式文件，不是临时下载副本。release 只统一调用
	// 契约，不能删除仍由 documents 记录管理的文件。
	if _, err := os.Stat(localPath); err != nil {
		t.Fatalf("inspect stored file after release: %v", err)
	}
}

func TestLocalStorageMaterializeHonorsContextAndPathSafety(t *testing.T) {
	storage, err := NewLocalStorage(t.TempDir(), 1024)
	if err != nil {
		t.Fatalf("create local storage: %v", err)
	}

	t.Run("canceled context", func(t *testing.T) {
		ctx, cancel := context.WithCancel(context.Background())
		cancel()

		localPath, release, err := storage.Materialize(
			ctx,
			"documents/source.pdf",
		)
		if localPath != "" || release != nil {
			t.Fatalf(
				"Materialize() returned path %q and release %v for canceled context",
				localPath,
				release != nil,
			)
		}
		if !errors.Is(err, context.Canceled) {
			t.Fatalf("Materialize() error = %v, want context.Canceled", err)
		}
	})

	t.Run("invalid storage key", func(t *testing.T) {
		localPath, release, err := storage.Materialize(
			context.Background(),
			"../outside.pdf",
		)
		if localPath != "" || release != nil {
			t.Fatalf(
				"Materialize() returned path %q and release %v for invalid key",
				localPath,
				release != nil,
			)
		}
		if !errors.Is(err, ErrInvalidStoragePath) {
			t.Fatalf("Materialize() error = %v, want ErrInvalidStoragePath", err)
		}
	})
}
