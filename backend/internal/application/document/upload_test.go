package document

import (
	"context"
	"errors"
	"io"
	"strings"
	"testing"

	documentdomain "rag-reasoning-platform/backend/internal/domain/document"
)

// fakeUploadRepository 是上传用例测试使用的内存假仓储。
// 它不会访问 PostgreSQL，只记录应用服务传给 Create 的数据。
type fakeUploadRepository struct {
	createFunc  func(context.Context, documentdomain.CreateInput) (documentdomain.Document, error)
	createCalls int
}

func (f *fakeUploadRepository) Create(
	ctx context.Context,
	input documentdomain.CreateInput,
) (documentdomain.Document, error) {
	f.createCalls++

	return f.createFunc(ctx, input)
}

// fakeFileStorage 是 FileStorage 的测试实现。
// 函数字段允许每个测试场景自行决定 Save 和 Delete 的行为。
type fakeFileStorage struct {
	saveFunc    func(context.Context, string, io.Reader) (StoredFile, error)
	deleteFunc  func(context.Context, string) error
	saveCalls   int
	deleteCalls int
}

func (f *fakeFileStorage) Save(
	ctx context.Context,
	originalName string,
	content io.Reader,
) (StoredFile, error) {
	f.saveCalls++

	return f.saveFunc(ctx, originalName, content)
}

func (f *fakeFileStorage) Delete(
	ctx context.Context,
	storagePath string,
) error {
	f.deleteCalls++

	return f.deleteFunc(ctx, storagePath)
}

// TestUploadServiceSavesFileAndCreatesDocument 验证上传成功的主流程：
// 先保存文件，再把可信的文件元数据交给文档仓储。
func TestUploadServiceSavesFileAndCreatesDocument(t *testing.T) {
	const originalName = "example.pdf"
	const fileContent = "%PDF-1.7\ntest document"

	storedFile := StoredFile{
		StoragePath: "documents/example.pdf",
		SizeBytes:   int64(len(fileContent)),
		SHA256:      strings.Repeat("a", 64),
	}

	expectedDocument := documentdomain.Document{
		ID:           7,
		OriginalName: originalName,
		StoragePath:  storedFile.StoragePath,
		MIMEType:     "application/pdf",
		SizeBytes:    storedFile.SizeBytes,
		SHA256:       storedFile.SHA256,
		Status:       documentdomain.StatusUploaded,
	}

	storage := &fakeFileStorage{
		saveFunc: func(
			_ context.Context,
			receivedName string,
			content io.Reader,
		) (StoredFile, error) {
			if receivedName != originalName {
				t.Fatalf(
					"expected file name %q, got %q",
					originalName,
					receivedName,
				)
			}

			receivedContent, err := io.ReadAll(content)
			if err != nil {
				t.Fatalf("read content passed to storage: %v", err)
			}

			if string(receivedContent) != fileContent {
				t.Fatalf(
					"expected content %q, got %q",
					fileContent,
					string(receivedContent),
				)
			}

			return storedFile, nil
		},
		deleteFunc: func(context.Context, string) error {
			t.Fatal("Delete must not be called when upload succeeds")

			return nil
		},
	}

	repository := &fakeUploadRepository{
		createFunc: func(
			_ context.Context,
			input documentdomain.CreateInput,
		) (documentdomain.Document, error) {
			expectedInput := documentdomain.CreateInput{
				OriginalName: originalName,
				StoragePath:  storedFile.StoragePath,
				MIMEType:     "application/pdf",
				SizeBytes:    storedFile.SizeBytes,
				SHA256:       storedFile.SHA256,
			}

			if input != expectedInput {
				t.Fatalf(
					"expected repository input %+v, got %+v",
					expectedInput,
					input,
				)
			}

			return expectedDocument, nil
		},
	}

	service := NewUploadService(repository, storage)

	createdDocument, err := service.Upload(
		context.Background(),
		UploadInput{
			OriginalName: originalName,
			Content:      strings.NewReader(fileContent),
		},
	)
	if err != nil {
		t.Fatalf("expected no error, got %v", err)
	}

	if createdDocument != expectedDocument {
		t.Fatalf(
			"expected document %+v, got %+v",
			expectedDocument,
			createdDocument,
		)
	}

	if storage.saveCalls != 1 {
		t.Fatalf("expected one Save call, got %d", storage.saveCalls)
	}

	if storage.deleteCalls != 0 {
		t.Fatalf("expected no Delete calls, got %d", storage.deleteCalls)
	}

	if repository.createCalls != 1 {
		t.Fatalf(
			"expected one repository Create call, got %d",
			repository.createCalls,
		)
	}
}

// TestUploadServiceDeletesStoredFileWhenRepositoryFails 验证补偿清理：
// 文件已经保存、但数据库写入失败时，必须删除刚保存的文件。
func TestUploadServiceDeletesStoredFileWhenRepositoryFails(t *testing.T) {
	repositoryError := errors.New("database unavailable")
	storedFile := StoredFile{
		StoragePath: "documents/orphan.pdf",
		SizeBytes:   128,
		SHA256:      strings.Repeat("b", 64),
	}

	storage := &fakeFileStorage{
		saveFunc: func(
			context.Context,
			string,
			io.Reader,
		) (StoredFile, error) {
			return storedFile, nil
		},
		deleteFunc: func(
			_ context.Context,
			storagePath string,
		) error {
			if storagePath != storedFile.StoragePath {
				t.Fatalf(
					"expected Delete path %q, got %q",
					storedFile.StoragePath,
					storagePath,
				)
			}

			return nil
		},
	}

	repository := &fakeUploadRepository{
		createFunc: func(
			context.Context,
			documentdomain.CreateInput,
		) (documentdomain.Document, error) {
			return documentdomain.Document{}, repositoryError
		},
	}

	service := NewUploadService(repository, storage)

	_, err := service.Upload(
		context.Background(),
		UploadInput{
			OriginalName: "orphan.pdf",
			Content:      strings.NewReader("%PDF-1.7"),
		},
	)

	if !errors.Is(err, repositoryError) {
		t.Fatalf("expected repository error, got %v", err)
	}

	if storage.saveCalls != 1 {
		t.Fatalf("expected one Save call, got %d", storage.saveCalls)
	}

	if repository.createCalls != 1 {
		t.Fatalf(
			"expected one repository Create call, got %d",
			repository.createCalls,
		)
	}

	if storage.deleteCalls != 1 {
		t.Fatalf(
			"expected one compensating Delete call, got %d",
			storage.deleteCalls,
		)
	}
}

// TestUploadServiceStopsWhenFileSaveFails 验证第一步失败后立即停止：
// 文件没有保存成功时，不能创建数据库记录，也没有文件需要删除。
func TestUploadServiceStopsWhenFileSaveFails(t *testing.T) {
	saveError := errors.New("disk unavailable")

	storage := &fakeFileStorage{
		saveFunc: func(
			context.Context,
			string,
			io.Reader,
		) (StoredFile, error) {
			return StoredFile{}, saveError
		},
		deleteFunc: func(context.Context, string) error {
			t.Fatal("Delete must not be called when Save fails")

			return nil
		},
	}

	repository := &fakeUploadRepository{
		createFunc: func(
			context.Context,
			documentdomain.CreateInput,
		) (documentdomain.Document, error) {
			t.Fatal("repository Create must not be called when Save fails")

			return documentdomain.Document{}, nil
		},
	}

	service := NewUploadService(repository, storage)

	_, err := service.Upload(
		context.Background(),
		UploadInput{
			OriginalName: "example.pdf",
			Content:      strings.NewReader("%PDF-1.7"),
		},
	)

	if !errors.Is(err, saveError) {
		t.Fatalf("expected save error, got %v", err)
	}

	if storage.saveCalls != 1 {
		t.Fatalf("expected one Save call, got %d", storage.saveCalls)
	}

	if storage.deleteCalls != 0 {
		t.Fatalf("expected no Delete calls, got %d", storage.deleteCalls)
	}

	if repository.createCalls != 0 {
		t.Fatalf(
			"expected no repository Create calls, got %d",
			repository.createCalls,
		)
	}
}

// TestUploadServicePreservesRepositoryAndDeleteErrors 验证补偿操作也失败时，
// 返回值仍然同时保留数据库错误和文件删除错误。
func TestUploadServicePreservesRepositoryAndDeleteErrors(t *testing.T) {
	repositoryError := errors.New("database unavailable")
	deleteError := errors.New("delete permission denied")
	storedFile := StoredFile{
		StoragePath: "documents/orphan.pdf",
		SizeBytes:   128,
		SHA256:      strings.Repeat("c", 64),
	}

	storage := &fakeFileStorage{
		saveFunc: func(
			context.Context,
			string,
			io.Reader,
		) (StoredFile, error) {
			return storedFile, nil
		},
		deleteFunc: func(
			context.Context,
			string,
		) error {
			return deleteError
		},
	}

	repository := &fakeUploadRepository{
		createFunc: func(
			context.Context,
			documentdomain.CreateInput,
		) (documentdomain.Document, error) {
			return documentdomain.Document{}, repositoryError
		},
	}

	service := NewUploadService(repository, storage)

	_, err := service.Upload(
		context.Background(),
		UploadInput{
			OriginalName: "orphan.pdf",
			Content:      strings.NewReader("%PDF-1.7"),
		},
	)

	if !errors.Is(err, repositoryError) {
		t.Fatalf("expected repository error, got %v", err)
	}

	if !errors.Is(err, deleteError) {
		t.Fatalf("expected delete error, got %v", err)
	}

	if storage.deleteCalls != 1 {
		t.Fatalf("expected one Delete call, got %d", storage.deleteCalls)
	}
}

// TestUploadServiceRejectsInvalidInput 验证无效输入会在访问外部依赖前被拒绝。
func TestUploadServiceRejectsInvalidInput(t *testing.T) {
	tests := []struct {
		name        string
		input       UploadInput
		expectedErr error
	}{
		{
			name: "blank original name",
			input: UploadInput{
				OriginalName: "   ",
				Content:      strings.NewReader("%PDF-1.7"),
			},
			expectedErr: ErrOriginalNameRequired,
		},
		{
			name: "missing file content",
			input: UploadInput{
				OriginalName: "example.pdf",
				Content:      nil,
			},
			expectedErr: ErrFileContentRequired,
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			storage := &fakeFileStorage{
				saveFunc: func(
					context.Context,
					string,
					io.Reader,
				) (StoredFile, error) {
					t.Fatal("Save must not be called for invalid input")

					return StoredFile{}, nil
				},
				deleteFunc: func(context.Context, string) error {
					t.Fatal("Delete must not be called for invalid input")

					return nil
				},
			}

			repository := &fakeUploadRepository{
				createFunc: func(
					context.Context,
					documentdomain.CreateInput,
				) (documentdomain.Document, error) {
					t.Fatal("repository Create must not be called for invalid input")

					return documentdomain.Document{}, nil
				},
			}

			service := NewUploadService(repository, storage)

			_, err := service.Upload(context.Background(), test.input)
			if !errors.Is(err, test.expectedErr) {
				t.Fatalf(
					"expected error %v, got %v",
					test.expectedErr,
					err,
				)
			}

			if storage.saveCalls != 0 {
				t.Fatalf(
					"expected no Save calls, got %d",
					storage.saveCalls,
				)
			}

			if repository.createCalls != 0 {
				t.Fatalf(
					"expected no repository Create calls, got %d",
					repository.createCalls,
				)
			}
		})
	}
}
