package postgres

import (
	"context"
	"errors"
	"fmt"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	accessdomain "rag-reasoning-platform/backend/internal/domain/access"
	documentdomain "rag-reasoning-platform/backend/internal/domain/document"
)

// ScopedProcessingJobRepository 为已认证用户创建和查询解析任务。
// Worker 仍使用 ProcessingJobRepository 的系统级领取与收尾能力。
type ScopedProcessingJobRepository struct {
	pool *pgxpool.Pool
}

var _ documentdomain.ScopedProcessingJobCreator = (*ScopedProcessingJobRepository)(nil)
var _ documentdomain.ScopedProcessingJobFinder = (*ScopedProcessingJobRepository)(nil)

// NewScopedProcessingJobRepository 创建带文档所有者边界的解析任务仓储。
func NewScopedProcessingJobRepository(
	pool *pgxpool.Pool,
) *ScopedProcessingJobRepository {
	return &ScopedProcessingJobRepository{pool: pool}
}

// CreateProcessingJob 只在文档属于当前 OwnerScope 时创建 queued 任务。
// INSERT ... SELECT 把归属检查与任务写入合并成一个数据库语句。
func (r *ScopedProcessingJobRepository) CreateProcessingJob(
	ctx context.Context,
	scope accessdomain.OwnerScope,
	documentID int64,
) (documentdomain.ProcessingJob, error) {
	if !scope.IsValid() {
		return documentdomain.ProcessingJob{}, accessdomain.ErrInvalidOwnerScope
	}

	const query = `
		INSERT INTO document_jobs (document_id)
		SELECT id
		FROM documents
		WHERE id = $1
		  AND owner_user_id = $2
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

	createdJob, err := scanProcessingJob(
		r.pool.QueryRow(ctx, query, documentID, scope.OwnerUserID()),
	)
	if errors.Is(err, pgx.ErrNoRows) || isForeignKeyViolation(err) {
		return documentdomain.ProcessingJob{}, documentdomain.ErrNotFound
	}
	if isConstraintViolation(err, "uq_document_jobs_active") {
		return documentdomain.ProcessingJob{}, documentdomain.ErrActiveProcessingJobExists
	}
	if err != nil {
		return documentdomain.ProcessingJob{}, fmt.Errorf(
			"create scoped document processing job: %w",
			err,
		)
	}
	return createdJob, nil
}

// GetProcessingJobByID 通过任务与文档 JOIN 强制校验任务所属用户。
func (r *ScopedProcessingJobRepository) GetProcessingJobByID(
	ctx context.Context,
	scope accessdomain.OwnerScope,
	jobID int64,
) (documentdomain.ProcessingJob, error) {
	if !scope.IsValid() {
		return documentdomain.ProcessingJob{}, accessdomain.ErrInvalidOwnerScope
	}

	const query = `
		SELECT
			job.id,
			job.document_id,
			job.status,
			job.attempt_count,
			job.error_message,
			job.created_at,
			job.updated_at,
			job.started_at,
			job.completed_at
		FROM document_jobs AS job
		JOIN documents AS source_document
		  ON source_document.id = job.document_id
		WHERE job.id = $1
		  AND source_document.owner_user_id = $2
	`

	foundJob, err := scanProcessingJob(
		r.pool.QueryRow(ctx, query, jobID, scope.OwnerUserID()),
	)
	if errors.Is(err, pgx.ErrNoRows) {
		return documentdomain.ProcessingJob{}, documentdomain.ErrProcessingJobNotFound
	}
	if err != nil {
		return documentdomain.ProcessingJob{}, fmt.Errorf(
			"get scoped processing job by ID: %w",
			err,
		)
	}
	return foundJob, nil
}
