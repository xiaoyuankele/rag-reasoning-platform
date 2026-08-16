package postgres

import (
	"context"
	"errors"
	"fmt"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	accessdomain "rag-reasoning-platform/backend/internal/domain/access"
	documentdomain "rag-reasoning-platform/backend/internal/domain/document"
	embeddingdomain "rag-reasoning-platform/backend/internal/domain/embedding"
)

// ScopedEmbeddingJobRepository 为已认证用户创建和查询向量任务。
// Embedding Worker 继续使用系统级 EmbeddingJobRepository。
type ScopedEmbeddingJobRepository struct {
	pool *pgxpool.Pool
}

var _ embeddingdomain.ScopedJobCreator = (*ScopedEmbeddingJobRepository)(nil)
var _ embeddingdomain.ScopedJobFinder = (*ScopedEmbeddingJobRepository)(nil)

// NewScopedEmbeddingJobRepository 创建带文档所有者边界的向量任务仓储。
func NewScopedEmbeddingJobRepository(
	pool *pgxpool.Pool,
) *ScopedEmbeddingJobRepository {
	return &ScopedEmbeddingJobRepository{pool: pool}
}

// CreateEmbeddingJob 只在文档属于当前 OwnerScope 时创建 queued 任务。
// 文档的 ready 状态由 Application 校验；本方法负责归属与写入的原子边界。
func (r *ScopedEmbeddingJobRepository) CreateEmbeddingJob(
	ctx context.Context,
	scope accessdomain.OwnerScope,
	documentID int64,
	modelName string,
	dimensions int,
) (embeddingdomain.Job, error) {
	if !scope.IsValid() {
		return embeddingdomain.Job{}, accessdomain.ErrInvalidOwnerScope
	}

	const query = `
		INSERT INTO embedding_jobs (
			document_id,
			model_name,
			dimensions
		)
		SELECT id, $3, $4
		FROM documents
		WHERE id = $1
		  AND owner_user_id = $2
		RETURNING
			id,
			document_id,
			model_name,
			dimensions,
			status,
			attempt_count,
			error_message,
			next_attempt_at,
			prompt_tokens,
			total_tokens,
			created_at,
			updated_at,
			started_at,
			completed_at
	`

	createdJob, err := scanEmbeddingJob(
		r.pool.QueryRow(
			ctx,
			query,
			documentID,
			scope.OwnerUserID(),
			modelName,
			dimensions,
		),
	)
	if errors.Is(err, pgx.ErrNoRows) || isForeignKeyViolation(err) {
		return embeddingdomain.Job{}, documentdomain.ErrNotFound
	}
	if isConstraintViolation(err, "uq_embedding_jobs_active") {
		return embeddingdomain.Job{}, embeddingdomain.ErrActiveJobExists
	}
	if err != nil {
		return embeddingdomain.Job{}, fmt.Errorf(
			"create scoped embedding job: %w",
			err,
		)
	}
	return createdJob, nil
}

// GetEmbeddingJobByID 通过任务与文档 JOIN 强制校验任务所属用户。
func (r *ScopedEmbeddingJobRepository) GetEmbeddingJobByID(
	ctx context.Context,
	scope accessdomain.OwnerScope,
	jobID int64,
) (embeddingdomain.Job, error) {
	if !scope.IsValid() {
		return embeddingdomain.Job{}, accessdomain.ErrInvalidOwnerScope
	}

	const query = `
		SELECT
			job.id,
			job.document_id,
			job.model_name,
			job.dimensions,
			job.status,
			job.attempt_count,
			job.error_message,
			job.next_attempt_at,
			job.prompt_tokens,
			job.total_tokens,
			job.created_at,
			job.updated_at,
			job.started_at,
			job.completed_at
		FROM embedding_jobs AS job
		JOIN documents AS source_document
		  ON source_document.id = job.document_id
		WHERE job.id = $1
		  AND source_document.owner_user_id = $2
	`

	foundJob, err := scanEmbeddingJob(
		r.pool.QueryRow(ctx, query, jobID, scope.OwnerUserID()),
	)
	if errors.Is(err, pgx.ErrNoRows) {
		return embeddingdomain.Job{}, embeddingdomain.ErrJobNotFound
	}
	if err != nil {
		return embeddingdomain.Job{}, fmt.Errorf(
			"get scoped embedding job by ID: %w",
			err,
		)
	}
	return foundJob, nil
}
