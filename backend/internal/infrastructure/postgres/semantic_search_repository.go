package postgres

import (
	"context"
	"fmt"

	"github.com/jackc/pgx/v5"
	"github.com/pgvector/pgvector-go"

	documentdomain "rag-reasoning-platform/backend/internal/domain/document"
	embeddingdomain "rag-reasoning-platform/backend/internal/domain/embedding"
)

var _ documentdomain.SemanticChunkSearcher = (*ChunkRepository)(nil)

// SearchSimilar 使用 pgvector 的精确余弦距离查询最相近的文本块。
//
// 第一版数据规模较小，因此不创建 HNSW/IVFFlat 近似索引。PostgreSQL 会精确比较
// 满足模型、维度、文档状态和可选文档过滤条件的全部候选向量，再返回前 Limit 条。
func (r *ChunkRepository) SearchSimilar(
	ctx context.Context,
	options documentdomain.SemanticSearchOptions,
) ([]documentdomain.SemanticSearchHit, error) {
	const query = `
		SELECT
			chunk.id,
			chunk.document_id,
			chunk.chunk_index,
			source_document.title,
			source_document.original_name,
			source_document.mime_type,
			chunk.content,
			chunk.page_start,
			chunk.page_end,
			1 - (
				chunk_embedding.embedding
				OPERATOR(public.<=>)
				$1::public.vector
			) AS similarity
		FROM chunk_embeddings AS chunk_embedding
		JOIN embedding_jobs AS embedding_job
			ON embedding_job.id = chunk_embedding.embedding_job_id
		JOIN text_chunks AS chunk
			ON chunk.id = chunk_embedding.chunk_id
		JOIN documents AS source_document
			ON source_document.id = chunk.document_id
		WHERE source_document.status = $2
			AND embedding_job.status = $3
			AND embedding_job.model_name = $4
			AND embedding_job.dimensions = $5
			AND ($6::BIGINT IS NULL OR chunk.document_id = $6)
		ORDER BY
			chunk_embedding.embedding
				OPERATOR(public.<=>)
				$1::public.vector ASC,
			chunk.document_id ASC,
			chunk.chunk_index ASC,
			chunk.id ASC
		LIMIT $7
	`

	rows, err := r.pool.Query(
		ctx,
		query,
		pgvector.NewVector(options.QueryVector),
		documentdomain.StatusReady,
		embeddingdomain.JobStatusSucceeded,
		options.ModelName,
		options.Dimensions,
		options.DocumentID,
		options.Limit,
	)
	if err != nil {
		return nil, fmt.Errorf("query semantically similar document chunks: %w", err)
	}
	defer rows.Close()

	// 非 nil 空切片确保未来 Handler 可以稳定编码为 JSON []，而不是 null。
	hits := make([]documentdomain.SemanticSearchHit, 0)
	for rows.Next() {
		hit, err := scanSemanticSearchHit(rows)
		if err != nil {
			return nil, fmt.Errorf("scan semantically similar document chunk: %w", err)
		}
		hits = append(hits, hit)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("iterate semantically similar document chunks: %w", err)
	}

	return hits, nil
}

// scanSemanticSearchHit 把四张表 JOIN 后的一行转换成稳定的领域命中结果。
// Scan 参数顺序必须与 SearchSimilar 的 SELECT 字段顺序完全一致。
func scanSemanticSearchHit(row pgx.Row) (documentdomain.SemanticSearchHit, error) {
	var hit documentdomain.SemanticSearchHit

	err := row.Scan(
		&hit.ChunkID,
		&hit.DocumentID,
		&hit.ChunkIndex,
		&hit.Title,
		&hit.OriginalName,
		&hit.MIMEType,
		&hit.Content,
		&hit.PageStart,
		&hit.PageEnd,
		&hit.Similarity,
	)
	if err != nil {
		return documentdomain.SemanticSearchHit{}, err
	}

	return hit, nil
}
