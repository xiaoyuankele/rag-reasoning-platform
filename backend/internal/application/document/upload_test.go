package document

import (
	"context"
	"errors"
	"io"
	"strings"
	"testing"

	accessdomain "rag-reasoning-platform/backend/internal/domain/access"
	documentdomain "rag-reasoning-platform/backend/internal/domain/document"
)

// fakeUploadRepository 是上传用例测试使用的内存假仓储。
// 它不会访问 PostgreSQL，只记录应用服务传给 CreateOrGetBySHA256 的数据。
type fakeUploadRepository struct {
	createOrGetFunc  func(context.Context, accessdomain.OwnerScope, documentdomain.CreateInput) (documentdomain.CreateOrGetResult, error)
	createOrGetCalls int
}

func (f *fakeUploadRepository) CreateOrGetBySHA256(
	ctx context.Context,
	scope accessdomain.OwnerScope,
	input documentdomain.CreateInput,
) (documentdomain.CreateOrGetResult, error) {
	f.createOrGetCalls++

	return f.createOrGetFunc(ctx, scope, input)
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
	const originalName = "example.md"
	const fileContent = "# Example"

	storedFile := StoredFile{
		StoragePath: "documents/example.md",
		MIMEType:    "text/markdown",
		SizeBytes:   int64(len(fileContent)),
		SHA256:      strings.Repeat("a", 64),
	}

	expectedDocument := documentdomain.Document{
		ID:           7,
		OriginalName: originalName,
		StoragePath:  storedFile.StoragePath,
		MIMEType:     storedFile.MIMEType,
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
		createOrGetFunc: func(
			_ context.Context,
			scope accessdomain.OwnerScope,
			input documentdomain.CreateInput,
		) (documentdomain.CreateOrGetResult, error) {
			if scope.OwnerUserID() != testOwnerUserID {
				t.Fatalf("repository scope owner = %d, want %d", scope.OwnerUserID(), testOwnerUserID)
			}
			expectedInput := documentdomain.CreateInput{
				OriginalName: originalName,
				MIMEType:     storedFile.MIMEType,
				StoragePath:  storedFile.StoragePath,
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

			return documentdomain.CreateOrGetResult{
				Document: expectedDocument,
				Created:  true,
			}, nil
		},
	}

	service := NewUploadService(repository, storage)

	result, err := service.Upload(
		context.Background(),
		testOwnerScope(t),
		UploadInput{
			OriginalName: originalName,
			Content:      strings.NewReader(fileContent),
		},
	)
	if err != nil {
		t.Fatalf("expected no error, got %v", err)
	}

	if result.Document != expectedDocument {
		t.Fatalf(
			"expected document %+v, got %+v",
			expectedDocument,
			result.Document,
		)
	}
	if result.Duplicate {
		t.Fatal("expected a newly created upload, got Duplicate=true")
	}

	if storage.saveCalls != 1 {
		t.Fatalf("expected one Save call, got %d", storage.saveCalls)
	}

	if storage.deleteCalls != 0 {
		t.Fatalf("expected no Delete calls, got %d", storage.deleteCalls)
	}

	if repository.createOrGetCalls != 1 {
		t.Fatalf(
			"expected one repository CreateOrGetBySHA256 call, got %d",
			repository.createOrGetCalls,
		)
	}
}

// TestUploadServiceDeletesStoredFileWhenRepositoryFails 验证补偿清理：
// 文件已经保存、但数据库写入失败时，必须删除刚保存的文件。
func TestUploadServiceDeletesStoredFileWhenRepositoryFails(t *testing.T) {
	repositoryError := errors.New("database unavailable")
	storedFile := StoredFile{
		StoragePath: "documents/orphan.pdf",
		MIMEType:    "application/pdf",
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
		createOrGetFunc: func(
			context.Context,
			accessdomain.OwnerScope,
			documentdomain.CreateInput,
		) (documentdomain.CreateOrGetResult, error) {
			return documentdomain.CreateOrGetResult{}, repositoryError
		},
	}

	service := NewUploadService(repository, storage)

	_, err := service.Upload(
		context.Background(),
		testOwnerScope(t),
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

	if repository.createOrGetCalls != 1 {
		t.Fatalf(
			"expected one repository CreateOrGetBySHA256 call, got %d",
			repository.createOrGetCalls,
		)
	}

	if storage.deleteCalls != 1 {
		t.Fatalf(
			"expected one compensating Delete call, got %d",
			storage.deleteCalls,
		)
	}
}

// TestUploadServiceCleansUpAfterRequestCancellation 验证请求上下文在数据库阶段
// 被取消后，补偿删除仍会使用独立的短超时上下文执行，避免遗留孤立文件。
func TestUploadServiceCleansUpAfterRequestCancellation(t *testing.T) {
	repositoryError := errors.New("database operation canceled")
	requestContext, cancelRequest := context.WithCancel(context.Background())
	defer cancelRequest()

	storage := &fakeFileStorage{
		saveFunc: func(context.Context, string, io.Reader) (StoredFile, error) {
			return StoredFile{
				StoragePath: "documents/canceled-request.pdf",
				MIMEType:    "application/pdf",
				SizeBytes:   128,
				SHA256:      strings.Repeat("9", 64),
			}, nil
		},
		deleteFunc: func(cleanupContext context.Context, _ string) error {
			if err := cleanupContext.Err(); err != nil {
				t.Fatalf("cleanup context was already canceled: %v", err)
			}
			return nil
		},
	}
	repository := &fakeUploadRepository{
		createOrGetFunc: func(
			context.Context,
			accessdomain.OwnerScope,
			documentdomain.CreateInput,
		) (documentdomain.CreateOrGetResult, error) {
			cancelRequest()
			return documentdomain.CreateOrGetResult{}, repositoryError
		},
	}

	_, err := NewUploadService(repository, storage).Upload(
		requestContext,
		testOwnerScope(t),
		UploadInput{
			OriginalName: "canceled-request.pdf",
			Content:      strings.NewReader("%PDF-1.7"),
		},
	)
	if !errors.Is(err, repositoryError) {
		t.Fatalf("Upload() error = %v, want repository error", err)
	}
	if storage.deleteCalls != 1 {
		t.Fatalf("Delete calls = %d, want 1", storage.deleteCalls)
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
		createOrGetFunc: func(
			context.Context,
			accessdomain.OwnerScope,
			documentdomain.CreateInput,
		) (documentdomain.CreateOrGetResult, error) {
			t.Fatal("repository CreateOrGetBySHA256 must not be called when Save fails")

			return documentdomain.CreateOrGetResult{}, nil
		},
	}

	service := NewUploadService(repository, storage)

	_, err := service.Upload(
		context.Background(),
		testOwnerScope(t),
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

	if repository.createOrGetCalls != 0 {
		t.Fatalf(
			"expected no repository CreateOrGetBySHA256 calls, got %d",
			repository.createOrGetCalls,
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
		MIMEType:    "application/pdf",
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
		createOrGetFunc: func(
			context.Context,
			accessdomain.OwnerScope,
			documentdomain.CreateInput,
		) (documentdomain.CreateOrGetResult, error) {
			return documentdomain.CreateOrGetResult{}, repositoryError
		},
	}

	service := NewUploadService(repository, storage)

	_, err := service.Upload(
		context.Background(),
		testOwnerScope(t),
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

// TestUploadServiceReturnsExistingDocumentAndDeletesDuplicateFile 验证命中同一用户
// 的相同内容后，应用服务返回已有记录，并删除本次刚写入的多余物理文件。
func TestUploadServiceReturnsExistingDocumentAndDeletesDuplicateFile(t *testing.T) {
	storedFile := StoredFile{
		StoragePath: "documents/new-random-name.pdf",
		MIMEType:    "application/pdf",
		SizeBytes:   128,
		SHA256:      strings.Repeat("d", 64),
	}
	existingDocument := documentdomain.Document{
		ID:           41,
		OriginalName: "first-name.pdf",
		StoragePath:  "documents/existing.pdf",
		MIMEType:     storedFile.MIMEType,
		SizeBytes:    storedFile.SizeBytes,
		SHA256:       storedFile.SHA256,
		Status:       documentdomain.StatusReady,
	}

	storage := &fakeFileStorage{
		saveFunc: func(context.Context, string, io.Reader) (StoredFile, error) {
			return storedFile, nil
		},
		deleteFunc: func(_ context.Context, storagePath string) error {
			if storagePath != storedFile.StoragePath {
				t.Fatalf("Delete path = %q, want %q", storagePath, storedFile.StoragePath)
			}
			return nil
		},
	}
	repository := &fakeUploadRepository{
		createOrGetFunc: func(
			context.Context,
			accessdomain.OwnerScope,
			documentdomain.CreateInput,
		) (documentdomain.CreateOrGetResult, error) {
			return documentdomain.CreateOrGetResult{
				Document: existingDocument,
				Created:  false,
			}, nil
		},
	}

	result, err := NewUploadService(repository, storage).Upload(
		context.Background(),
		testOwnerScope(t),
		UploadInput{
			OriginalName: "renamed-copy.pdf",
			Content:      strings.NewReader("%PDF-1.7"),
		},
	)
	if err != nil {
		t.Fatalf("Upload() error = %v, want nil", err)
	}
	if !result.Duplicate {
		t.Fatal("Upload() Duplicate = false, want true")
	}
	if result.Document != existingDocument {
		t.Fatalf("Upload() Document = %+v, want %+v", result.Document, existingDocument)
	}
	if storage.deleteCalls != 1 {
		t.Fatalf("Delete calls = %d, want 1", storage.deleteCalls)
	}
}

// TestUploadServiceReportsDuplicateCleanupFailure 验证数据库已识别重复、但新物理
// 文件删除失败时不会假装成功，否则磁盘会静默残留孤立文件。
func TestUploadServiceReportsDuplicateCleanupFailure(t *testing.T) {
	deleteError := errors.New("duplicate file is locked")
	storedFile := StoredFile{
		StoragePath: "documents/duplicate.pdf",
		MIMEType:    "application/pdf",
		SizeBytes:   128,
		SHA256:      strings.Repeat("e", 64),
	}
	storage := &fakeFileStorage{
		saveFunc: func(context.Context, string, io.Reader) (StoredFile, error) {
			return storedFile, nil
		},
		deleteFunc: func(context.Context, string) error {
			return deleteError
		},
	}
	repository := &fakeUploadRepository{
		createOrGetFunc: func(
			context.Context,
			accessdomain.OwnerScope,
			documentdomain.CreateInput,
		) (documentdomain.CreateOrGetResult, error) {
			return documentdomain.CreateOrGetResult{
				Document: documentdomain.Document{ID: 42},
				Created:  false,
			}, nil
		},
	}

	_, err := NewUploadService(repository, storage).Upload(
		context.Background(),
		testOwnerScope(t),
		UploadInput{
			OriginalName: "copy.pdf",
			Content:      strings.NewReader("%PDF-1.7"),
		},
	)
	if !errors.Is(err, deleteError) {
		t.Fatalf("Upload() error = %v, want wrapped cleanup error", err)
	}
	if storage.deleteCalls != 1 {
		t.Fatalf("Delete calls = %d, want 1", storage.deleteCalls)
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
				createOrGetFunc: func(
					context.Context,
					accessdomain.OwnerScope,
					documentdomain.CreateInput,
				) (documentdomain.CreateOrGetResult, error) {
					t.Fatal("repository CreateOrGetBySHA256 must not be called for invalid input")

					return documentdomain.CreateOrGetResult{}, nil
				},
			}

			service := NewUploadService(repository, storage)

			_, err := service.Upload(context.Background(), testOwnerScope(t), test.input)
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

			if repository.createOrGetCalls != 0 {
				t.Fatalf(
					"expected no repository CreateOrGetBySHA256 calls, got %d",
					repository.createOrGetCalls,
				)
			}
		})
	}
}
