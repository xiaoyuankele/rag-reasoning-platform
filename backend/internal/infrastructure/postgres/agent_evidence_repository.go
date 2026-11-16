package postgres

import (
	"context"
	"fmt"

	"github.com/jackc/pgx/v5/pgxpool"

	accessdomain "rag-reasoning-platform/backend/internal/domain/access"
	documentdomain "rag-reasoning-platform/backend/internal/domain/document"
)

type AgentEvidenceRepository struct {
	pool *pgxpool.Pool
}

func NewAgentEvidenceRepository(pool *pgxpool.Pool) *AgentEvidenceRepository {
	return &AgentEvidenceRepository{pool: pool}
}

func (r *AgentEvidenceRepository) ReadAgentEvidenceChunks(
	ctx context.Context,
	scope accessdomain.OwnerScope,
	documentIDs []int64,
	chunkIDs []int64,
) ([]documentdomain.SemanticSearchHit, error) {
	if !scope.IsValid() {
		return nil, accessdomain.ErrInvalidOwnerScope
	}
	if len(documentIDs) == 0 || len(documentIDs) != len(chunkIDs) {
		return nil, documentdomain.ErrNotFound
	}
	for index := range documentIDs {
		if documentIDs[index] <= 0 || chunkIDs[index] <= 0 {
			return nil, documentdomain.ErrNotFound
		}
	}

	rows, err := r.pool.Query(
		ctx,
		`WITH requested AS (
			SELECT document_id, chunk_id, ordinal
			FROM UNNEST($2::BIGINT[], $3::BIGINT[])
			     WITH ORDINALITY AS input(document_id, chunk_id, ordinal)
		 )
		 SELECT document.id,
		        chunk.id,
		        document.title,
		        document.original_name,
		        chunk.page_start,
		        chunk.page_end,
		        chunk.content
		 FROM requested
		 JOIN documents AS document
		   ON document.id = requested.document_id
		  AND document.owner_user_id = $1
		  AND document.status = 'ready'
		 JOIN text_chunks AS chunk
		   ON chunk.id = requested.chunk_id
		  AND chunk.document_id = document.id
		 ORDER BY requested.ordinal`,
		scope.OwnerUserID(),
		documentIDs,
		chunkIDs,
	)
	if err != nil {
		return nil, fmt.Errorf("query scoped agent evidence: %w", err)
	}
	defer rows.Close()

	hits := make([]documentdomain.SemanticSearchHit, 0, len(documentIDs))
	for rows.Next() {
		var hit documentdomain.SemanticSearchHit
		if err := rows.Scan(
			&hit.DocumentID,
			&hit.ChunkID,
			&hit.Title,
			&hit.OriginalName,
			&hit.PageStart,
			&hit.PageEnd,
			&hit.Content,
		); err != nil {
			return nil, fmt.Errorf("scan scoped agent evidence: %w", err)
		}
		hits = append(hits, hit)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("iterate scoped agent evidence: %w", err)
	}
	if len(hits) != len(documentIDs) {
		return nil, documentdomain.ErrNotFound
	}
	return hits, nil
}
