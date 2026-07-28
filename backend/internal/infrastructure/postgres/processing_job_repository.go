package postgres

import (
	"context"
	"errors"
	"fmt"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/jackc/pgx/v5/pgxpool"

	"rag-reasoning-platform/backend/internal/domain/document"
)

// ProcessingJobRepository 使用 PostgreSQL 保存解析任务。
type ProcessingJobRepository struct {
	pool *pgxpool.Pool
}

var _ document.ProcessingJobCreator = (*ProcessingJobRepository)(nil)
var _ document.ProcessingJobFinder = (*ProcessingJobRepository)(nil)
var _ document.ProcessingJobClaimer = (*ProcessingJobRepository)(nil)

// NewProcessingJobRepository 创建 PostgreSQL 解析任务仓储。
func NewProcessingJobRepository(
	pool *pgxpool.Pool,
) *ProcessingJobRepository {
	return &ProcessingJobRepository{
		pool: pool,
	}
}

// CreateProcessingJob 为文档创建 queued 状态的解析任务。
func (r *ProcessingJobRepository) CreateProcessingJob(
	ctx context.Context,
	documentID int64,
) (document.ProcessingJob, error) {
	const query = `
		INSERT INTO document_jobs (document_id)
		VALUES ($1)
		RETURNING
			id,
			document_id,
			status,
			attempt_count,
			error_message,
			created_at,
			updated_at,
			started_at,
			completed_at
	`

	row := r.pool.QueryRow(ctx, query, documentID)
	createdJob, err := scanProcessingJob(row)
	if isConstraintViolation(err, "uq_document_jobs_active") {
		return document.ProcessingJob{},
			document.ErrActiveProcessingJobExists
	}
	if isForeignKeyViolation(err) {
		// 文档可能在应用层查询后、任务写入前被并发删除。
		return document.ProcessingJob{}, document.ErrNotFound
	}
	if err != nil {
		return document.ProcessingJob{}, fmt.Errorf(
			"create document processing job: %w",
			err,
		)
	}

	return createdJob, nil
}

// GetProcessingJobByID 根据主键查询解析任务。
func (r *ProcessingJobRepository) GetProcessingJobByID(
	ctx context.Context,
	jobID int64,
) (document.ProcessingJob, error) {
	const query = `
		SELECT
			id,
			document_id,
			status,
			attempt_count,
			error_message,
			created_at,
			updated_at,
			started_at,
			completed_at
		FROM document_jobs
		WHERE id = $1
	`

	row := r.pool.QueryRow(ctx, query, jobID)
	foundJob, err := scanProcessingJob(row)
	if errors.Is(err, pgx.ErrNoRows) {
		return document.ProcessingJob{},
			document.ErrProcessingJobNotFound
	}
	if err != nil {
		return document.ProcessingJob{}, fmt.Errorf(
			"get document processing job by ID: %w",
			err,
		)
	}

	return foundJob, nil
}

// ClaimNextProcessingJob 原子领取创建时间最早的 queued 任务。
//
// FOR UPDATE SKIP LOCKED 会跳过已经被其他 Worker 锁定的任务，
// 防止多个 Worker 同时领取同一条记录。任务和文档的状态更新位于
// 同一事务中，外部只能同时看到更新前或更新后的状态。
func (r *ProcessingJobRepository) ClaimNextProcessingJob(
	ctx context.Context,
) (document.ProcessingJob, error) {
	transaction, err := r.pool.Begin(ctx)
	if err != nil {
		return document.ProcessingJob{}, fmt.Errorf(
			"begin claim processing job transaction: %w",
			err,
		)
	}
	defer func() {
		_ = transaction.Rollback(context.Background())
	}()

	const selectQuery = `
		SELECT
			j.id,
			j.document_id,
			j.status,
			j.attempt_count,
			j.error_message,
			j.created_at,
			j.updated_at,
			j.started_at,
			j.completed_at
		FROM document_jobs AS j
		INNER JOIN documents AS d ON d.id = j.document_id
		WHERE j.status = 'queued'
			AND d.status IN ('uploaded', 'failed')
		ORDER BY j.created_at ASC, j.id ASC
		FOR UPDATE OF j, d SKIP LOCKED
		LIMIT 1
	`

	queuedJob, err := scanProcessingJob(
		transaction.QueryRow(ctx, selectQuery),
	)
	if errors.Is(err, pgx.ErrNoRows) {
		return document.ProcessingJob{},
			document.ErrNoQueuedProcessingJob
	}
	if err != nil {
		return document.ProcessingJob{}, fmt.Errorf(
			"select next queued processing job: %w",
			err,
		)
	}

	const updateJobQuery = `
		UPDATE document_jobs
		SET
			status = 'processing',
			attempt_count = attempt_count + 1,
			error_message = NULL,
			updated_at = CURRENT_TIMESTAMP,
			started_at = CURRENT_TIMESTAMP,
			completed_at = NULL
		WHERE id = $1
			AND status = 'queued'
		RETURNING
			id,
			document_id,
			status,
			attempt_count,
			error_message,
			created_at,
			updated_at,
			started_at,
			completed_at
	`

	claimedJob, err := scanProcessingJob(
		transaction.QueryRow(ctx, updateJobQuery, queuedJob.ID),
	)
	if err != nil {
		return document.ProcessingJob{}, fmt.Errorf(
			"mark processing job as processing: %w",
			err,
		)
	}

	const updateDocumentQuery = `
		UPDATE documents
		SET
			status = 'processing',
			error_message = NULL,
			updated_at = CURRENT_TIMESTAMP
		WHERE id = $1
			AND status IN ('uploaded', 'failed')
	`

	commandTag, err := transaction.Exec(
		ctx,
		updateDocumentQuery,
		claimedJob.DocumentID,
	)
	if err != nil {
		return document.ProcessingJob{}, fmt.Errorf(
			"mark claimed job document as processing: %w",
			err,
		)
	}
	if commandTag.RowsAffected() != 1 {
		return document.ProcessingJob{}, fmt.Errorf(
			"mark claimed job document as processing: expected 1 updated row, got %d",
			commandTag.RowsAffected(),
		)
	}

	if err := transaction.Commit(ctx); err != nil {
		return document.ProcessingJob{}, fmt.Errorf(
			"commit claimed processing job: %w",
			err,
		)
	}

	return claimedJob, nil
}

func scanProcessingJob(
	row pgx.Row,
) (document.ProcessingJob, error) {
	var job document.ProcessingJob
	var status string

	err := row.Scan(
		&job.ID,
		&job.DocumentID,
		&status,
		&job.AttemptCount,
		&job.ErrorMessage,
		&job.CreatedAt,
		&job.UpdatedAt,
		&job.StartedAt,
		&job.CompletedAt,
	)
	if err != nil {
		return document.ProcessingJob{}, err
	}

	job.Status = document.ProcessingJobStatus(status)
	if !job.Status.IsValid() {
		return document.ProcessingJob{}, fmt.Errorf(
			"invalid processing job status %q",
			status,
		)
	}

	return job, nil
}

func isConstraintViolation(err error, constraintName string) bool {
	var postgresError *pgconn.PgError
	return errors.As(err, &postgresError) &&
		postgresError.Code == "23505" &&
		postgresError.ConstraintName == constraintName
}

func isForeignKeyViolation(err error) bool {
	var postgresError *pgconn.PgError
	return errors.As(err, &postgresError) &&
		postgresError.Code == "23503"
}
