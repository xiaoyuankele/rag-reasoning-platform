package document

import (
	"context"
	"errors"
	"reflect"
	"testing"

	documentdomain "rag-reasoning-platform/backend/internal/domain/document"
)

type fakeChunkListDocumentFinder struct {
	getByIDFunc  func(context.Context, int64) (documentdomain.Document, error)
	getByIDCalls int
}

func (f *fakeChunkListDocumentFinder) GetByID(
	ctx context.Context,
	documentID int64,
) (documentdomain.Document, error) {
	f.getByIDCalls++
	return f.getByIDFunc(ctx, documentID)
}

type fakeChunkPageLister struct {
	listFunc  func(context.Context, int64, documentdomain.ChunkPageOptions) (documentdomain.ChunkPageResult, error)
	listCalls int
}

func (f *fakeChunkPageLister) ListPageByDocumentID(
	ctx context.Context,
	documentID int64,
	options documentdomain.ChunkPageOptions,
) (documentdomain.ChunkPageResult, error) {
	f.listCalls++
	return f.listFunc(ctx, documentID, options)
}

func TestChunkListServiceRejectsInvalidInput(t *testing.T) {
	testCases := []struct {
		name    string
		input   ChunkListInput
		wantErr error
	}{
		{
			name:    "document ID",
			input:   ChunkListInput{DocumentID: 0, Page: 1, PageSize: 20},
			wantErr: ErrInvalidID,
		},
		{
			name:    "page",
			input:   ChunkListInput{DocumentID: 7, Page: 0, PageSize: 20},
			wantErr: ErrInvalidPage,
		},
		{
			name:    "page size zero",
			input:   ChunkListInput{DocumentID: 7, Page: 1, PageSize: 0},
			wantErr: ErrInvalidPageSize,
		},
		{
			name:    "page size too large",
			input:   ChunkListInput{DocumentID: 7, Page: 1, PageSize: 101},
			wantErr: ErrInvalidPageSize,
		},
	}

	for _, testCase := range testCases {
		t.Run(testCase.name, func(t *testing.T) {
			finder := &fakeChunkListDocumentFinder{
				getByIDFunc: func(
					context.Context,
					int64,
				) (documentdomain.Document, error) {
					t.Fatal("GetByID() must not be called for invalid input")
					return documentdomain.Document{}, nil
				},
			}
			lister := &fakeChunkPageLister{
				listFunc: func(
					context.Context,
					int64,
					documentdomain.ChunkPageOptions,
				) (documentdomain.ChunkPageResult, error) {
					t.Fatal("ListPageByDocumentID() must not be called for invalid input")
					return documentdomain.ChunkPageResult{}, nil
				},
			}
			service := NewChunkListService(finder, lister)

			_, err := service.List(context.Background(), testCase.input)

			if !errors.Is(err, testCase.wantErr) {
				t.Fatalf("List() error = %v, want %v", err, testCase.wantErr)
			}
			if finder.getByIDCalls != 0 || lister.listCalls != 0 {
				t.Fatal("invalid input reached a repository dependency")
			}
		})
	}
}

func TestChunkListServiceRejectsDocumentThatIsNotReady(t *testing.T) {
	statuses := []documentdomain.Status{
		documentdomain.StatusUploaded,
		documentdomain.StatusProcessing,
		documentdomain.StatusFailed,
	}

	for _, status := range statuses {
		t.Run(string(status), func(t *testing.T) {
			finder := &fakeChunkListDocumentFinder{
				getByIDFunc: func(
					context.Context,
					int64,
				) (documentdomain.Document, error) {
					return documentdomain.Document{ID: 7, Status: status}, nil
				},
			}
			lister := &fakeChunkPageLister{
				listFunc: func(
					context.Context,
					int64,
					documentdomain.ChunkPageOptions,
				) (documentdomain.ChunkPageResult, error) {
					t.Fatal("chunks must not be read before the document is ready")
					return documentdomain.ChunkPageResult{}, nil
				},
			}
			service := NewChunkListService(finder, lister)

			_, err := service.List(context.Background(), ChunkListInput{
				DocumentID: 7,
				Page:       1,
				PageSize:   20,
			})

			if !errors.Is(err, ErrDocumentChunksNotReady) {
				t.Fatalf("List() error = %v, want ErrDocumentChunksNotReady", err)
			}
			if lister.listCalls != 0 {
				t.Fatalf("ListPageByDocumentID() calls = %d, want 0", lister.listCalls)
			}
		})
	}
}

func TestChunkListServiceReturnsPage(t *testing.T) {
	expectedChunks := []documentdomain.TextChunk{
		{ID: 12, DocumentID: 7, Index: 2, Content: "third chunk"},
		{ID: 13, DocumentID: 7, Index: 3, Content: "fourth chunk"},
	}
	finder := &fakeChunkListDocumentFinder{
		getByIDFunc: func(
			_ context.Context,
			documentID int64,
		) (documentdomain.Document, error) {
			return documentdomain.Document{
				ID:     documentID,
				Status: documentdomain.StatusReady,
			}, nil
		},
	}
	lister := &fakeChunkPageLister{
		listFunc: func(
			_ context.Context,
			documentID int64,
			options documentdomain.ChunkPageOptions,
		) (documentdomain.ChunkPageResult, error) {
			if documentID != 7 {
				t.Fatalf("document ID = %d, want 7", documentID)
			}
			expectedOptions := documentdomain.ChunkPageOptions{
				Limit:  2,
				Offset: 2,
			}
			if options != expectedOptions {
				t.Fatalf("options = %+v, want %+v", options, expectedOptions)
			}
			return documentdomain.ChunkPageResult{
				Chunks: expectedChunks,
				Total:  5,
			}, nil
		},
	}
	service := NewChunkListService(finder, lister)

	result, err := service.List(context.Background(), ChunkListInput{
		DocumentID: 7,
		Page:       2,
		PageSize:   2,
	})

	if err != nil {
		t.Fatalf("List() error = %v, want nil", err)
	}
	if result.DocumentID != 7 || result.Page != 2 ||
		result.PageSize != 2 || result.Total != 5 || result.TotalPages != 3 {
		t.Fatalf("List() pagination = %+v, want page 2/3 with total 5", result)
	}
	if !reflect.DeepEqual(result.Chunks, expectedChunks) {
		t.Fatalf("List() chunks = %+v, want %+v", result.Chunks, expectedChunks)
	}
}

func TestChunkListServicePreservesDependencyErrors(t *testing.T) {
	t.Run("document not found", func(t *testing.T) {
		finder := &fakeChunkListDocumentFinder{
			getByIDFunc: func(
				context.Context,
				int64,
			) (documentdomain.Document, error) {
				return documentdomain.Document{}, documentdomain.ErrNotFound
			},
		}
		lister := &fakeChunkPageLister{}
		service := NewChunkListService(finder, lister)

		_, err := service.List(context.Background(), ChunkListInput{
			DocumentID: 999,
			Page:       1,
			PageSize:   20,
		})

		if !errors.Is(err, documentdomain.ErrNotFound) {
			t.Fatalf("List() error = %v, want ErrNotFound", err)
		}
	})

	t.Run("chunk repository", func(t *testing.T) {
		repositoryErr := errors.New("database unavailable")
		finder := &fakeChunkListDocumentFinder{
			getByIDFunc: func(
				context.Context,
				int64,
			) (documentdomain.Document, error) {
				return documentdomain.Document{
					ID:     7,
					Status: documentdomain.StatusReady,
				}, nil
			},
		}
		lister := &fakeChunkPageLister{
			listFunc: func(
				context.Context,
				int64,
				documentdomain.ChunkPageOptions,
			) (documentdomain.ChunkPageResult, error) {
				return documentdomain.ChunkPageResult{}, repositoryErr
			},
		}
		service := NewChunkListService(finder, lister)

		_, err := service.List(context.Background(), ChunkListInput{
			DocumentID: 7,
			Page:       1,
			PageSize:   20,
		})

		if !errors.Is(err, repositoryErr) {
			t.Fatalf("List() error = %v, want repository error", err)
		}
	})
}
