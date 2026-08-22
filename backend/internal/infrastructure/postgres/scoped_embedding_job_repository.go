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
	pool            *pgxpool.Pool
	admissionLimits embeddingdomain.JobAdmissionLimits
}

// embeddingJobAdmissionAdvisoryLockID 是所有向量任务创建者共享的事务锁编号。
// 它只在“统计活动任务并创建新任务”的短事务内持有，不覆盖 Worker 的远程调用。
const embeddingJobAdmissionAdvisoryLockID int64 = 0x524147454D424544

var _ embeddingdomain.ScopedJobRequester = (*ScopedEmbeddingJobRepository)(nil)
var _ embeddingdomain.ScopedJobFinder = (*ScopedEmbeddingJobRepository)(nil)
var _ embeddingdomain.ScopedLatestJobFinder = (*ScopedEmbeddingJobRepository)(nil)
var _ embeddingdomain.ScopedJobCanceler = (*ScopedEmbeddingJobRepository)(nil)

// NewScopedEmbeddingJobRepository 创建带文档所有者边界的向量任务仓储。
func NewScopedEmbeddingJobRepository(
	pool *pgxpool.Pool,
	admissionLimits embeddingdomain.JobAdmissionLimits,
) *ScopedEmbeddingJobRepository {
	return &ScopedEmbeddingJobRepository{
		pool:            pool,
		admissionLimits: admissionLimits,
	}
}

// RequestEmbeddingJob 原子保存当前用户的向量化意图。
//
// 事务先锁定 document 行，再根据最新状态决定初始任务状态：ready 对应 queued，
// 其余合法状态对应 waiting_document。解析完成事务也锁定同一 document 行，
// 因而并发发生时后执行的一方一定能观察到前一方已经提交的状态。
func (r *ScopedEmbeddingJobRepository) RequestEmbeddingJob(
	ctx context.Context,
	scope accessdomain.OwnerScope,
	documentID int64,
	modelName string,
	dimensions int,
) (embeddingdomain.JobRequestResult, error) {
	if !scope.IsValid() {
		return embeddingdomain.JobRequestResult{}, accessdomain.ErrInvalidOwnerScope
	}
	if !r.admissionLimits.IsValid() {
		return embeddingdomain.JobRequestResult{}, embeddingdomain.ErrInvalidJobAdmissionLimits
	}

	transaction, err := r.pool.Begin(ctx)
	if err != nil {
		return embeddingdomain.JobRequestResult{}, fmt.Errorf(
			"begin scoped embedding request transaction: %w",
			err,
		)
	}
	defer func() {
		_ = transaction.Rollback(context.Background())
	}()

	const lockDocumentQuery = `
		SELECT status
		FROM documents
		WHERE id = $1
		  AND owner_user_id = $2
		FOR UPDATE
	`

	var documentStatus documentdomain.Status
	err = transaction.QueryRow(
		ctx,
		lockDocumentQuery,
		documentID,
		scope.OwnerUserID(),
	).Scan(&documentStatus)
	if errors.Is(err, pgx.ErrNoRows) {
		return embeddingdomain.JobRequestResult{}, documentdomain.ErrNotFound
	}
	if err != nil {
		return embeddingdomain.JobRequestResult{}, fmt.Errorf(
			"lock document before requesting embedding job: %w",
			err,
		)
	}
	if !documentStatus.IsValid() {
		return embeddingdomain.JobRequestResult{}, fmt.Errorf(
			"request embedding job for invalid document status %q",
			documentStatus,
		)
	}

	// 同一个 document 行锁让并发申请按顺序执行。先返回已经存在的活动任务，
	// 可以让重复点击成为幂等查询，而不是向 Handler 抛出唯一约束冲突。
	findActiveJob := func() (embeddingdomain.Job, error) {
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
			WHERE document_id = $1
			  AND status IN ('waiting_document', 'queued', 'processing')
			ORDER BY id DESC
			LIMIT 1
		`
		return scanEmbeddingJob(transaction.QueryRow(ctx, query, documentID))
	}

	existingJob, err := findActiveJob()
	if err == nil {
		if err := transaction.Commit(ctx); err != nil {
			return embeddingdomain.JobRequestResult{}, fmt.Errorf(
				"commit existing scoped embedding request: %w",
				err,
			)
		}
		return embeddingdomain.JobRequestResult{
			Job:     existingJob,
			Created: false,
		}, nil
	}
	if !errors.Is(err, pgx.ErrNoRows) {
		return embeddingdomain.JobRequestResult{}, fmt.Errorf(
			"find active scoped embedding job: %w",
			err,
		)
	}

	// 不同文档拥有不同的行锁，单靠 document FOR UPDATE 无法阻止两个请求
	// 同时通过 COUNT。事务级 advisory lock 把“容量检查 + INSERT”变成一个
	// 跨文档、跨后端实例的短临界区，事务结束时由 PostgreSQL 自动释放。
	const admissionLockQuery = `SELECT pg_advisory_xact_lock($1)`
	if _, err := transaction.Exec(
		ctx,
		admissionLockQuery,
		embeddingJobAdmissionAdvisoryLockID,
	); err != nil {
		return embeddingdomain.JobRequestResult{}, fmt.Errorf(
			"lock embedding job admission capacity: %w",
			err,
		)
	}

	const countActiveJobsQuery = `
		SELECT
			COUNT(*) FILTER (WHERE source_document.owner_user_id = $1),
			COUNT(*)
		FROM embedding_jobs AS job
		JOIN documents AS source_document
		  ON source_document.id = job.document_id
		WHERE job.status IN ('waiting_document', 'queued', 'processing')
	`
	var ownerActiveJobCount int64
	var globalActiveJobCount int64
	if err := transaction.QueryRow(
		ctx,
		countActiveJobsQuery,
		scope.OwnerUserID(),
	).Scan(&ownerActiveJobCount, &globalActiveJobCount); err != nil {
		return embeddingdomain.JobRequestResult{}, fmt.Errorf(
			"count active embedding jobs before admission: %w",
			err,
		)
	}
	if ownerActiveJobCount >= int64(r.admissionLimits.MaxActiveJobsPerOwner) {
		return embeddingdomain.JobRequestResult{}, embeddingdomain.ErrOwnerActiveJobLimitExceeded
	}
	if globalActiveJobCount >= int64(r.admissionLimits.MaxActiveJobsGlobal) {
		return embeddingdomain.JobRequestResult{}, embeddingdomain.ErrGlobalActiveJobLimitExceeded
	}

	initialStatus := embeddingdomain.JobStatusWaitingDocument
	if documentStatus == documentdomain.StatusReady {
		initialStatus = embeddingdomain.JobStatusQueued
	}

	const insertJobQuery = `
		INSERT INTO embedding_jobs (
			document_id,
			model_name,
			dimensions,
			status
		)
		VALUES ($1, $2, $3, $4)
		ON CONFLICT DO NOTHING
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
			initialStatus,
		),
	)
	if errors.Is(err, pgx.ErrNoRows) {
		// 唯一索引仍是最终并发保护。若有其他系统级写入者没有遵循
		// document 行锁，冲突后也返回它创建的活动任务。
		existingJob, err = findActiveJob()
		if err != nil {
			return embeddingdomain.JobRequestResult{}, fmt.Errorf(
				"find concurrently created embedding job: %w",
				err,
			)
		}
		if err := transaction.Commit(ctx); err != nil {
			return embeddingdomain.JobRequestResult{}, fmt.Errorf(
				"commit concurrent scoped embedding request: %w",
				err,
			)
		}
		return embeddingdomain.JobRequestResult{
			Job:     existingJob,
			Created: false,
		}, nil
	}
	if isForeignKeyViolation(err) {
		return embeddingdomain.JobRequestResult{}, documentdomain.ErrNotFound
	}
	if err != nil {
		return embeddingdomain.JobRequestResult{}, fmt.Errorf(
			"request scoped embedding job: %w",
			err,
		)
	}
	if err := transaction.Commit(ctx); err != nil {
		return embeddingdomain.JobRequestResult{}, fmt.Errorf(
			"commit scoped embedding request: %w",
			err,
		)
	}
	return embeddingdomain.JobRequestResult{
		Job:     createdJob,
		Created: true,
	}, nil
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

// FindLatestEmbeddingJobsByDocumentIDs 一次查询当前所有者每份文档最新的任务。
// DISTINCT ON 按 document_id 分组，ORDER BY id DESC 选择该组最新创建的任务。
// 没有任务、不存在和属于其他用户的文档都不会出现在结果中。
func (r *ScopedEmbeddingJobRepository) FindLatestEmbeddingJobsByDocumentIDs(
	ctx context.Context,
	scope accessdomain.OwnerScope,
	documentIDs []int64,
) ([]embeddingdomain.Job, error) {
	if !scope.IsValid() {
		return nil, accessdomain.ErrInvalidOwnerScope
	}
	if len(documentIDs) == 0 {
		return make([]embeddingdomain.Job, 0), nil
	}

	const query = `
		SELECT DISTINCT ON (job.document_id)
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
		WHERE source_document.owner_user_id = $1
		  AND job.document_id = ANY($2::BIGINT[])
		ORDER BY job.document_id, job.id DESC
	`

	rows, err := r.pool.Query(ctx, query, scope.OwnerUserID(), documentIDs)
	if err != nil {
		return nil, fmt.Errorf("query latest scoped embedding jobs: %w", err)
	}
	defer rows.Close()

	jobs := make([]embeddingdomain.Job, 0, len(documentIDs))
	for rows.Next() {
		job, err := scanEmbeddingJob(rows)
		if err != nil {
			return nil, fmt.Errorf("scan latest scoped embedding job: %w", err)
		}
		jobs = append(jobs, job)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("iterate latest scoped embedding jobs: %w", err)
	}

	return jobs, nil
}

// CancelEmbeddingJob 在所有者边界内原子取消 waiting_document 或 queued 任务。
//
// 事务先锁 document，再锁 embedding_job，与申请任务时的加锁顺序一致。
// 这样 Worker 领取任务、重复申请和取消并发发生时，最终只会提交一个合法状态。
func (r *ScopedEmbeddingJobRepository) CancelEmbeddingJob(
	ctx context.Context,
	scope accessdomain.OwnerScope,
	jobID int64,
) (embeddingdomain.Job, error) {
	if !scope.IsValid() {
		return embeddingdomain.Job{}, accessdomain.ErrInvalidOwnerScope
	}

	transaction, err := r.pool.Begin(ctx)
	if err != nil {
		return embeddingdomain.Job{}, fmt.Errorf("begin scoped embedding cancellation: %w", err)
	}
	defer func() {
		_ = transaction.Rollback(context.Background())
	}()

	const findDocumentQuery = `
		SELECT job.document_id
		FROM embedding_jobs AS job
		JOIN documents AS source_document
		  ON source_document.id = job.document_id
		WHERE job.id = $1
		  AND source_document.owner_user_id = $2
	`
	var documentID int64
	err = transaction.QueryRow(
		ctx,
		findDocumentQuery,
		jobID,
		scope.OwnerUserID(),
	).Scan(&documentID)
	if errors.Is(err, pgx.ErrNoRows) {
		return embeddingdomain.Job{}, embeddingdomain.ErrJobNotFound
	}
	if err != nil {
		return embeddingdomain.Job{}, fmt.Errorf("find scoped embedding job before cancellation: %w", err)
	}

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
		return embeddingdomain.Job{}, embeddingdomain.ErrJobNotFound
	}
	if err != nil {
		return embeddingdomain.Job{}, fmt.Errorf("lock document before embedding cancellation: %w", err)
	}

	const lockJobQuery = `
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
		  AND document_id = $2
		FOR UPDATE
	`
	lockedJob, err := scanEmbeddingJob(
		transaction.QueryRow(ctx, lockJobQuery, jobID, lockedDocumentID),
	)
	if errors.Is(err, pgx.ErrNoRows) {
		return embeddingdomain.Job{}, embeddingdomain.ErrJobNotFound
	}
	if err != nil {
		return embeddingdomain.Job{}, fmt.Errorf("lock embedding job before cancellation: %w", err)
	}

	switch lockedJob.Status {
	case embeddingdomain.JobStatusCanceled:
		if err := transaction.Commit(ctx); err != nil {
			return embeddingdomain.Job{}, fmt.Errorf("commit repeated embedding cancellation: %w", err)
		}
		return lockedJob, nil
	case embeddingdomain.JobStatusProcessing:
		return embeddingdomain.Job{}, embeddingdomain.ErrJobProcessingCannotCancel
	case embeddingdomain.JobStatusSucceeded, embeddingdomain.JobStatusFailed:
		return embeddingdomain.Job{}, embeddingdomain.ErrJobTerminalCannotCancel
	case embeddingdomain.JobStatusWaitingDocument, embeddingdomain.JobStatusQueued:
		// 继续执行下面的状态更新。
	default:
		return embeddingdomain.Job{}, fmt.Errorf("cancel embedding job with invalid status %q", lockedJob.Status)
	}

	const cancelJobQuery = `
		UPDATE embedding_jobs
		SET
			status = 'canceled',
			error_message = NULL,
			updated_at = CURRENT_TIMESTAMP,
			started_at = NULL,
			completed_at = CURRENT_TIMESTAMP,
			prompt_tokens = NULL,
			total_tokens = NULL
		WHERE id = $1
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
	canceledJob, err := scanEmbeddingJob(
		transaction.QueryRow(ctx, cancelJobQuery, jobID),
	)
	if err != nil {
		return embeddingdomain.Job{}, fmt.Errorf("cancel scoped embedding job: %w", err)
	}
	if err := transaction.Commit(ctx); err != nil {
		return embeddingdomain.Job{}, fmt.Errorf("commit scoped embedding cancellation: %w", err)
	}
	return canceledJob, nil
}
