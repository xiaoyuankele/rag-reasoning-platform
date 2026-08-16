package document

import (
	"context"
	"errors"
	"testing"

	accessdomain "rag-reasoning-platform/backend/internal/domain/access"
	documentdomain "rag-reasoning-platform/backend/internal/domain/document"
)

// fakeDocumentLister 是列表应用服务测试使用的仓储替身。
// 它只实现 Lister，体现调用者只依赖自己需要的最小接口。
type fakeDocumentLister struct {
	listFunc  func(context.Context, accessdomain.OwnerScope, documentdomain.ListOptions) (documentdomain.ListResult, error)
	listCalls int
}

func (f *fakeDocumentLister) List(
	ctx context.Context,
	scope accessdomain.OwnerScope,
	options documentdomain.ListOptions,
) (documentdomain.ListResult, error) {
	f.listCalls++
	return f.listFunc(ctx, scope, options)
}

// TestListServiceRejectsInvalidPagination 验证非法分页参数会在应用层被拒绝，
// 并且不会产生没有意义的数据库查询。
func TestListServiceRejectsInvalidPagination(t *testing.T) {
	tests := []struct {
		name          string
		input         ListInput
		expectedError error
	}{
		{
			name:          "zero page",
			input:         ListInput{Page: 0, PageSize: 20},
			expectedError: ErrInvalidPage,
		},
		{
			name:          "negative page",
			input:         ListInput{Page: -1, PageSize: 20},
			expectedError: ErrInvalidPage,
		},
		{
			name:          "zero page size",
			input:         ListInput{Page: 1, PageSize: 0},
			expectedError: ErrInvalidPageSize,
		},
		{
			name:          "page size above maximum",
			input:         ListInput{Page: 1, PageSize: MaxPageSize + 1},
			expectedError: ErrInvalidPageSize,
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			repository := &fakeDocumentLister{
				listFunc: func(
					context.Context,
					accessdomain.OwnerScope,
					documentdomain.ListOptions,
				) (documentdomain.ListResult, error) {
					t.Fatal("List must not be called for invalid pagination")
					return documentdomain.ListResult{}, nil
				},
			}

			service := NewListService(repository)
			_, err := service.List(context.Background(), testOwnerScope(t), test.input)
			if !errors.Is(err, test.expectedError) {
				t.Fatalf("expected %v, got %v", test.expectedError, err)
			}

			if repository.listCalls != 0 {
				t.Fatalf("expected no List calls, got %d", repository.listCalls)
			}
		})
	}
}

// TestListServiceReturnsPaginatedDocuments 验证 page/page_size 会被转换为
// 仓储使用的 limit/offset，并返回完整分页信息。
func TestListServiceReturnsPaginatedDocuments(t *testing.T) {
	expectedDocuments := []documentdomain.Document{
		{ID: 42, OriginalName: "second.pdf"},
		{ID: 41, OriginalName: "first.pdf"},
	}

	repository := &fakeDocumentLister{
		listFunc: func(
			_ context.Context,
			scope accessdomain.OwnerScope,
			options documentdomain.ListOptions,
		) (documentdomain.ListResult, error) {
			if scope.OwnerUserID() != testOwnerUserID {
				t.Fatalf("repository scope owner = %d, want %d", scope.OwnerUserID(), testOwnerUserID)
			}
			expectedOptions := documentdomain.ListOptions{
				Limit:  20,
				Offset: 40,
			}
			if options != expectedOptions {
				t.Fatalf("expected options %+v, got %+v", expectedOptions, options)
			}

			return documentdomain.ListResult{
				Documents: expectedDocuments,
				Total:     42,
			}, nil
		},
	}

	service := NewListService(repository)
	result, err := service.List(
		context.Background(),
		testOwnerScope(t),
		ListInput{Page: 3, PageSize: 20},
	)
	if err != nil {
		t.Fatalf("list documents: %v", err)
	}

	if repository.listCalls != 1 {
		t.Fatalf("expected one List call, got %d", repository.listCalls)
	}

	if result.Page != 3 ||
		result.PageSize != 20 ||
		result.Total != 42 ||
		result.TotalPages != 3 {
		t.Fatalf("unexpected pagination result: %+v", result)
	}

	if len(result.Documents) != len(expectedDocuments) {
		t.Fatalf(
			"expected %d documents, got %d",
			len(expectedDocuments),
			len(result.Documents),
		)
	}

	for index := range expectedDocuments {
		if result.Documents[index] != expectedDocuments[index] {
			t.Fatalf(
				"expected document %+v, got %+v",
				expectedDocuments[index],
				result.Documents[index],
			)
		}
	}
}

// TestListServicePreservesRepositoryError 验证仓储错误被包装后仍可由上层识别。
func TestListServicePreservesRepositoryError(t *testing.T) {
	repositoryError := errors.New("database unavailable")
	repository := &fakeDocumentLister{
		listFunc: func(
			context.Context,
			accessdomain.OwnerScope,
			documentdomain.ListOptions,
		) (documentdomain.ListResult, error) {
			return documentdomain.ListResult{}, repositoryError
		},
	}

	service := NewListService(repository)
	_, err := service.List(
		context.Background(),
		testOwnerScope(t),
		ListInput{Page: 1, PageSize: 20},
	)
	if !errors.Is(err, repositoryError) {
		t.Fatalf("expected repository error, got %v", err)
	}
}
