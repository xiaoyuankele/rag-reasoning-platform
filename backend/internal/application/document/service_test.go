package document

import (
	"context"
	"errors"
	"testing"

	documentdomain "rag-reasoning-platform/backend/internal/domain/document"
)

// fakeRepository 是只用于应用服务测试的仓储实现。
type fakeRepository struct {
	getByIDFunc  func(context.Context, int64) (documentdomain.Document, error)
	getByIDCalls int
}

// Create 只是为了满足 documentdomain.Repository 接口。
// 当前 GetByID 测试不应该调用它。
func (f *fakeRepository) Create(
	context.Context,
	documentdomain.CreateInput,
) (documentdomain.Document, error) {
	panic("unexpected call to Create")
}

// GetByID 记录调用次数，并执行测试场景提供的函数。
func (f *fakeRepository) GetByID(
	ctx context.Context,
	id int64,
) (documentdomain.Document, error) {
	f.getByIDCalls++

	return f.getByIDFunc(ctx, id)
}

// TestServiceGetByIDRejectsInvalidID 验证非法 ID 不会访问仓储。
func TestServiceGetByIDRejectsInvalidID(t *testing.T) {
	repository := &fakeRepository{}
	service := NewService(repository)

	_, err := service.GetByID(context.Background(), 0)

	if !errors.Is(err, ErrInvalidID) {
		t.Fatalf("expected ErrInvalidID, got %v", err)
	}

	if repository.getByIDCalls != 0 {
		t.Fatalf(
			"expected repository not to be called, got %d calls",
			repository.getByIDCalls,
		)
	}
}

// TestServiceGetByIDReturnsDocument 验证查询成功时返回仓储中的文档。
func TestServiceGetByIDReturnsDocument(t *testing.T) {
	expectedDocument := documentdomain.Document{
		ID:           42,
		OriginalName: "example.pdf",
		Status:       documentdomain.StatusReady,
	}

	repository := &fakeRepository{
		getByIDFunc: func(
			_ context.Context,
			id int64,
		) (documentdomain.Document, error) {
			if id != expectedDocument.ID {
				t.Fatalf(
					"expected repository ID %d, got %d",
					expectedDocument.ID,
					id,
				)
			}

			return expectedDocument, nil
		},
	}

	service := NewService(repository)

	foundDocument, err := service.GetByID(
		context.Background(),
		expectedDocument.ID,
	)
	if err != nil {
		t.Fatalf("expected no error, got %v", err)
	}

	if foundDocument != expectedDocument {
		t.Fatal("service returned an unexpected document")
	}

	if repository.getByIDCalls != 1 {
		t.Fatalf(
			"expected one repository call, got %d",
			repository.getByIDCalls,
		)
	}
}

// TestServiceGetByIDPreservesNotFound 验证领域错误经过包装后仍可识别。
func TestServiceGetByIDPreservesNotFound(t *testing.T) {
	repository := &fakeRepository{
		getByIDFunc: func(
			context.Context,
			int64,
		) (documentdomain.Document, error) {
			return documentdomain.Document{}, documentdomain.ErrNotFound
		},
	}

	service := NewService(repository)

	_, err := service.GetByID(context.Background(), 999)

	if !errors.Is(err, documentdomain.ErrNotFound) {
		t.Fatalf("expected ErrNotFound, got %v", err)
	}

	if repository.getByIDCalls != 1 {
		t.Fatalf(
			"expected one repository call, got %d",
			repository.getByIDCalls,
		)
	}
}
