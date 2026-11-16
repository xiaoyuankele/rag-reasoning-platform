package agent

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"strings"

	accessdomain "rag-reasoning-platform/backend/internal/domain/access"
	documentdomain "rag-reasoning-platform/backend/internal/domain/document"
)

var ErrRuntimeEvidenceDependencies = errors.New("agent runtime evidence dependencies must be provided")

type ScopedEvidenceChunkReader interface {
	ReadAgentEvidenceChunks(
		ctx context.Context,
		scope accessdomain.OwnerScope,
		documentIDs []int64,
		chunkIDs []int64,
	) ([]documentdomain.SemanticSearchHit, error)
}

type RuntimeEvidenceReader struct {
	chunks ScopedEvidenceChunkReader
}

func NewRuntimeEvidenceReader(chunks ScopedEvidenceChunkReader) (*RuntimeEvidenceReader, error) {
	if chunks == nil {
		return nil, ErrRuntimeEvidenceDependencies
	}
	return &RuntimeEvidenceReader{chunks: chunks}, nil
}

func (r *RuntimeEvidenceReader) ReadDocumentEvidence(
	ctx context.Context,
	scope accessdomain.OwnerScope,
	locators []EvidenceLocator,
) ([]ReadEvidenceItem, error) {
	if !scope.IsValid() {
		return nil, accessdomain.ErrInvalidOwnerScope
	}
	if len(locators) == 0 || len(locators) > MaxReadEvidenceRefs {
		return nil, ErrInvalidToolCall
	}
	documentIDs := make([]int64, 0, len(locators))
	chunkIDs := make([]int64, 0, len(locators))
	seen := make(map[string]struct{}, len(locators))
	for _, locator := range locators {
		if strings.TrimSpace(locator.EvidenceRef) == "" ||
			strings.TrimSpace(locator.EvidenceRef) != locator.EvidenceRef ||
			locator.DocumentID <= 0 || locator.ChunkID <= 0 {
			return nil, ErrInvalidToolCall
		}
		if _, duplicate := seen[locator.EvidenceRef]; duplicate {
			return nil, ErrInvalidToolCall
		}
		seen[locator.EvidenceRef] = struct{}{}
		documentIDs = append(documentIDs, locator.DocumentID)
		chunkIDs = append(chunkIDs, locator.ChunkID)
	}
	hits, err := r.chunks.ReadAgentEvidenceChunks(ctx, scope, documentIDs, chunkIDs)
	if err != nil {
		switch {
		case errors.Is(err, context.Canceled), errors.Is(err, context.DeadlineExceeded):
			return nil, err
		case errors.Is(err, documentdomain.ErrNotFound):
			return nil, errors.Join(ErrEvidenceRefNotFound, err)
		default:
			return nil, errors.Join(ErrToolTemporarilyUnavailable, fmt.Errorf("agent evidence unavailable: %w", err))
		}
	}
	if len(hits) != len(locators) {
		return nil, ErrEvidenceRefNotFound
	}
	items := make([]ReadEvidenceItem, 0, len(hits))
	for index, hit := range hits {
		locator := locators[index]
		if hit.DocumentID != locator.DocumentID || hit.ChunkID != locator.ChunkID {
			return nil, ErrEvidenceRefNotFound
		}
		contentHash := sha256.Sum256([]byte(hit.Content))
		items = append(items, ReadEvidenceItem{
			DocumentID:  hit.DocumentID,
			ChunkID:     hit.ChunkID,
			Title:       runtimeEvidenceTitle(hit.Title, hit.OriginalName),
			PageStart:   hit.PageStart,
			PageEnd:     hit.PageEnd,
			Excerpt:     boundedRuntimeEvidenceExcerpt(hit.Content, MaxEvidenceExcerptRunes),
			ContentHash: hex.EncodeToString(contentHash[:]),
		})
	}
	return items, nil
}

func runtimeEvidenceTitle(title *string, originalName string) string {
	if title != nil && strings.TrimSpace(*title) != "" {
		return *title
	}
	return originalName
}

func boundedRuntimeEvidenceExcerpt(content string, limit int) string {
	if limit <= 0 {
		return ""
	}
	content = strings.TrimSpace(content)
	runes := []rune(content)
	if len(runes) <= limit {
		return content
	}
	if limit == 1 {
		return "…"
	}
	return strings.TrimSpace(string(runes[:limit-1])) + "…"
}
