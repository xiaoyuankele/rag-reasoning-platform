package postgres

import (
	"context"
	"errors"
	"fmt"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	documentdomain "rag-reasoning-platform/backend/internal/domain/document"
	embeddingdomain "rag-reasoning-platform/backend/internal/domain/embedding"
)

// EmbeddingJobRepository 使用 PostgreSQL 保存向量任务。
type EmbeddingJobRepository struct {
	pool *pgxpool.Pool
}

var _ embeddingdomain.JobCreator = (*EmbeddingJobRepository)(nil)
var _ embeddingdomain.JobFinder = (*EmbeddingJobRepository)(nil)

// NewEmbeddingJobRepository 创建 PostgreSQL 向量任务仓储。
func NewEmbeddingJobRepository(
	pool *pgxpool.Pool,
) *EmbeddingJobRepository {
	return &EmbeddingJobRepository{pool: pool}
}

// CreateEmbeddingJob 为文档创建 queued 状态的向量任务。
func (r *EmbeddingJobRepository) CreateEmbeddingJob(
	ctx context.Context,
	documentID int64,
	modelName string,
	dimensions int,
) (embeddingdomain.Job, error) {
	const query = `
		INSERT INTO embedding_jobs (
			document_id,
			model_name,
			dimensions
		)
		VALUES ($1, $2, $3)
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
			modelName,
			dimensions,
		),
	)
	if isConstraintViolation(err, "uq_embedding_jobs_active") {
		return embeddingdomain.Job{}, embeddingdomain.ErrActiveJobExists
	}
	if isForeignKeyViolation(err) {
		// 文档可能在 Application 查询后、任务写入前被并发删除。
		return embeddingdomain.Job{}, documentdomain.ErrNotFound
	}
	if err != nil {
		return embeddingdomain.Job{}, fmt.Errorf(
			"create embedding job: %w",
			err,
		)
	}

	return createdJob, nil
}

// GetEmbeddingJobByID 按主键读取一条向量任务记录。
func (r *EmbeddingJobRepository) GetEmbeddingJobByID(
	ctx context.Context,
	jobID int64,
) (embeddingdomain.Job, error) {
	const query = `
		SELECT
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
		FROM embedding_jobs
		WHERE id = $1
	`

	foundJob, err := scanEmbeddingJob(
		r.pool.QueryRow(ctx, query, jobID),
	)
	if errors.Is(err, pgx.ErrNoRows) {
		return embeddingdomain.Job{}, embeddingdomain.ErrJobNotFound
	}
	if err != nil {
		return embeddingdomain.Job{}, fmt.Errorf(
			"get embedding job by ID: %w",
			err,
		)
	}

	return foundJob, nil
}

// scanEmbeddingJob 按照 SQL SELECT/RETURNING 的固定列顺序，把一行数据库
// 结果转换成领域层 Job。所有向量任务查询共用它，避免各方法重复 Scan 逻辑。
func scanEmbeddingJob(row pgx.Row) (embeddingdomain.Job, error) {
	var job embeddingdomain.Job
	var status string

	err := row.Scan(
		&job.ID,
		&job.DocumentID,
		&job.ModelName,
		&job.Dimensions,
		&status,
		&job.AttemptCount,
		&job.ErrorMessage,
		&job.NextAttemptAt,
		&job.PromptTokens,
		&job.TotalTokens,
		&job.CreatedAt,
		&job.UpdatedAt,
		&job.StartedAt,
		&job.CompletedAt,
	)
	if err != nil {
		return embeddingdomain.Job{}, err
	}

	job.Status = embeddingdomain.JobStatus(status)
	if !job.Status.IsValid() {
		return embeddingdomain.Job{}, fmt.Errorf(
			"invalid embedding job status %q",
			status,
		)
	}

	return job, nil
}
