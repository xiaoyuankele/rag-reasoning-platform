package agentruntime

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"unicode/utf8"

	agentapplication "rag-reasoning-platform/backend/internal/application/agent"
	documentapplication "rag-reasoning-platform/backend/internal/application/document"
	embeddingapplication "rag-reasoning-platform/backend/internal/application/embedding"
	accessdomain "rag-reasoning-platform/backend/internal/domain/access"
	documentdomain "rag-reasoning-platform/backend/internal/domain/document"
	embeddingdomain "rag-reasoning-platform/backend/internal/domain/embedding"
)

var ErrSearchDependencies = errors.New("agent runtime search dependencies must be provided")

type KeywordSearchService interface {
	Search(
		ctx context.Context,
		scope accessdomain.OwnerScope,
		input documentapplication.SearchInput,
	) (documentapplication.SearchOutput, error)
}

type SemanticSearchService interface {
	Search(
		ctx context.Context,
		scope accessdomain.OwnerScope,
		input embeddingapplication.SemanticSearchInput,
	) (embeddingapplication.SemanticSearchOutput, error)
}

type SearchDocuments struct {
	keyword  KeywordSearchService
	semantic SemanticSearchService
}

var _ agentapplication.SearchDocumentsPort = (*SearchDocuments)(nil)

func NewSearchDocuments(
	keyword KeywordSearchService,
	semantic SemanticSearchService,
) (*SearchDocuments, error) {
	if keyword == nil || semantic == nil {
		return nil, ErrSearchDependencies
	}
	return &SearchDocuments{keyword: keyword, semantic: semantic}, nil
}

func (s *SearchDocuments) SearchDocuments(
	ctx context.Context,
	scope accessdomain.OwnerScope,
	input agentapplication.SearchDocumentsInput,
) ([]agentapplication.SearchDocumentsHit, error) {
	if !scope.IsValid() {
		return nil, accessdomain.ErrInvalidOwnerScope
	}
	query := strings.TrimSpace(input.Query)
	if query == "" || !utf8.ValidString(query) ||
		utf8.RuneCountInString(query) > agentapplication.MaxSearchQueryRunes ||
		input.TopK <= 0 || input.TopK > agentapplication.MaxSearchTopK ||
		(input.DocumentID != nil && *input.DocumentID <= 0) {
		return nil, agentapplication.ErrInvalidToolCall
	}
	input.Query = query
	switch input.Strategy {
	case agentapplication.SearchStrategyKeyword:
		if utf8.RuneCountInString(input.Query) > documentapplication.MaxSearchQueryRunes {
			return nil, agentapplication.ErrInvalidToolCall
		}
		output, err := s.keyword.Search(ctx, scope, documentapplication.SearchInput{
			Query: input.Query, DocumentID: input.DocumentID, Page: 1, PageSize: int64(input.TopK),
		})
		if err != nil {
			return nil, classifyKeywordError(err)
		}
		hits := make([]agentapplication.SearchDocumentsHit, 0, len(output.Hits))
		for _, hit := range output.Hits {
			hits = append(hits, agentapplication.SearchDocumentsHit{
				DocumentID: hit.DocumentID,
				ChunkID:    hit.ChunkID,
				Title:      searchTitle(hit.Title, hit.OriginalName),
				PageStart:  hit.PageStart,
				PageEnd:    hit.PageEnd,
				Snippet:    hit.Content,
				Score:      0,
			})
		}
		return hits, nil
	case agentapplication.SearchStrategySemantic:
		output, err := s.semantic.Search(ctx, scope, embeddingapplication.SemanticSearchInput{
			Query: input.Query, DocumentID: input.DocumentID, TopK: input.TopK,
		})
		if err != nil {
			return nil, classifySemanticError(err)
		}
		hits := make([]agentapplication.SearchDocumentsHit, 0, len(output.Hits))
		for _, hit := range output.Hits {
			hits = append(hits, agentapplication.SearchDocumentsHit{
				DocumentID: hit.DocumentID,
				ChunkID:    hit.ChunkID,
				Title:      searchTitle(hit.Title, hit.OriginalName),
				PageStart:  hit.PageStart,
				PageEnd:    hit.PageEnd,
				Snippet:    hit.Content,
				Score:      hit.Similarity,
			})
		}
		return hits, nil
	default:
		return nil, agentapplication.ErrInvalidToolCall
	}
}

func searchTitle(title *string, originalName string) string {
	if title != nil && strings.TrimSpace(*title) != "" {
		return *title
	}
	return originalName
}

func classifyKeywordError(err error) error {
	switch {
	case errors.Is(err, context.Canceled), errors.Is(err, context.DeadlineExceeded):
		return err
	case errors.Is(err, documentdomain.ErrNotFound):
		return errors.Join(agentapplication.ErrEvidenceRefNotFound, err)
	case errors.Is(err, documentapplication.ErrSearchQueryRequired),
		errors.Is(err, documentapplication.ErrSearchQueryInvalidUTF8),
		errors.Is(err, documentapplication.ErrSearchQueryTooLong),
		errors.Is(err, documentapplication.ErrInvalidID),
		errors.Is(err, documentapplication.ErrInvalidPage),
		errors.Is(err, documentapplication.ErrInvalidPageSize):
		return errors.Join(agentapplication.ErrInvalidToolCall, err)
	default:
		return errors.Join(
			agentapplication.ErrToolTemporarilyUnavailable,
			fmt.Errorf("keyword search unavailable: %w", err),
		)
	}
}

func classifySemanticError(err error) error {
	switch {
	case errors.Is(err, context.Canceled), errors.Is(err, context.DeadlineExceeded):
		return err
	case errors.Is(err, documentdomain.ErrNotFound):
		return errors.Join(agentapplication.ErrEvidenceRefNotFound, err)
	case errors.Is(err, embeddingapplication.ErrSemanticSearchQueryRequired),
		errors.Is(err, embeddingapplication.ErrSemanticSearchQueryInvalidUTF8),
		errors.Is(err, embeddingapplication.ErrSemanticSearchQueryTooLong),
		errors.Is(err, embeddingapplication.ErrInvalidSemanticSearchTopK),
		errors.Is(err, embeddingapplication.ErrInvalidDocumentID):
		return errors.Join(agentapplication.ErrInvalidToolCall, err)
	case errors.Is(err, embeddingapplication.ErrDocumentEmbeddingsNotReady),
		errors.Is(err, embeddingapplication.ErrEmbeddingProviderCapacityExhausted),
		errors.Is(err, embeddingdomain.ErrEmbeddingRateLimited),
		errors.Is(err, embeddingdomain.ErrEmbeddingUnavailable):
		return errors.Join(agentapplication.ErrToolTemporarilyUnavailable, err)
	case errors.Is(err, embeddingdomain.ErrEmbeddingAuthentication),
		errors.Is(err, embeddingdomain.ErrEmbeddingQuotaExceeded),
		errors.Is(err, embeddingdomain.ErrEmbeddingRequestRejected),
		errors.Is(err, embeddingdomain.ErrInvalidEmbeddingResponse):
		return errors.Join(agentapplication.ErrToolResultInvalid, err)
	default:
		return errors.Join(
			agentapplication.ErrToolTemporarilyUnavailable,
			fmt.Errorf("semantic search unavailable: %w", err),
		)
	}
}
