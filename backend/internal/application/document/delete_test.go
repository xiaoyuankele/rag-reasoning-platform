package document

import (
	"context"
	"errors"
	"slices"
	"testing"

	accessdomain "rag-reasoning-platform/backend/internal/domain/access"
	documentdomain "rag-reasoning-platform/backend/internal/domain/document"
)

type fakeDeleteRepository struct {
	getByIDFunc  func(context.Context, accessdomain.OwnerScope, int64) (documentdomain.Document, error)
	deleteFunc   func(context.Context, accessdomain.OwnerScope, int64) error
	getByIDCalls int
	deleteCalls  int
}

func (f *fakeDeleteRepository) GetByID(
	ctx context.Context,
	scope accessdomain.OwnerScope,
	id int64,
) (documentdomain.Document, error) {
	f.getByIDCalls++

	return f.getByIDFunc(ctx, scope, id)
}

func (f *fakeDeleteRepository) Delete(
	ctx context.Context,
	scope accessdomain.OwnerScope,
	id int64,
) error {
	f.deleteCalls++

	return f.deleteFunc(ctx, scope, id)
}

type fakeDeleteFileStorage struct {
	deleteFunc  func(context.Context, string) error
	deleteCalls int
}

func (f *fakeDeleteFileStorage) Delete(
	ctx context.Context,
	storagePath string,
) error {
	f.deleteCalls++

	return f.deleteFunc(ctx, storagePath)
}

func TestDeleteServiceRejectsInvalidID(t *testing.T) {
	repository := newFailOnCallDeleteRepository(t)
	storage := newFailOnCallDeleteStorage(t)
	service := NewDeleteService(repository, storage)

	err := service.Delete(context.Background(), testOwnerScope(t), 0)

	if !errors.Is(err, ErrInvalidID) {
		t.Fatalf("Delete() error = %v, want ErrInvalidID", err)
	}

	assertDeleteCalls(t, repository, storage, 0, 0, 0)
}

func TestDeleteServicePreservesNotFound(t *testing.T) {
	repository := &fakeDeleteRepository{
		getByIDFunc: func(
			_ context.Context,
			_ accessdomain.OwnerScope,
			_ int64,
		) (documentdomain.Document, error) {
			return documentdomain.Document{}, documentdomain.ErrNotFound
		},
		deleteFunc: func(context.Context, accessdomain.OwnerScope, int64) error {
			t.Fatal("repository Delete() must not be called after GetByID failure")
			return nil
		},
	}
	storage := newFailOnCallDeleteStorage(t)
	service := NewDeleteService(repository, storage)

	err := service.Delete(context.Background(), testOwnerScope(t), 99)

	if !errors.Is(err, documentdomain.ErrNotFound) {
		t.Fatalf("Delete() error = %v, want ErrNotFound", err)
	}

	assertDeleteCalls(t, repository, storage, 1, 0, 0)
}

func TestDeleteServiceStopsWhenFileDeletionFails(t *testing.T) {
	fileError := errors.New("file system unavailable")
	repository := &fakeDeleteRepository{
		getByIDFunc: func(
			_ context.Context,
			_ accessdomain.OwnerScope,
			id int64,
		) (documentdomain.Document, error) {
			return documentdomain.Document{
				ID:          id,
				StoragePath: "documents/file-delete-error.pdf",
			}, nil
		},
		deleteFunc: func(context.Context, accessdomain.OwnerScope, int64) error {
			t.Fatal("repository Delete() must not be called after file deletion failure")
			return nil
		},
	}
	storage := &fakeDeleteFileStorage{
		deleteFunc: func(context.Context, string) error {
			return fileError
		},
	}
	service := NewDeleteService(repository, storage)

	err := service.Delete(context.Background(), testOwnerScope(t), 7)

	if !errors.Is(err, fileError) {
		t.Fatalf("Delete() error = %v, want wrapped file error", err)
	}

	assertDeleteCalls(t, repository, storage, 1, 1, 0)
}

func TestDeleteServicePreservesRepositoryDeleteError(t *testing.T) {
	databaseError := errors.New("database unavailable")
	repository := &fakeDeleteRepository{
		getByIDFunc: func(
			_ context.Context,
			_ accessdomain.OwnerScope,
			id int64,
		) (documentdomain.Document, error) {
			return documentdomain.Document{
				ID:          id,
				StoragePath: "documents/database-delete-error.pdf",
			}, nil
		},
		deleteFunc: func(context.Context, accessdomain.OwnerScope, int64) error {
			return databaseError
		},
	}
	storage := &fakeDeleteFileStorage{
		deleteFunc: func(context.Context, string) error {
			return nil
		},
	}
	service := NewDeleteService(repository, storage)

	err := service.Delete(context.Background(), testOwnerScope(t), 8)

	if !errors.Is(err, databaseError) {
		t.Fatalf("Delete() error = %v, want wrapped database error", err)
	}

	assertDeleteCalls(t, repository, storage, 1, 1, 1)
}

func TestDeleteServiceDeletesFileBeforeDatabaseRecord(t *testing.T) {
	const expectedID int64 = 7
	const expectedStoragePath = "documents/delete-test.pdf"

	expectedDocument := documentdomain.Document{
		ID:          expectedID,
		StoragePath: expectedStoragePath,
	}
	callOrder := make([]string, 0, 3)

	repository := &fakeDeleteRepository{
		getByIDFunc: func(
			_ context.Context,
			scope accessdomain.OwnerScope,
			id int64,
		) (documentdomain.Document, error) {
			callOrder = append(callOrder, "get")
			if scope.OwnerUserID() != testOwnerUserID {
				t.Fatalf("GetByID() scope owner = %d, want %d", scope.OwnerUserID(), testOwnerUserID)
			}
			if id != expectedID {
				t.Fatalf("GetByID() id = %d, want %d", id, expectedID)
			}

			return expectedDocument, nil
		},
		deleteFunc: func(_ context.Context, scope accessdomain.OwnerScope, id int64) error {
			callOrder = append(callOrder, "database-delete")
			if scope.OwnerUserID() != testOwnerUserID {
				t.Fatalf("Delete() scope owner = %d, want %d", scope.OwnerUserID(), testOwnerUserID)
			}
			if id != expectedID {
				t.Fatalf("Delete() id = %d, want %d", id, expectedID)
			}

			return nil
		},
	}
	storage := &fakeDeleteFileStorage{
		deleteFunc: func(
			_ context.Context,
			storagePath string,
		) error {
			callOrder = append(callOrder, "file-delete")
			if storagePath != expectedStoragePath {
				t.Fatalf(
					"Delete() storagePath = %q, want %q",
					storagePath,
					expectedStoragePath,
				)
			}

			return nil
		},
	}
	service := NewDeleteService(repository, storage)

	err := service.Delete(context.Background(), testOwnerScope(t), expectedID)

	if err != nil {
		t.Fatalf("Delete() error = %v, want nil", err)
	}

	assertDeleteCalls(t, repository, storage, 1, 1, 1)

	expectedCallOrder := []string{
		"get",
		"file-delete",
		"database-delete",
	}
	if !slices.Equal(callOrder, expectedCallOrder) {
		t.Fatalf(
			"call order = %v, want %v",
			callOrder,
			expectedCallOrder,
		)
	}
}

// newFailOnCallDeleteRepository 创建一个“不允许被调用”的仓储。
// 非法 ID 测试使用它，能够在服务意外访问依赖时给出明确失败信息。
func newFailOnCallDeleteRepository(t *testing.T) *fakeDeleteRepository {
	t.Helper()

	return &fakeDeleteRepository{
		getByIDFunc: func(
			context.Context,
			accessdomain.OwnerScope,
			int64,
		) (documentdomain.Document, error) {
			t.Fatal("GetByID() must not be called")
			return documentdomain.Document{}, nil
		},
		deleteFunc: func(context.Context, accessdomain.OwnerScope, int64) error {
			t.Fatal("repository Delete() must not be called")
			return nil
		},
	}
}

func newFailOnCallDeleteStorage(t *testing.T) *fakeDeleteFileStorage {
	t.Helper()

	return &fakeDeleteFileStorage{
		deleteFunc: func(context.Context, string) error {
			t.Fatal("storage Delete() must not be called")
			return nil
		},
	}
}

// assertDeleteCalls 集中验证删除流程与三个外部操作的交互次数。
func assertDeleteCalls(
	t *testing.T,
	repository *fakeDeleteRepository,
	storage *fakeDeleteFileStorage,
	wantGetByIDCalls int,
	wantStorageDeleteCalls int,
	wantRepositoryDeleteCalls int,
) {
	t.Helper()

	if repository.getByIDCalls != wantGetByIDCalls {
		t.Fatalf(
			"GetByID() calls = %d, want %d",
			repository.getByIDCalls,
			wantGetByIDCalls,
		)
	}

	if storage.deleteCalls != wantStorageDeleteCalls {
		t.Fatalf(
			"storage Delete() calls = %d, want %d",
			storage.deleteCalls,
			wantStorageDeleteCalls,
		)
	}

	if repository.deleteCalls != wantRepositoryDeleteCalls {
		t.Fatalf(
			"repository Delete() calls = %d, want %d",
			repository.deleteCalls,
			wantRepositoryDeleteCalls,
		)
	}
}
