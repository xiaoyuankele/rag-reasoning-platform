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
	pool            *pgxpool.Pool
	admissionLimits documentdomain.ProcessingJobAdmissionLimits
}

// processingJobAdmissionAdvisoryLockID 是所有解析任务创建者共享的事务锁编号。
// 它只保护“统计活动任务并创建新任务”的短临界区，不覆盖 Worker 的实际解析。
const processingJobAdmissionAdvisoryLockID int64 = 0x52414750524F434A

var _ documentdomain.ScopedProcessingJobCreator = (*ScopedProcessingJobRepository)(nil)
var _ documentdomain.ScopedProcessingJobFinder = (*ScopedProcessingJobRepository)(nil)
var _ documentdomain.ScopedLatestProcessingJobFinder = (*ScopedProcessingJobRepository)(nil)
var _ documentdomain.ScopedProcessingJobCanceler = (*ScopedProcessingJobRepository)(nil)

// NewScopedProcessingJobRepository 创建带文档所有者边界的解析任务仓储。
func NewScopedProcessingJobRepository(
	pool *pgxpool.Pool,
	admissionLimits documentdomain.ProcessingJobAdmissionLimits,
) *ScopedProcessingJobRepository {
	return &ScopedProcessingJobRepository{
		pool:            pool,
		admissionLimits: admissionLimits,
	}
}

// CreateProcessingJob 只在文档属于当前 OwnerScope 时创建 queued 任务。
// 同文档重复检查、单用户/全局容量检查和 INSERT 在同一事务内完成。
func (r *ScopedProcessingJobRepository) CreateProcessingJob(
	ctx context.Context,
	scope accessdomain.OwnerScope,
	documentID int64,
) (documentdomain.ProcessingJob, error) {
	if !scope.IsValid() {
		return documentdomain.ProcessingJob{}, accessdomain.ErrInvalidOwnerScope
	}
	if !r.admissionLimits.IsValid() {
		return documentdomain.ProcessingJob{},
			documentdomain.ErrInvalidProcessingJobAdmissionLimits
	}

	transaction, err := r.pool.Begin(ctx)
	if err != nil {
		return documentdomain.ProcessingJob{}, fmt.Errorf(
			"begin scoped processing job transaction: %w",
			err,
		)
	}
	defer func() {
		_ = transaction.Rollback(context.Background())
	}()

	// 同一文档的并发请求先在 documents 行上排队。锁定后再查活动任务，
	// 能确保重复点击始终得到“任务已存在”，不会被容量已满掩盖。
	const lockDocumentQuery = `
		SELECT id
		FROM documents
		WHERE id = $1
		  AND owner_user_id = $2
		FOR UPDATE
	`
	var lockedDocumentID int64
	err = transaction.QueryRow(
		ctx,
		lockDocumentQuery,
		documentID,
		scope.OwnerUserID(),
	).Scan(&lockedDocumentID)
	if errors.Is(err, pgx.ErrNoRows) {
		return documentdomain.ProcessingJob{}, documentdomain.ErrNotFound
	}
	if err != nil {
		return documentdomain.ProcessingJob{}, fmt.Errorf(
			"lock document before creating processing job: %w",
			err,
		)
	}

	const activeJobQuery = `
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
		WHERE document_id = $1
		  AND status IN ('queued', 'processing')
		ORDER BY id DESC
		LIMIT 1
	`
	_, err = scanProcessingJob(
		transaction.QueryRow(ctx, activeJobQuery, lockedDocumentID),
	)
	if err == nil {
		return documentdomain.ProcessingJob{},
			documentdomain.ErrActiveProcessingJobExists
	}
	if !errors.Is(err, pgx.ErrNoRows) {
		return documentdomain.ProcessingJob{}, fmt.Errorf(
			"find active scoped processing job: %w",
			err,
		)
	}

	// 不同文档有不同的行锁，因此还需要一个事务级 advisory lock 将
	// “跨文档容量统计 + INSERT”串成一个原子临界区。事务结束后锁自动释放。
	const admissionLockQuery = `SELECT pg_advisory_xact_lock($1)`
	if _, err := transaction.Exec(
		ctx,
		admissionLockQuery,
		processingJobAdmissionAdvisoryLockID,
	); err != nil {
		return documentdomain.ProcessingJob{}, fmt.Errorf(
			"lock processing job admission capacity: %w",
			err,
		)
	}

	const countActiveJobsQuery = `
		SELECT
			COUNT(*) FILTER (WHERE source_document.owner_user_id = $1),
			COUNT(*)
		FROM document_jobs AS job
		JOIN documents AS source_document
		  ON source_document.id = job.document_id
		WHERE job.status IN ('queued', 'processing')
	`
	var ownerActiveJobCount int64
	var globalActiveJobCount int64
	if err := transaction.QueryRow(
		ctx,
		countActiveJobsQuery,
		scope.OwnerUserID(),
	).Scan(&ownerActiveJobCount, &globalActiveJobCount); err != nil {
		return documentdomain.ProcessingJob{}, fmt.Errorf(
			"count active processing jobs before admission: %w",
			err,
		)
	}
	if ownerActiveJobCount >= int64(r.admissionLimits.MaxActiveJobsPerOwner) {
		return documentdomain.ProcessingJob{},
			documentdomain.ErrOwnerActiveProcessingJobLimitExceeded
	}
	if globalActiveJobCount >= int64(r.admissionLimits.MaxActiveJobsGlobal) {
		return documentdomain.ProcessingJob{},
			documentdomain.ErrGlobalProcessingJobLimitExceeded
	}
	if err := ensureProcessingOwnerSchedule(
		ctx,
		transaction,
		scope.OwnerUserID(),
	); err != nil {
		return documentdomain.ProcessingJob{}, err
	}

	const insertJobQuery = `
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
	createdJob, err := scanProcessingJob(
		transaction.QueryRow(ctx, insertJobQuery, lockedDocumentID),
	)
	if isConstraintViolation(err, "uq_document_jobs_active") {
		return documentdomain.ProcessingJob{},
			documentdomain.ErrActiveProcessingJobExists
	}
	if isForeignKeyViolation(err) {
		return documentdomain.ProcessingJob{}, documentdomain.ErrNotFound
	}
	if err != nil {
		return documentdomain.ProcessingJob{}, fmt.Errorf(
			"create scoped document processing job: %w",
			err,
		)
	}
	if err := transaction.Commit(ctx); err != nil {
		return documentdomain.ProcessingJob{}, fmt.Errorf(
			"commit scoped document processing job: %w",
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

// FindLatestProcessingJobsByDocumentIDs 一次查询当前所有者每份文档最新的解析任务。
// DISTINCT ON 按 document_id 分组，ORDER BY id DESC 选择该组最新创建的任务。
// 没有任务、不存在和属于其他用户的文档都不会出现在结果中。
func (r *ScopedProcessingJobRepository) FindLatestProcessingJobsByDocumentIDs(
	ctx context.Context,
	scope accessdomain.OwnerScope,
	documentIDs []int64,
) ([]documentdomain.ProcessingJob, error) {
	if !scope.IsValid() {
		return nil, accessdomain.ErrInvalidOwnerScope
	}
	if len(documentIDs) == 0 {
		return make([]documentdomain.ProcessingJob, 0), nil
	}

	const query = `
		SELECT DISTINCT ON (job.document_id)
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
		WHERE source_document.owner_user_id = $1
		  AND job.document_id = ANY($2::BIGINT[])
		ORDER BY job.document_id, job.id DESC
	`

	rows, err := r.pool.Query(ctx, query, scope.OwnerUserID(), documentIDs)
	if err != nil {
		return nil, fmt.Errorf("query latest scoped processing jobs: %w", err)
	}
	defer rows.Close()

	jobs := make([]documentdomain.ProcessingJob, 0, len(documentIDs))
	for rows.Next() {
		job, err := scanProcessingJob(rows)
		if err != nil {
			return nil, fmt.Errorf("scan latest scoped processing job: %w", err)
		}
		jobs = append(jobs, job)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("iterate latest scoped processing jobs: %w", err)
	}

	return jobs, nil
}

// CancelProcessingJob 在所有者边界内原子取消 queued 解析任务。
//
// SELECT ... FOR UPDATE OF job 只锁定目标任务行。Worker 领取使用
// FOR UPDATE ... SKIP LOCKED：取消先获得锁时 Worker 会跳过该任务；Worker
// 先领取时取消会在锁释放后观察到 processing，并返回稳定冲突错误。
func (r *ScopedProcessingJobRepository) CancelProcessingJob(
	ctx context.Context,
	scope accessdomain.OwnerScope,
	jobID int64,
) (documentdomain.ProcessingJob, error) {
	if !scope.IsValid() {
		return documentdomain.ProcessingJob{}, accessdomain.ErrInvalidOwnerScope
	}

	transaction, err := r.pool.Begin(ctx)
	if err != nil {
		return documentdomain.ProcessingJob{}, fmt.Errorf(
			"begin scoped processing cancellation: %w",
			err,
		)
	}
	defer func() {
		_ = transaction.Rollback(context.Background())
	}()

	const lockJobQuery = `
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
		FOR UPDATE OF job
	`

	lockedJob, err := scanProcessingJob(
		transaction.QueryRow(
			ctx,
			lockJobQuery,
			jobID,
			scope.OwnerUserID(),
		),
	)
	if errors.Is(err, pgx.ErrNoRows) {
		return documentdomain.ProcessingJob{}, documentdomain.ErrProcessingJobNotFound
	}
	if err != nil {
		return documentdomain.ProcessingJob{}, fmt.Errorf(
			"lock scoped processing job before cancellation: %w",
			err,
		)
	}

	switch lockedJob.Status {
	case documentdomain.ProcessingJobStatusCanceled:
		if err := transaction.Commit(ctx); err != nil {
			return documentdomain.ProcessingJob{}, fmt.Errorf(
				"commit repeated processing cancellation: %w",
				err,
			)
		}
		return lockedJob, nil
	case documentdomain.ProcessingJobStatusProcessing:
		return documentdomain.ProcessingJob{},
			documentdomain.ErrProcessingJobProcessingCannotCancel
	case documentdomain.ProcessingJobStatusSucceeded,
		documentdomain.ProcessingJobStatusFailed:
		return documentdomain.ProcessingJob{},
			documentdomain.ErrProcessingJobTerminalCannotCancel
	case documentdomain.ProcessingJobStatusQueued:
		// 继续执行下面的状态更新。
	default:
		return documentdomain.ProcessingJob{}, fmt.Errorf(
			"cancel processing job with invalid status %q",
			lockedJob.Status,
		)
	}

	const cancelJobQuery = `
		UPDATE document_jobs
		SET
			status = 'canceled',
			error_message = NULL,
			error_code = NULL,
			queue_wait_ms = NULL,
			processor_ms = NULL,
			total_ms = NULL,
			file_bytes = NULL,
			chunk_count = NULL,
			updated_at = CURRENT_TIMESTAMP,
			started_at = NULL,
			completed_at = CURRENT_TIMESTAMP
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
	canceledJob, err := scanProcessingJob(
		transaction.QueryRow(ctx, cancelJobQuery, jobID),
	)
	if err != nil {
		return documentdomain.ProcessingJob{}, fmt.Errorf(
			"cancel scoped processing job: %w",
			err,
		)
	}
	if err := transaction.Commit(ctx); err != nil {
		return documentdomain.ProcessingJob{}, fmt.Errorf(
			"commit scoped processing cancellation: %w",
			err,
		)
	}
	return canceledJob, nil
}
