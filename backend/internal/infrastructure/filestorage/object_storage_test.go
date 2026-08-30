package filestorage

import (
	"bytes"
	"context"
	"errors"
	"io"
	"os"
	"path/filepath"
	"sync"
	"testing"

	applicationdocument "rag-reasoning-platform/backend/internal/application/document"
)

type fakeStoredObject struct {
	content  []byte
	metadata ObjectMetadata
}

// fakeObjectClient 是零费用对象客户端。它只在测试进程内保存字节，
// 但严格使用和未来 COS/S3 适配器相同的 ObjectClient 方法。
type fakeObjectClient struct {
	mu                  sync.Mutex
	objects             map[string]fakeStoredObject
	putErr              error
	getErr              error
	deleteErr           error
	storeBeforePutError bool
	returnNilReader     bool
	putCalls            int
	getCalls            int
	deleteCalls         int
}

func newFakeObjectClient() *fakeObjectClient {
	return &fakeObjectClient{objects: make(map[string]fakeStoredObject)}
}

func (c *fakeObjectClient) PutObject(
	ctx context.Context,
	key string,
	content io.Reader,
	metadata ObjectMetadata,
) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	data, err := io.ReadAll(content)
	if err != nil {
		return err
	}

	c.mu.Lock()
	defer c.mu.Unlock()
	c.putCalls++
	if c.putErr == nil || c.storeBeforePutError {
		c.objects[key] = fakeStoredObject{
			content:  append([]byte(nil), data...),
			metadata: metadata,
		}
	}
	return c.putErr
}

func (c *fakeObjectClient) GetObject(
	ctx context.Context,
	key string,
) (io.ReadCloser, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}

	c.mu.Lock()
	defer c.mu.Unlock()
	c.getCalls++
	if c.getErr != nil {
		return nil, c.getErr
	}
	object, exists := c.objects[key]
	if !exists {
		return nil, ErrObjectNotFound
	}
	if c.returnNilReader {
		return nil, nil
	}

	return io.NopCloser(bytes.NewReader(append([]byte(nil), object.content...))), nil
}

func (c *fakeObjectClient) DeleteObject(
	ctx context.Context,
	key string,
) error {
	if err := ctx.Err(); err != nil {
		return err
	}

	c.mu.Lock()
	defer c.mu.Unlock()
	c.deleteCalls++
	if c.deleteErr != nil {
		return c.deleteErr
	}
	if _, exists := c.objects[key]; !exists {
		return ErrObjectNotFound
	}
	delete(c.objects, key)
	return nil
}

func (c *fakeObjectClient) object(key string) (fakeStoredObject, bool) {
	c.mu.Lock()
	defer c.mu.Unlock()
	object, exists := c.objects[key]
	return object, exists
}

func (c *fakeObjectClient) objectCount() int {
	c.mu.Lock()
	defer c.mu.Unlock()
	return len(c.objects)
}

func TestNewObjectStorageRejectsInvalidConfiguration(t *testing.T) {
	client := newFakeObjectClient()
	tests := []struct {
		name             string
		client           ObjectClient
		stagingDirectory string
		maxSizeBytes     int64
		wantErr          error
	}{
		{
			name:             "client is required",
			stagingDirectory: t.TempDir(),
			maxSizeBytes:     1024,
			wantErr:          ErrObjectClientRequired,
		},
		{
			name:         "staging directory is required",
			client:       client,
			maxSizeBytes: 1024,
			wantErr:      ErrObjectStagingDirectoryRequired,
		},
		{
			name:             "maximum size must be positive",
			client:           client,
			stagingDirectory: t.TempDir(),
			wantErr:          ErrInvalidMaxFileSize,
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			_, err := NewObjectStorage(
				test.client,
				test.stagingDirectory,
				test.maxSizeBytes,
			)
			if !errors.Is(err, test.wantErr) {
				t.Fatalf("NewObjectStorage() error = %v, want %v", err, test.wantErr)
			}
		})
	}
}

func TestObjectStorageSatisfiesDocumentFileStorageContract(t *testing.T) {
	runDocumentFileStorageContract(t, func(t *testing.T) applicationdocument.FileStorage {
		t.Helper()
		storage, err := NewObjectStorage(
			newFakeObjectClient(),
			t.TempDir(),
			1024*1024,
		)
		if err != nil {
			t.Fatalf("NewObjectStorage() error = %v, want nil", err)
		}
		return storage
	})
}

func TestObjectStorageSaveUploadsTrustedMetadataAndCleansStaging(t *testing.T) {
	client := newFakeObjectClient()
	stagingDirectory := t.TempDir()
	storage, err := NewObjectStorage(client, stagingDirectory, 1024)
	if err != nil {
		t.Fatalf("create object storage: %v", err)
	}
	content := []byte("%PDF-1.7\nobject content")

	storedFile, err := storage.Save(
		context.Background(),
		"paper.PDF",
		bytes.NewReader(content),
	)
	if err != nil {
		t.Fatalf("Save() error = %v, want nil", err)
	}
	object, exists := client.object(storedFile.StoragePath)
	if !exists {
		t.Fatalf("object %q was not uploaded", storedFile.StoragePath)
	}
	if !bytes.Equal(object.content, content) {
		t.Fatalf("object content = %q, want %q", object.content, content)
	}
	if object.metadata.ContentType != storedFile.MIMEType ||
		object.metadata.ContentLength != storedFile.SizeBytes ||
		object.metadata.SHA256 != storedFile.SHA256 {
		t.Fatalf(
			"object metadata = %+v, stored file = %+v",
			object.metadata,
			storedFile,
		)
	}
	assertDirectoryEmpty(t, stagingDirectory)
}

func TestObjectStorageSaveCompensatesPartialRemoteFailure(t *testing.T) {
	putErr := errors.New("remote upload connection reset")
	client := newFakeObjectClient()
	client.putErr = putErr
	client.storeBeforePutError = true
	stagingDirectory := t.TempDir()
	storage, err := NewObjectStorage(client, stagingDirectory, 1024)
	if err != nil {
		t.Fatalf("create object storage: %v", err)
	}

	storedFile, err := storage.Save(
		context.Background(),
		"paper.pdf",
		bytes.NewReader([]byte("%PDF-1.7\npartial object")),
	)
	if !errors.Is(err, putErr) {
		t.Fatalf("Save() error = %v, want %v", err, putErr)
	}
	if storedFile != (applicationdocument.StoredFile{}) {
		t.Fatalf("Save() stored file = %+v, want zero value", storedFile)
	}
	if client.objectCount() != 0 {
		t.Fatalf("object count after compensation = %d, want 0", client.objectCount())
	}
	if client.deleteCalls != 1 {
		t.Fatalf("DeleteObject() calls = %d, want 1", client.deleteCalls)
	}
	assertDirectoryEmpty(t, stagingDirectory)
}

func TestObjectStorageMaterializeDownloadsAndReleasesOnlyLocalCopy(t *testing.T) {
	client := newFakeObjectClient()
	stagingDirectory := t.TempDir()
	storage, err := NewObjectStorage(client, stagingDirectory, 1024)
	if err != nil {
		t.Fatalf("create object storage: %v", err)
	}
	content := []byte("%PDF-1.7\nmaterialized object")
	storedFile, err := storage.Save(
		context.Background(),
		"paper.pdf",
		bytes.NewReader(content),
	)
	if err != nil {
		t.Fatalf("save object: %v", err)
	}

	localPath, release, err := storage.Materialize(
		context.Background(),
		storedFile.StoragePath,
	)
	if err != nil {
		t.Fatalf("Materialize() error = %v, want nil", err)
	}
	if !filepath.IsAbs(localPath) || filepath.Ext(localPath) != ".pdf" {
		t.Fatalf("materialized path = %q, want absolute .pdf path", localPath)
	}
	localContent, err := os.ReadFile(localPath)
	if err != nil {
		t.Fatalf("read materialized file: %v", err)
	}
	if !bytes.Equal(localContent, content) {
		t.Fatalf("materialized content = %q, want %q", localContent, content)
	}

	if err := release(); err != nil {
		t.Fatalf("release materialized file: %v", err)
	}
	if err := release(); err != nil {
		t.Fatalf("second release must be idempotent: %v", err)
	}
	if _, err := os.Stat(localPath); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("materialized file still exists or stat failed: %v", err)
	}
	if _, exists := client.object(storedFile.StoragePath); !exists {
		t.Fatal("release deleted the formal object")
	}
}

func TestObjectStorageMaterializeRejectsOversizedRemoteObject(t *testing.T) {
	client := newFakeObjectClient()
	client.objects["documents/document-remote.pdf"] = fakeStoredObject{
		content: []byte("%PDF-12345"),
	}
	stagingDirectory := t.TempDir()
	storage, err := NewObjectStorage(client, stagingDirectory, 8)
	if err != nil {
		t.Fatalf("create object storage: %v", err)
	}

	localPath, release, err := storage.Materialize(
		context.Background(),
		"documents/document-remote.pdf",
	)
	if !errors.Is(err, applicationdocument.ErrFileTooLarge) {
		t.Fatalf("Materialize() error = %v, want ErrFileTooLarge", err)
	}
	if localPath != "" || release != nil {
		t.Fatalf("Materialize() returned path %q and release %v", localPath, release != nil)
	}
	assertDirectoryEmpty(t, stagingDirectory)
}

func TestObjectStorageNormalizesNotFoundAndRejectsInvalidKeys(t *testing.T) {
	client := newFakeObjectClient()
	storage, err := NewObjectStorage(client, t.TempDir(), 1024)
	if err != nil {
		t.Fatalf("create object storage: %v", err)
	}

	_, err = storage.Open(
		context.Background(),
		"documents/document-missing.pdf",
	)
	if !errors.Is(err, os.ErrNotExist) || !errors.Is(err, ErrObjectNotFound) {
		t.Fatalf("Open() error = %v, want os.ErrNotExist and ErrObjectNotFound", err)
	}
	if err := storage.Delete(
		context.Background(),
		"documents/document-missing.pdf",
	); err != nil {
		t.Fatalf("Delete() missing object error = %v, want nil", err)
	}

	invalidKeys := []string{
		"",
		" ../outside.pdf",
		"../outside.pdf",
		"documents/nested/file.pdf",
		"documents\\file.pdf",
		"documents/file.exe",
		"/documents/file.pdf",
	}
	for _, key := range invalidKeys {
		t.Run(key, func(t *testing.T) {
			if err := storage.Delete(context.Background(), key); !errors.Is(
				err,
				ErrInvalidStoragePath,
			) {
				t.Fatalf("Delete(%q) error = %v, want ErrInvalidStoragePath", key, err)
			}
		})
	}
}

func TestObjectStorageOperationsHonorCanceledContext(t *testing.T) {
	client := newFakeObjectClient()
	storage, err := NewObjectStorage(client, t.TempDir(), 1024)
	if err != nil {
		t.Fatalf("create object storage: %v", err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	if _, err := storage.Save(
		ctx,
		"paper.pdf",
		bytes.NewReader([]byte("%PDF-1.7")),
	); !errors.Is(err, context.Canceled) {
		t.Fatalf("Save() error = %v, want context.Canceled", err)
	}
	if _, err := storage.Open(
		ctx,
		"documents/document-canceled.pdf",
	); !errors.Is(err, context.Canceled) {
		t.Fatalf("Open() error = %v, want context.Canceled", err)
	}
	if err := storage.Delete(
		ctx,
		"documents/document-canceled.pdf",
	); !errors.Is(err, context.Canceled) {
		t.Fatalf("Delete() error = %v, want context.Canceled", err)
	}
	if _, _, err := storage.Materialize(
		ctx,
		"documents/document-canceled.pdf",
	); !errors.Is(err, context.Canceled) {
		t.Fatalf("Materialize() error = %v, want context.Canceled", err)
	}
}

func TestObjectStorageOpenRejectsNilSuccessfulReader(t *testing.T) {
	client := newFakeObjectClient()
	client.objects["documents/document-nil.pdf"] = fakeStoredObject{
		content: []byte("%PDF-1.7"),
	}
	client.returnNilReader = true
	storage, err := NewObjectStorage(client, t.TempDir(), 1024)
	if err != nil {
		t.Fatalf("create object storage: %v", err)
	}

	reader, err := storage.Open(
		context.Background(),
		"documents/document-nil.pdf",
	)
	if reader != nil || !errors.Is(err, ErrObjectReaderRequired) {
		t.Fatalf("Open() reader = %v, error = %v", reader, err)
	}
}

func assertDirectoryEmpty(t *testing.T, directory string) {
	t.Helper()
	entries, err := os.ReadDir(directory)
	if err != nil {
		t.Fatalf("read directory %q: %v", directory, err)
	}
	if len(entries) != 0 {
		t.Fatalf("directory %q contains %d entries, want 0", directory, len(entries))
	}
}
