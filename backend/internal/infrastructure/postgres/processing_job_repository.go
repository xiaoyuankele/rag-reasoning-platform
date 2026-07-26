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
