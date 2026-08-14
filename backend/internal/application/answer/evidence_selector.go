package answer

import documentdomain "rag-reasoning-platform/backend/internal/domain/document"

// selectAnswerEvidence 从相似度已按降序排列的候选中选择最终问答证据。
//
// diversifyDocuments 为 true 时使用两遍选择：
//  1. 第一遍先为每篇文档选择排名最高的一条，保证全库问答的来源多样性；
//  2. 第二遍再按原始相似度顺序补满 limit，避免候选文档较少时浪费证据名额。
//
// diversifyDocuments 为 false 时只按原始顺序选择，适合已经限定 document_id 的
// 单文档问答。两种模式都会按 ChunkID 去重，并返回非 nil 切片。
func selectAnswerEvidence(
	hits []documentdomain.SemanticSearchHit,
	limit int,
	diversifyDocuments bool,
) []documentdomain.SemanticSearchHit {
	capacity := limit
	if capacity < 0 {
		capacity = 0
	}
	if capacity > len(hits) {
		capacity = len(hits)
	}

	selected := make([]documentdomain.SemanticSearchHit, 0, capacity)
	if limit <= 0 || len(hits) == 0 {
		return selected
	}

	selectedChunkIDs := make(map[int64]struct{}, capacity)
	if diversifyDocuments {
		selectedDocumentIDs := make(map[int64]struct{}, capacity)

		for _, hit := range hits {
			if len(selected) == limit {
				return selected
			}
			if _, duplicate := selectedChunkIDs[hit.ChunkID]; duplicate {
				continue
			}
			if _, alreadyRepresented := selectedDocumentIDs[hit.DocumentID]; alreadyRepresented {
				continue
			}

			selected = append(selected, hit)
			selectedChunkIDs[hit.ChunkID] = struct{}{}
			selectedDocumentIDs[hit.DocumentID] = struct{}{}
		}
	}

	// 第二遍既负责为多样化结果补位，也负责单文档模式的原序选择。
	for _, hit := range hits {
		if len(selected) == limit {
			break
		}
		if _, duplicate := selectedChunkIDs[hit.ChunkID]; duplicate {
			continue
		}

		selected = append(selected, hit)
		selectedChunkIDs[hit.ChunkID] = struct{}{}
	}

	return selected
}
