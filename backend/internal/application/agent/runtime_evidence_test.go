package agent

import (
	"context"
	"errors"
	"testing"

	accessdomain "rag-reasoning-platform/backend/internal/domain/access"
	documentdomain "rag-reasoning-platform/backend/internal/domain/document"
)

type evidenceChunkReaderFunc func(
	context.Context,
	accessdomain.OwnerScope,
	[]int64,
	[]int64,
) ([]documentdomain.SemanticSearchHit, error)

func (f evidenceChunkReaderFunc) ReadAgentEvidenceChunks(
	ctx context.Context,
	scope accessdomain.OwnerScope,
	documentIDs []int64,
	chunkIDs []int64,
) ([]documentdomain.SemanticSearchHit, error) {
	return f(ctx, scope, documentIDs, chunkIDs)
}

func TestRuntimeEvidenceReaderValidatesDependenciesAndClassifiesErrors(t *testing.T) {
	if _, err := NewRuntimeEvidenceReader(nil); !errors.Is(err, ErrRuntimeEvidenceDependencies) {
		t.Fatalf("nil dependency error = %v", err)
	}
	scope, _ := accessdomain.NewOwnerScope(42)
	locator := []EvidenceLocator{{EvidenceRef: "step-1:item-1", DocumentID: 1, ChunkID: 2}}
	for _, test := range []struct {
		name string
		err  error
		want error
	}{
		{name: "not found", err: documentdomain.ErrNotFound, want: ErrEvidenceRefNotFound},
		{name: "database unavailable", err: errors.New("database unavailable"), want: ErrToolTemporarilyUnavailable},
	} {
		t.Run(test.name, func(t *testing.T) {
			reader, _ := NewRuntimeEvidenceReader(evidenceChunkReaderFunc(func(
				context.Context, accessdomain.OwnerScope, []int64, []int64,
			) ([]documentdomain.SemanticSearchHit, error) {
				return nil, test.err
			}))
			if _, err := reader.ReadDocumentEvidence(t.Context(), scope, locator); !errors.Is(err, test.want) {
				t.Fatalf("ReadDocumentEvidence() error = %v, want %v", err, test.want)
			}
		})
	}
}
