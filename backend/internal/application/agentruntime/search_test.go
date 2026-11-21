package agentruntime

import (
	"context"
	"errors"
	"strings"
	"testing"

	agentapplication "rag-reasoning-platform/backend/internal/application/agent"
	documentapplication "rag-reasoning-platform/backend/internal/application/document"
	embeddingapplication "rag-reasoning-platform/backend/internal/application/embedding"
	accessdomain "rag-reasoning-platform/backend/internal/domain/access"
	documentdomain "rag-reasoning-platform/backend/internal/domain/document"
	embeddingdomain "rag-reasoning-platform/backend/internal/domain/embedding"
)

type keywordSearchFunc func(context.Context, accessdomain.OwnerScope, documentapplication.SearchInput) (documentapplication.SearchOutput, error)

func (f keywordSearchFunc) Search(
	ctx context.Context,
	scope accessdomain.OwnerScope,
	input documentapplication.SearchInput,
) (documentapplication.SearchOutput, error) {
	return f(ctx, scope, input)
}

type semanticSearchFunc func(context.Context, accessdomain.OwnerScope, embeddingapplication.SemanticSearchInput) (embeddingapplication.SemanticSearchOutput, error)

func (f semanticSearchFunc) Search(
	ctx context.Context,
	scope accessdomain.OwnerScope,
	input embeddingapplication.SemanticSearchInput,
) (embeddingapplication.SemanticSearchOutput, error) {
	return f(ctx, scope, input)
}

func TestSearchDocumentsDispatchesKeywordAndSemantic(t *testing.T) {
	scope, _ := accessdomain.NewOwnerScope(42)
	documentID := int64(7)
	page := 3
	keywordCalls := 0
	semanticCalls := 0
	service, err := NewSearchDocuments(
		keywordSearchFunc(func(_ context.Context, gotScope accessdomain.OwnerScope, input documentapplication.SearchInput) (documentapplication.SearchOutput, error) {
			keywordCalls++
			if gotScope.OwnerUserID() != scope.OwnerUserID() || input.Query != "bridge" ||
				input.DocumentID == nil || *input.DocumentID != documentID || input.Page != 1 || input.PageSize != 2 {
				t.Fatalf("keyword input = scope:%+v input:%+v", gotScope, input)
			}
			return documentapplication.SearchOutput{Hits: []documentdomain.SearchHit{{
				DocumentID: documentID, ChunkID: 11, OriginalName: "bridge.pdf",
				Content: "keyword snippet", PageStart: &page, PageEnd: &page,
			}}}, nil
		}),
		semanticSearchFunc(func(_ context.Context, gotScope accessdomain.OwnerScope, input embeddingapplication.SemanticSearchInput) (embeddingapplication.SemanticSearchOutput, error) {
			semanticCalls++
			if gotScope.OwnerUserID() != scope.OwnerUserID() || input.Query != "vibration" || input.TopK != 3 ||
				input.DocumentID == nil || *input.DocumentID != documentID {
				t.Fatalf("semantic input = scope:%+v input:%+v", gotScope, input)
			}
			title := "Semantic title"
			return embeddingapplication.SemanticSearchOutput{Hits: []documentdomain.SemanticSearchHit{{
				DocumentID: 8, ChunkID: 12, Title: &title, OriginalName: "fallback.pdf",
				Content: "semantic snippet", PageStart: &page, PageEnd: &page, Similarity: 0.88,
			}}}, nil
		}),
	)
	if err != nil {
		t.Fatalf("NewSearchDocuments() error = %v", err)
	}
	keywordHits, err := service.SearchDocuments(t.Context(), scope, agentapplication.SearchDocumentsInput{
		Query: "bridge", Strategy: agentapplication.SearchStrategyKeyword, DocumentID: &documentID, TopK: 2,
	})
	if err != nil || len(keywordHits) != 1 || keywordHits[0].Title != "bridge.pdf" || keywordHits[0].Score != 0 {
		t.Fatalf("keyword hits = %+v, %v", keywordHits, err)
	}
	semanticHits, err := service.SearchDocuments(t.Context(), scope, agentapplication.SearchDocumentsInput{
		Query: "vibration", Strategy: agentapplication.SearchStrategySemantic, DocumentID: &documentID, TopK: 3,
	})
	if err != nil || len(semanticHits) != 1 || semanticHits[0].Title != "Semantic title" || semanticHits[0].Score != 0.88 {
		t.Fatalf("semantic hits = %+v, %v", semanticHits, err)
	}
	if keywordCalls != 1 || semanticCalls != 1 {
		t.Fatalf("search calls keyword=%d semantic=%d", keywordCalls, semanticCalls)
	}
}

func TestSearchDocumentsRejectsLongKeywordAndAgentBounds(t *testing.T) {
	keywordCalls := 0
	semanticCalls := 0
	service, err := NewSearchDocuments(
		keywordSearchFunc(func(context.Context, accessdomain.OwnerScope, documentapplication.SearchInput) (documentapplication.SearchOutput, error) {
			keywordCalls++
			return documentapplication.SearchOutput{}, nil
		}),
		semanticSearchFunc(func(context.Context, accessdomain.OwnerScope, embeddingapplication.SemanticSearchInput) (embeddingapplication.SemanticSearchOutput, error) {
			semanticCalls++
			return embeddingapplication.SemanticSearchOutput{}, nil
		}),
	)
	if err != nil {
		t.Fatalf("NewSearchDocuments() error = %v", err)
	}
	scope, _ := accessdomain.NewOwnerScope(42)
	for _, input := range []agentapplication.SearchDocumentsInput{
		{Query: strings.Repeat("磁", documentapplication.MaxSearchQueryRunes+1), Strategy: agentapplication.SearchStrategyKeyword, TopK: 1},
		{Query: strings.Repeat("x", agentapplication.MaxSearchQueryRunes+1), Strategy: agentapplication.SearchStrategySemantic, TopK: 1},
		{Query: "x", Strategy: agentapplication.SearchStrategySemantic, TopK: agentapplication.MaxSearchTopK + 1},
	} {
		if _, err := service.SearchDocuments(t.Context(), scope, input); !errors.Is(err, agentapplication.ErrInvalidToolCall) {
			t.Fatalf("invalid input %+v error = %v", input, err)
		}
	}
	if keywordCalls != 0 || semanticCalls != 0 {
		t.Fatalf("invalid calls keyword=%d semantic=%d", keywordCalls, semanticCalls)
	}
}

func TestSearchDocumentsValidatesDependenciesAndClassifiesErrors(t *testing.T) {
	semantic := semanticSearchFunc(func(context.Context, accessdomain.OwnerScope, embeddingapplication.SemanticSearchInput) (embeddingapplication.SemanticSearchOutput, error) {
		return embeddingapplication.SemanticSearchOutput{}, nil
	})
	if _, err := NewSearchDocuments(nil, semantic); !errors.Is(err, ErrSearchDependencies) {
		t.Fatalf("nil dependency error = %v", err)
	}
	scope, _ := accessdomain.NewOwnerScope(42)
	for _, test := range []struct {
		name string
		err  error
		want error
	}{
		{name: "temporary", err: embeddingdomain.ErrEmbeddingUnavailable, want: agentapplication.ErrToolTemporarilyUnavailable},
		{name: "capacity", err: embeddingapplication.ErrEmbeddingProviderCapacityExhausted, want: agentapplication.ErrToolTemporarilyUnavailable},
		{name: "permanent", err: embeddingdomain.ErrEmbeddingAuthentication, want: agentapplication.ErrToolResultInvalid},
		{name: "not found", err: documentdomain.ErrNotFound, want: agentapplication.ErrEvidenceRefNotFound},
	} {
		t.Run(test.name, func(t *testing.T) {
			service, _ := NewSearchDocuments(
				keywordSearchFunc(func(context.Context, accessdomain.OwnerScope, documentapplication.SearchInput) (documentapplication.SearchOutput, error) {
					return documentapplication.SearchOutput{}, errors.New("unexpected keyword call")
				}),
				semanticSearchFunc(func(context.Context, accessdomain.OwnerScope, embeddingapplication.SemanticSearchInput) (embeddingapplication.SemanticSearchOutput, error) {
					return embeddingapplication.SemanticSearchOutput{}, test.err
				}),
			)
			if _, err := service.SearchDocuments(t.Context(), scope, agentapplication.SearchDocumentsInput{
				Query: "error", Strategy: agentapplication.SearchStrategySemantic, TopK: 1,
			}); !errors.Is(err, test.want) {
				t.Fatalf("SearchDocuments() error = %v, want %v", err, test.want)
			}
		})
	}
}

func TestSearchDocumentsReturnsNonNilEmptyHits(t *testing.T) {
	service, _ := NewSearchDocuments(
		keywordSearchFunc(func(context.Context, accessdomain.OwnerScope, documentapplication.SearchInput) (documentapplication.SearchOutput, error) {
			return documentapplication.SearchOutput{}, nil
		}),
		semanticSearchFunc(func(context.Context, accessdomain.OwnerScope, embeddingapplication.SemanticSearchInput) (embeddingapplication.SemanticSearchOutput, error) {
			return embeddingapplication.SemanticSearchOutput{}, nil
		}),
	)
	scope, _ := accessdomain.NewOwnerScope(42)
	hits, err := service.SearchDocuments(t.Context(), scope, agentapplication.SearchDocumentsInput{
		Query: "none", Strategy: agentapplication.SearchStrategySemantic, TopK: 1,
	})
	if err != nil || hits == nil || len(hits) != 0 {
		t.Fatalf("empty hits = %#v, %v", hits, err)
	}
}
