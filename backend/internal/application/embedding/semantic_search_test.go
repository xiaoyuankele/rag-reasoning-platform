package embedding

import (
	"context"
	"errors"
	"reflect"
	"strings"
	"testing"

	accessdomain "rag-reasoning-platform/backend/internal/domain/access"
	documentdomain "rag-reasoning-platform/backend/internal/domain/document"
	embeddingdomain "rag-reasoning-platform/backend/internal/domain/embedding"
)

// fakeSemanticSearcher 模拟语义搜索仓储，并记录 Application 传入的查询条件。
type fakeSemanticSearcher struct {
	readinessFunc func(
		context.Context,
		accessdomain.OwnerScope,
		documentdomain.SemanticEmbeddingReadinessOptions,
	) (bool, error)
	searchFunc func(
		context.Context,
		accessdomain.OwnerScope,
		documentdomain.SemanticSearchOptions,
	) ([]documentdomain.SemanticSearchHit, error)
	calls          int
	readinessCalls int
}

func (f *fakeSemanticSearcher) HasCompleteSemanticEmbeddings(
	ctx context.Context,
	scope accessdomain.OwnerScope,
	options documentdomain.SemanticEmbeddingReadinessOptions,
) (bool, error) {
	f.readinessCalls++
	if f.readinessFunc == nil {
		return true, nil
	}
	return f.readinessFunc(ctx, scope, options)
}

func (f *fakeSemanticSearcher) SearchSimilar(
	ctx context.Context,
	scope accessdomain.OwnerScope,
	options documentdomain.SemanticSearchOptions,
) ([]documentdomain.SemanticSearchHit, error) {
	f.calls++
	return f.searchFunc(ctx, scope, options)
}

func TestNewSemanticSearchServiceValidatesDependencies(t *testing.T) {
	embedder := &fakeEmbedder{embedFunc: failSemanticEmbed(t)}
	searcher := &fakeSemanticSearcher{searchFunc: failSemanticSearch(t)}

	tests := []struct {
		name       string
		embedder   embeddingdomain.Embedder
		searcher   semanticSearchRepository
		modelName  string
		dimensions int
		wantedErr  error
	}{
		{name: "missing embedder", searcher: searcher, modelName: "model", dimensions: 2, wantedErr: ErrSemanticSearchDependencies},
		{name: "missing searcher", embedder: embedder, modelName: "model", dimensions: 2, wantedErr: ErrSemanticSearchDependencies},
		{name: "blank model", embedder: embedder, searcher: searcher, modelName: " ", dimensions: 2, wantedErr: ErrSemanticSearchConfiguration},
		{name: "invalid dimensions", embedder: embedder, searcher: searcher, modelName: "model", dimensions: 0, wantedErr: ErrSemanticSearchConfiguration},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			service, err := NewSemanticSearchService(
				test.embedder,
				test.searcher,
				test.modelName,
				test.dimensions,
			)
			if service != nil || !errors.Is(err, test.wantedErr) {
				t.Fatalf("NewSemanticSearchService() = (%v, %v), want (nil, %v)", service, err, test.wantedErr)
			}
		})
	}
}

func TestSemanticSearchServiceRejectsInvalidInputBeforeDependencies(t *testing.T) {
	invalidDocumentID := int64(0)
	tests := []struct {
		name      string
		input     SemanticSearchInput
		wantedErr error
	}{
		{name: "blank query", input: SemanticSearchInput{Query: " \t\n ", TopK: 5}, wantedErr: ErrSemanticSearchQueryRequired},
		{name: "invalid UTF-8", input: SemanticSearchInput{Query: string([]byte{0xff}), TopK: 5}, wantedErr: ErrSemanticSearchQueryInvalidUTF8},
		{name: "query too long", input: SemanticSearchInput{Query: strings.Repeat("问", MaxSemanticSearchQueryRunes+1), TopK: 5}, wantedErr: ErrSemanticSearchQueryTooLong},
		{name: "invalid document ID", input: SemanticSearchInput{Query: "问题", DocumentID: &invalidDocumentID, TopK: 5}, wantedErr: ErrInvalidDocumentID},
		{name: "zero top k", input: SemanticSearchInput{Query: "问题", TopK: 0}, wantedErr: ErrInvalidSemanticSearchTopK},
		{name: "top k above limit", input: SemanticSearchInput{Query: "问题", TopK: MaxSemanticSearchTopK + 1}, wantedErr: ErrInvalidSemanticSearchTopK},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			embedder := &fakeEmbedder{embedFunc: failSemanticEmbed(t)}
			searcher := &fakeSemanticSearcher{searchFunc: failSemanticSearch(t)}
			service := newSemanticSearchServiceForTest(t, embedder, searcher)

			_, err := service.Search(
				context.Background(),
				testEmbeddingOwnerScope(t),
				test.input,
			)
			if !errors.Is(err, test.wantedErr) {
				t.Fatalf("Search() error = %v, want %v", err, test.wantedErr)
			}
			if embedder.embedCalls != 0 || searcher.calls != 0 {
				t.Fatalf("dependency calls = embedder:%d searcher:%d, want 0/0", embedder.embedCalls, searcher.calls)
			}
		})
	}
}

func TestSemanticSearchServiceEmbedsQueryAndSearchesSimilarChunks(t *testing.T) {
	documentID := int64(20)
	queryVector := []float32{0.1, 0.2, 0.3}
	page := 6
	expectedHits := []documentdomain.SemanticSearchHit{
		{
			ChunkID:      101,
			DocumentID:   documentID,
			ChunkIndex:   4,
			OriginalName: "paper.pdf",
			MIMEType:     "application/pdf",
			Content:      "与问题语义相关的正文",
			PageStart:    &page,
			PageEnd:      &page,
			Similarity:   0.91,
		},
	}

	embedder := &fakeEmbedder{
		embedFunc: func(
			_ context.Context,
			request embeddingdomain.EmbedRequest,
		) (embeddingdomain.EmbedResult, error) {
			expectedRequest := embeddingdomain.EmbedRequest{
				Inputs:     []string{"协同控制如何改善稳定性？"},
				Model:      "test-model",
				Dimensions: 3,
			}
			if !reflect.DeepEqual(request, expectedRequest) {
				t.Fatalf("Embed() request = %+v, want %+v", request, expectedRequest)
			}
			return embeddingdomain.EmbedResult{Vectors: [][]float32{queryVector}}, nil
		},
	}
	searcher := &fakeSemanticSearcher{
		readinessFunc: func(
			_ context.Context,
			scope accessdomain.OwnerScope,
			options documentdomain.SemanticEmbeddingReadinessOptions,
		) (bool, error) {
			if scope.OwnerUserID() != testEmbeddingOwnerUserID {
				t.Fatalf(
					"readiness scope owner = %d, want %d",
					scope.OwnerUserID(),
					testEmbeddingOwnerUserID,
				)
			}
			expectedOptions := documentdomain.SemanticEmbeddingReadinessOptions{
				DocumentID: documentID,
				ModelName:  "test-model",
				Dimensions: 3,
			}
			if !reflect.DeepEqual(options, expectedOptions) {
				t.Fatalf(
					"HasCompleteSemanticEmbeddings() options = %+v, want %+v",
					options,
					expectedOptions,
				)
			}
			return true, nil
		},
		searchFunc: func(
			_ context.Context,
			scope accessdomain.OwnerScope,
			options documentdomain.SemanticSearchOptions,
		) ([]documentdomain.SemanticSearchHit, error) {
			if scope.OwnerUserID() != testEmbeddingOwnerUserID {
				t.Fatalf(
					"search scope owner = %d, want %d",
					scope.OwnerUserID(),
					testEmbeddingOwnerUserID,
				)
			}
			expectedOptions := documentdomain.SemanticSearchOptions{
				QueryVector: queryVector,
				ModelName:   "test-model",
				Dimensions:  3,
				DocumentID:  &documentID,
				Limit:       5,
			}
			if !reflect.DeepEqual(options, expectedOptions) {
				t.Fatalf("SearchSimilar() options = %+v, want %+v", options, expectedOptions)
			}
			return expectedHits, nil
		},
	}
	service := newSemanticSearchServiceForTest(t, embedder, searcher)

	output, err := service.Search(
		context.Background(),
		testEmbeddingOwnerScope(t),
		SemanticSearchInput{
			Query:      "  协同控制如何改善稳定性？  ",
			DocumentID: &documentID,
			TopK:       5,
		},
	)
	if err != nil {
		t.Fatalf("Search() error = %v, want nil", err)
	}
	if output.Query != "协同控制如何改善稳定性？" ||
		!reflect.DeepEqual(output.Hits, expectedHits) {
		t.Fatalf("Search() output = %+v, want normalized query and expected hits", output)
	}
	if searcher.readinessCalls != 1 || embedder.embedCalls != 1 || searcher.calls != 1 {
		t.Fatalf(
			"dependency calls = readiness:%d embedder:%d searcher:%d, want 1/1/1",
			searcher.readinessCalls,
			embedder.embedCalls,
			searcher.calls,
		)
	}
}

func TestSemanticSearchServiceRejectsDocumentWithoutCompleteEmbeddings(t *testing.T) {
	documentID := int64(20)
	embedder := &fakeEmbedder{embedFunc: failSemanticEmbed(t)}
	searcher := &fakeSemanticSearcher{
		readinessFunc: func(
			context.Context,
			accessdomain.OwnerScope,
			documentdomain.SemanticEmbeddingReadinessOptions,
		) (bool, error) {
			return false, nil
		},
		searchFunc: failSemanticSearch(t),
	}
	service := newSemanticSearchServiceForTest(t, embedder, searcher)

	_, err := service.Search(
		context.Background(),
		testEmbeddingOwnerScope(t),
		SemanticSearchInput{Query: "问题", DocumentID: &documentID, TopK: 5},
	)
	if !errors.Is(err, ErrDocumentEmbeddingsNotReady) {
		t.Fatalf("Search() error = %v, want ErrDocumentEmbeddingsNotReady", err)
	}
	if searcher.readinessCalls != 1 || embedder.embedCalls != 0 || searcher.calls != 0 {
		t.Fatalf(
			"dependency calls = readiness:%d embedder:%d searcher:%d, want 1/0/0",
			searcher.readinessCalls,
			embedder.embedCalls,
			searcher.calls,
		)
	}
}

func TestSemanticSearchServicePreservesReadinessError(t *testing.T) {
	documentID := int64(999)
	embedder := &fakeEmbedder{embedFunc: failSemanticEmbed(t)}
	searcher := &fakeSemanticSearcher{
		readinessFunc: func(
			context.Context,
			accessdomain.OwnerScope,
			documentdomain.SemanticEmbeddingReadinessOptions,
		) (bool, error) {
			return false, documentdomain.ErrNotFound
		},
		searchFunc: failSemanticSearch(t),
	}
	service := newSemanticSearchServiceForTest(t, embedder, searcher)

	_, err := service.Search(
		context.Background(),
		testEmbeddingOwnerScope(t),
		SemanticSearchInput{Query: "问题", DocumentID: &documentID, TopK: 5},
	)
	if !errors.Is(err, documentdomain.ErrNotFound) {
		t.Fatalf("Search() error = %v, want wrapped ErrNotFound", err)
	}
	if searcher.readinessCalls != 1 || embedder.embedCalls != 0 || searcher.calls != 0 {
		t.Fatalf(
			"dependency calls = readiness:%d embedder:%d searcher:%d, want 1/0/0",
			searcher.readinessCalls,
			embedder.embedCalls,
			searcher.calls,
		)
	}
}

func TestSemanticSearchServicePreservesEmbedderError(t *testing.T) {
	providerError := embeddingdomain.ErrEmbeddingRateLimited
	embedder := &fakeEmbedder{
		embedFunc: func(context.Context, embeddingdomain.EmbedRequest) (embeddingdomain.EmbedResult, error) {
			return embeddingdomain.EmbedResult{}, providerError
		},
	}
	searcher := &fakeSemanticSearcher{searchFunc: failSemanticSearch(t)}
	service := newSemanticSearchServiceForTest(t, embedder, searcher)

	_, err := service.Search(
		context.Background(),
		testEmbeddingOwnerScope(t),
		SemanticSearchInput{Query: "问题", TopK: 5},
	)
	if !errors.Is(err, providerError) {
		t.Fatalf("Search() error = %v, want wrapped provider error", err)
	}
	if searcher.calls != 0 {
		t.Fatalf("searcher calls = %d, want 0 after embedding failure", searcher.calls)
	}
}

func TestSemanticSearchServiceRejectsInvalidEmbeddingResult(t *testing.T) {
	tests := []struct {
		name    string
		vectors [][]float32
	}{
		{name: "missing vector", vectors: nil},
		{name: "multiple vectors", vectors: [][]float32{{1, 2, 3}, {4, 5, 6}}},
		{name: "wrong dimensions", vectors: [][]float32{{1, 2}}},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			embedder := &fakeEmbedder{
				embedFunc: func(context.Context, embeddingdomain.EmbedRequest) (embeddingdomain.EmbedResult, error) {
					return embeddingdomain.EmbedResult{Vectors: test.vectors}, nil
				},
			}
			searcher := &fakeSemanticSearcher{searchFunc: failSemanticSearch(t)}
			service := newSemanticSearchServiceForTest(t, embedder, searcher)

			_, err := service.Search(
				context.Background(),
				testEmbeddingOwnerScope(t),
				SemanticSearchInput{Query: "问题", TopK: 5},
			)
			if !errors.Is(err, embeddingdomain.ErrInvalidEmbeddingResponse) {
				t.Fatalf("Search() error = %v, want ErrInvalidEmbeddingResponse", err)
			}
			if searcher.calls != 0 {
				t.Fatalf("searcher calls = %d, want 0 for invalid vector", searcher.calls)
			}
		})
	}
}

func TestSemanticSearchServicePreservesRepositoryError(t *testing.T) {
	repositoryError := errors.New("vector query failed")
	embedder := &fakeEmbedder{
		embedFunc: func(context.Context, embeddingdomain.EmbedRequest) (embeddingdomain.EmbedResult, error) {
			return embeddingdomain.EmbedResult{Vectors: [][]float32{{1, 2, 3}}}, nil
		},
	}
	searcher := &fakeSemanticSearcher{
		searchFunc: func(context.Context, accessdomain.OwnerScope, documentdomain.SemanticSearchOptions) ([]documentdomain.SemanticSearchHit, error) {
			return nil, repositoryError
		},
	}
	service := newSemanticSearchServiceForTest(t, embedder, searcher)

	_, err := service.Search(
		context.Background(),
		testEmbeddingOwnerScope(t),
		SemanticSearchInput{Query: "问题", TopK: 5},
	)
	if !errors.Is(err, repositoryError) {
		t.Fatalf("Search() error = %v, want wrapped repository error", err)
	}
}

func TestSemanticSearchServiceNormalizesEmptyHits(t *testing.T) {
	embedder := &fakeEmbedder{
		embedFunc: func(context.Context, embeddingdomain.EmbedRequest) (embeddingdomain.EmbedResult, error) {
			return embeddingdomain.EmbedResult{Vectors: [][]float32{{1, 2, 3}}}, nil
		},
	}
	searcher := &fakeSemanticSearcher{
		searchFunc: func(context.Context, accessdomain.OwnerScope, documentdomain.SemanticSearchOptions) ([]documentdomain.SemanticSearchHit, error) {
			return nil, nil
		},
	}
	service := newSemanticSearchServiceForTest(t, embedder, searcher)

	output, err := service.Search(
		context.Background(),
		testEmbeddingOwnerScope(t),
		SemanticSearchInput{Query: "没有结果的问题", TopK: 5},
	)
	if err != nil {
		t.Fatalf("Search() error = %v, want nil", err)
	}
	if output.Hits == nil || len(output.Hits) != 0 {
		t.Fatalf("Search() hits = %#v, want non-nil empty slice", output.Hits)
	}
}

func newSemanticSearchServiceForTest(
	t *testing.T,
	embedder embeddingdomain.Embedder,
	searcher semanticSearchRepository,
) *SemanticSearchService {
	t.Helper()
	service, err := NewSemanticSearchService(embedder, searcher, "test-model", 3)
	if err != nil {
		t.Fatalf("NewSemanticSearchService() error = %v, want nil", err)
	}
	return service
}

func failSemanticEmbed(
	t *testing.T,
) func(context.Context, embeddingdomain.EmbedRequest) (embeddingdomain.EmbedResult, error) {
	t.Helper()
	return func(context.Context, embeddingdomain.EmbedRequest) (embeddingdomain.EmbedResult, error) {
		t.Fatal("Embed() must not be called")
		return embeddingdomain.EmbedResult{}, nil
	}
}

func failSemanticSearch(
	t *testing.T,
) func(context.Context, accessdomain.OwnerScope, documentdomain.SemanticSearchOptions) ([]documentdomain.SemanticSearchHit, error) {
	t.Helper()
	return func(context.Context, accessdomain.OwnerScope, documentdomain.SemanticSearchOptions) ([]documentdomain.SemanticSearchHit, error) {
		t.Fatal("SearchSimilar() must not be called")
		return nil, nil
	}
}
