package answer

import (
	"reflect"
	"testing"

	documentdomain "rag-reasoning-platform/backend/internal/domain/document"
)

func TestSelectAnswerEvidenceDiversifiesDocumentsBeforeFilling(
	t *testing.T,
) {
	hits := []documentdomain.SemanticSearchHit{
		{ChunkID: 1, DocumentID: 225, Similarity: 0.95},
		{ChunkID: 2, DocumentID: 225, Similarity: 0.94},
		{ChunkID: 3, DocumentID: 225, Similarity: 0.93},
		{ChunkID: 4, DocumentID: 208, Similarity: 0.92},
		{ChunkID: 5, DocumentID: 20, Similarity: 0.91},
		{ChunkID: 6, DocumentID: 208, Similarity: 0.90},
	}

	selected := selectAnswerEvidence(hits, 5, true)

	// 第一遍依次取得文档 225、208、20 的最高命中，第二遍才回到原顺序
	// 使用 chunk 2 和 3 补满五条。
	wantedChunkIDs := []int64{1, 4, 5, 2, 3}
	if got := answerEvidenceChunkIDs(selected); !reflect.DeepEqual(
		got,
		wantedChunkIDs,
	) {
		t.Fatalf("selected chunk IDs = %v, want %v", got, wantedChunkIDs)
	}
}

func TestSelectAnswerEvidencePreservesOrderWithoutDiversification(
	t *testing.T,
) {
	hits := []documentdomain.SemanticSearchHit{
		{ChunkID: 1, DocumentID: 225, Similarity: 0.95},
		{ChunkID: 2, DocumentID: 225, Similarity: 0.94},
		{ChunkID: 3, DocumentID: 225, Similarity: 0.93},
	}

	selected := selectAnswerEvidence(hits, 2, false)

	wantedChunkIDs := []int64{1, 2}
	if got := answerEvidenceChunkIDs(selected); !reflect.DeepEqual(
		got,
		wantedChunkIDs,
	) {
		t.Fatalf("selected chunk IDs = %v, want %v", got, wantedChunkIDs)
	}
}

func TestSelectAnswerEvidenceSkipsDuplicateChunks(t *testing.T) {
	hits := []documentdomain.SemanticSearchHit{
		{ChunkID: 1, DocumentID: 225, Similarity: 0.95},
		{ChunkID: 1, DocumentID: 225, Similarity: 0.95},
		{ChunkID: 2, DocumentID: 208, Similarity: 0.92},
		{ChunkID: 3, DocumentID: 20, Similarity: 0.91},
	}

	selected := selectAnswerEvidence(hits, 3, true)

	wantedChunkIDs := []int64{1, 2, 3}
	if got := answerEvidenceChunkIDs(selected); !reflect.DeepEqual(
		got,
		wantedChunkIDs,
	) {
		t.Fatalf("selected chunk IDs = %v, want %v", got, wantedChunkIDs)
	}
}

func TestSelectAnswerEvidenceDoesNotModifyCandidates(t *testing.T) {
	hits := []documentdomain.SemanticSearchHit{
		{ChunkID: 1, DocumentID: 225, Similarity: 0.95},
		{ChunkID: 2, DocumentID: 225, Similarity: 0.94},
		{ChunkID: 3, DocumentID: 208, Similarity: 0.92},
	}
	original := append([]documentdomain.SemanticSearchHit(nil), hits...)

	_ = selectAnswerEvidence(hits, 2, true)

	if !reflect.DeepEqual(hits, original) {
		t.Fatalf("candidate hits changed to %#v, want original %#v", hits, original)
	}
}

func TestSelectAnswerEvidenceReturnsNonNilEmptySlice(t *testing.T) {
	tests := []struct {
		name  string
		hits  []documentdomain.SemanticSearchHit
		limit int
	}{
		{name: "no candidates", hits: nil, limit: 5},
		{name: "zero limit", hits: []documentdomain.SemanticSearchHit{{ChunkID: 1}}, limit: 0},
		{name: "negative limit", hits: []documentdomain.SemanticSearchHit{{ChunkID: 1}}, limit: -1},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			selected := selectAnswerEvidence(test.hits, test.limit, true)
			if selected == nil || len(selected) != 0 {
				t.Fatalf("selected = %#v, want non-nil empty slice", selected)
			}
		})
	}
}

func answerEvidenceChunkIDs(
	hits []documentdomain.SemanticSearchHit,
) []int64 {
	chunkIDs := make([]int64, 0, len(hits))
	for _, hit := range hits {
		chunkIDs = append(chunkIDs, hit.ChunkID)
	}
	return chunkIDs
}
