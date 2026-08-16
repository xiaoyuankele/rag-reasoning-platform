package document

import (
	"context"
	"errors"
	"reflect"
	"strings"
	"testing"

	accessdomain "rag-reasoning-platform/backend/internal/domain/access"
	documentdomain "rag-reasoning-platform/backend/internal/domain/document"
)

// fakeChunkSearcher 让应用服务测试不依赖真实 PostgreSQL。
type fakeChunkSearcher struct {
	searchFunc func(
		context.Context,
		accessdomain.OwnerScope,
		documentdomain.SearchOptions,
	) (documentdomain.SearchResult, error)
	calls int
}

func (f *fakeChunkSearcher) Search(
	ctx context.Context,
	scope accessdomain.OwnerScope,
	options documentdomain.SearchOptions,
) (documentdomain.SearchResult, error) {
	f.calls++
	return f.searchFunc(ctx, scope, options)
}

func TestSearchServiceRejectsInvalidInput(t *testing.T) {
	invalidDocumentID := int64(0)

	tests := []struct {
		name      string
		input     SearchInput
		wantedErr error
	}{
		{
			name: "blank query",
			input: SearchInput{
				Query:    "  \t\n ",
				Page:     1,
				PageSize: 20,
			},
			wantedErr: ErrSearchQueryRequired,
		},
		{
			name: "invalid UTF-8 query",
			input: SearchInput{
				Query:    string([]byte{0xff}),
				Page:     1,
				PageSize: 20,
			},
			wantedErr: ErrSearchQueryInvalidUTF8,
		},
		{
			name: "query longer than rune limit",
			input: SearchInput{
				Query:    strings.Repeat("磁", MaxSearchQueryRunes+1),
				Page:     1,
				PageSize: 20,
			},
			wantedErr: ErrSearchQueryTooLong,
		},
		{
			name: "zero page",
			input: SearchInput{
				Query:    "磁悬浮",
				Page:     0,
				PageSize: 20,
			},
			wantedErr: ErrInvalidPage,
		},
		{
			name: "page size above limit",
			input: SearchInput{
				Query:    "maglev",
				Page:     1,
				PageSize: MaxPageSize + 1,
			},
			wantedErr: ErrInvalidPageSize,
		},
		{
			name: "non-positive document filter",
			input: SearchInput{
				Query:      "maglev",
				DocumentID: &invalidDocumentID,
				Page:       1,
				PageSize:   20,
			},
			wantedErr: ErrInvalidID,
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			searcher := &fakeChunkSearcher{
				searchFunc: func(
					context.Context,
					accessdomain.OwnerScope,
					documentdomain.SearchOptions,
				) (documentdomain.SearchResult, error) {
					t.Fatal("searcher must not be called for invalid input")
					return documentdomain.SearchResult{}, nil
				},
			}
			service := NewSearchService(searcher)

			_, err := service.Search(
				context.Background(),
				testOwnerScope(t),
				test.input,
			)
			if !errors.Is(err, test.wantedErr) {
				t.Fatalf(
					"Search() error = %v, want %v",
					err,
					test.wantedErr,
				)
			}
			if searcher.calls != 0 {
				t.Fatalf("searcher calls = %d, want 0", searcher.calls)
			}
		})
	}
}

func TestSearchServiceNormalizesQueryAndCalculatesPagination(t *testing.T) {
	documentID := int64(20)
	pageStart := 3
	expectedHits := []documentdomain.SearchHit{
		{
			ChunkID:      101,
			DocumentID:   20,
			ChunkIndex:   2,
			OriginalName: "磁悬浮论文.pdf",
			MIMEType:     "application/pdf",
			Content:      "磁悬浮车辆的动力学特性",
			PageStart:    &pageStart,
			PageEnd:      &pageStart,
		},
	}
	searcher := &fakeChunkSearcher{
		searchFunc: func(
			_ context.Context,
			scope accessdomain.OwnerScope,
			options documentdomain.SearchOptions,
		) (documentdomain.SearchResult, error) {
			if scope.OwnerUserID() != testOwnerUserID {
				t.Fatalf(
					"Search() scope owner = %d, want %d",
					scope.OwnerUserID(),
					testOwnerUserID,
				)
			}
			expectedOptions := documentdomain.SearchOptions{
				Query:      "磁悬浮",
				DocumentID: &documentID,
				Limit:      2,
				Offset:     2,
			}
			if !reflect.DeepEqual(options, expectedOptions) {
				t.Fatalf(
					"Search() options = %+v, want %+v",
					options,
					expectedOptions,
				)
			}

			return documentdomain.SearchResult{
				Hits:  expectedHits,
				Total: 5,
			}, nil
		},
	}
	service := NewSearchService(searcher)

	output, err := service.Search(
		context.Background(),
		testOwnerScope(t),
		SearchInput{
			Query:      "  磁悬浮  ",
			DocumentID: &documentID,
			Page:       2,
			PageSize:   2,
		},
	)
	if err != nil {
		t.Fatalf("Search() error = %v, want nil", err)
	}

	if output.Query != "磁悬浮" {
		t.Fatalf("output query = %q, want %q", output.Query, "磁悬浮")
	}
	if output.Page != 2 || output.PageSize != 2 {
		t.Fatalf(
			"output pagination = page %d, size %d; want page 2, size 2",
			output.Page,
			output.PageSize,
		)
	}
	if output.Total != 5 || output.TotalPages != 3 {
		t.Fatalf(
			"output totals = total %d, pages %d; want total 5, pages 3",
			output.Total,
			output.TotalPages,
		)
	}
	if len(output.Hits) != 1 || output.Hits[0] != expectedHits[0] {
		t.Fatalf("output hits = %+v, want %+v", output.Hits, expectedHits)
	}
	if searcher.calls != 1 {
		t.Fatalf("searcher calls = %d, want 1", searcher.calls)
	}
}

func TestSearchServiceReturnsEmptySlice(t *testing.T) {
	searcher := &fakeChunkSearcher{
		searchFunc: func(
			context.Context,
			accessdomain.OwnerScope,
			documentdomain.SearchOptions,
		) (documentdomain.SearchResult, error) {
			return documentdomain.SearchResult{}, nil
		},
	}
	service := NewSearchService(searcher)

	output, err := service.Search(
		context.Background(),
		testOwnerScope(t),
		SearchInput{Query: "not-found", Page: 1, PageSize: 20},
	)
	if err != nil {
		t.Fatalf("Search() error = %v, want nil", err)
	}
	if output.Hits == nil {
		t.Fatal("Search() hits = nil, want non-nil empty slice")
	}
	if len(output.Hits) != 0 {
		t.Fatalf("Search() hits length = %d, want 0", len(output.Hits))
	}
	if output.Total != 0 || output.TotalPages != 0 {
		t.Fatalf(
			"Search() totals = %d/%d, want 0/0",
			output.Total,
			output.TotalPages,
		)
	}
}

func TestSearchServicePreservesRepositoryError(t *testing.T) {
	repositoryError := errors.New("query search index failed")
	searcher := &fakeChunkSearcher{
		searchFunc: func(
			context.Context,
			accessdomain.OwnerScope,
			documentdomain.SearchOptions,
		) (documentdomain.SearchResult, error) {
			return documentdomain.SearchResult{}, repositoryError
		},
	}
	service := NewSearchService(searcher)

	_, err := service.Search(
		context.Background(),
		testOwnerScope(t),
		SearchInput{Query: "maglev", Page: 1, PageSize: 20},
	)
	if !errors.Is(err, repositoryError) {
		t.Fatalf(
			"Search() error = %v, want wrapped repository error",
			err,
		)
	}
}
