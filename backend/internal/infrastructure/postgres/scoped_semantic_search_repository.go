package postgres

import (
	"context"
	"errors"
	"fmt"

	"github.com/jackc/pgx/v5"
	"github.com/pgvector/pgvector-go"

	accessdomain "rag-reasoning-platform/backend/internal/domain/access"
	documentdomain "rag-reasoning-platform/backend/internal/domain/document"
	embeddingdomain "rag-reasoning-platform/backend/internal/domain/embedding"
)

var _ documentdomain.ScopedSemanticChunkSearcher = (*ScopedChunkRepository)(nil)
var _ documentdomain.ScopedSemanticEmbeddingReadinessChecker = (*ScopedChunkRepository)(nil)

// HasCompleteSemanticEmbeddings 只核对当前 OwnerScope 内指定文档的向量完整性。
// 文档不存在和属于其他用户都返回 ErrNotFound，并且该检查发生在远程 Embedder 调用前。
func (r *ScopedChunkRepository) HasCompleteSemanticEmbeddings(
	ctx context.Context,
	scope accessdomain.OwnerScope,
	options documentdomain.SemanticEmbeddingReadinessOptions,
) (bool, error) {
	if !scope.IsValid() {
		return false, accessdomain.ErrInvalidOwnerScope
	}

	const query = `
		SELECT
			source_document.status,
			COUNT(chunk.id) AS chunk_count,
			COUNT(chunk_embedding.chunk_id) FILTER (
				WHERE embedding_job.status = $3
				  AND embedding_job.model_name = $4
				  AND embedding_job.dimensions = $5
			) AS matching_embedding_count
		FROM documents AS source_document
		LEFT JOIN text_chunks AS chunk
		  ON chunk.document_id = source_document.id
		LEFT JOIN chunk_embeddings AS chunk_embedding
		  ON chunk_embedding.chunk_id = chunk.id
		LEFT JOIN embedding_jobs AS embedding_job
		  ON embedding_job.id = chunk_embedding.embedding_job_id
		WHERE source_document.id = $1
		  AND source_document.owner_user_id = $2
		GROUP BY source_document.id, source_document.status
	`

	var status documentdomain.Status
	var chunkCount int64
	var matchingEmbeddingCount int64
	err := r.pool.QueryRow(
		ctx,
		query,
		options.DocumentID,
		scope.OwnerUserID(),
		embeddingdomain.JobStatusSucceeded,
		options.ModelName,
		options.Dimensions,
	).Scan(&status, &chunkCount, &matchingEmbeddingCount)
	if errors.Is(err, pgx.ErrNoRows) {
		return false, documentdomain.ErrNotFound
	}
	if err != nil {
		return false, fmt.Errorf(
			"check scoped document semantic embedding readiness: %w",
			err,
		)
	}

	return status == documentdomain.StatusReady &&
		chunkCount > 0 &&
		matchingEmbeddingCount == chunkCount, nil
}

// SearchSimilar 在当前 OwnerScope 的 ready 文档向量中执行精确余弦检索。
// 全库搜索只表示当前用户的全部文档；可选 DocumentID 不能扩大这个候选集合。
func (r *ScopedChunkRepository) SearchSimilar(
	ctx context.Context,
	scope accessdomain.OwnerScope,
	options documentdomain.SemanticSearchOptions,
) ([]documentdomain.SemanticSearchHit, error) {
	if !scope.IsValid() {
		return nil, accessdomain.ErrInvalidOwnerScope
	}

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
		WHERE source_document.owner_user_id = $2
		  AND source_document.status = $3
		  AND embedding_job.status = $4
		  AND embedding_job.model_name = $5
		  AND embedding_job.dimensions = $6
		  AND ($7::BIGINT IS NULL OR chunk.document_id = $7)
		ORDER BY
			chunk_embedding.embedding
				OPERATOR(public.<=>)
				$1::public.vector ASC,
			chunk.document_id ASC,
			chunk.chunk_index ASC,
			chunk.id ASC
		LIMIT $8
	`

	rows, err := r.pool.Query(
		ctx,
		query,
		pgvector.NewVector(options.QueryVector),
		scope.OwnerUserID(),
		documentdomain.StatusReady,
		embeddingdomain.JobStatusSucceeded,
		options.ModelName,
		options.Dimensions,
		options.DocumentID,
		options.Limit,
	)
	if err != nil {
		return nil, fmt.Errorf(
			"query scoped semantically similar document chunks: %w",
			err,
		)
	}
	defer rows.Close()

	hits := make([]documentdomain.SemanticSearchHit, 0)
	for rows.Next() {
		hit, err := scanSemanticSearchHit(rows)
		if err != nil {
			return nil, fmt.Errorf(
				"scan scoped semantically similar document chunk: %w",
				err,
			)
		}
		hits = append(hits, hit)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf(
			"iterate scoped semantically similar document chunks: %w",
			err,
		)
	}

	return hits, nil
}
