package postgres

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	documentdomain "rag-reasoning-platform/backend/internal/domain/document"
	embeddingdomain "rag-reasoning-platform/backend/internal/domain/embedding"
)

// EmbeddingJobRepository 使用 PostgreSQL 保存向量任务。
type EmbeddingJobRepository struct {
	pool             *pgxpool.Pool
	schedulingPolicy embeddingdomain.JobSchedulingPolicy
}

var defaultEmbeddingJobSchedulingPolicy = embeddingdomain.JobSchedulingPolicy{
	MaxInFlightPerOwner:         1,
	MaxBorrowedInFlightPerOwner: 2,
	StarvationThreshold:         2 * time.Minute,
}

var _ embeddingdomain.JobCreator = (*EmbeddingJobRepository)(nil)
var _ embeddingdomain.JobFinder = (*EmbeddingJobRepository)(nil)

// NewEmbeddingJobRepository 创建 PostgreSQL 向量任务仓储。
func NewEmbeddingJobRepository(
	pool *pgxpool.Pool,
) *EmbeddingJobRepository {
	return NewEmbeddingJobRepositoryWithSchedulingPolicy(
		pool,
		defaultEmbeddingJobSchedulingPolicy,
	)
}

// NewEmbeddingJobRepositoryWithSchedulingPolicy 创建使用指定 Owner 公平策略的仓储。
func NewEmbeddingJobRepositoryWithSchedulingPolicy(
	pool *pgxpool.Pool,
	policy embeddingdomain.JobSchedulingPolicy,
) *EmbeddingJobRepository {
	return &EmbeddingJobRepository{
		pool:             pool,
		schedulingPolicy: policy,
	}
}

// CreateEmbeddingJob 为文档创建 queued 状态的向量任务。
func (r *EmbeddingJobRepository) CreateEmbeddingJob(
	ctx context.Context,
	documentID int64,
	modelName string,
	dimensions int,
) (embeddingdomain.Job, error) {
	transaction, err := r.pool.Begin(ctx)
	if err != nil {
		return embeddingdomain.Job{}, fmt.Errorf(
			"begin embedding job transaction: %w",
			err,
		)
	}
	defer func() {
		_ = transaction.Rollback(context.Background())
	}()

	// 系统级创建入口也必须登记 Owner，防止测试、维护命令或未来内部调用
	// 创建出调度器永远看不到的 queued 任务。
	const lockDocumentQuery = `
		SELECT owner_user_id
		FROM documents
		WHERE id = $1
		FOR SHARE
	`
	var ownerUserID int64
	err = transaction.QueryRow(
		ctx,
		lockDocumentQuery,
		documentID,
	).Scan(&ownerUserID)
	if errors.Is(err, pgx.ErrNoRows) {
		return embeddingdomain.Job{}, documentdomain.ErrNotFound
	}
	if err != nil {
		return embeddingdomain.Job{}, fmt.Errorf(
			"lock document before creating embedding job: %w",
			err,
		)
	}
	if err := ensureEmbeddingOwnerSchedule(
		ctx,
		transaction,
		ownerUserID,
	); err != nil {
		return embeddingdomain.Job{}, err
	}

	const insertJobQuery = `
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
		transaction.QueryRow(
			ctx,
			insertJobQuery,
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
	if err := transaction.Commit(ctx); err != nil {
		return embeddingdomain.Job{}, fmt.Errorf(
			"commit embedding job: %w",
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
