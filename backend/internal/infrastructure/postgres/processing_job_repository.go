package postgres

import (
	"context"
	"errors"
	"fmt"
	"time"

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
var _ document.ProcessingJobFinalizer = (*ProcessingJobRepository)(nil)

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
			error_code = NULL,
			queue_wait_ms = NULL,
			processor_ms = NULL,
			total_ms = NULL,
			file_bytes = NULL,
			chunk_count = NULL,
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

// MarkProcessingJobSucceeded 原子地把任务标记为 succeeded，
// 并把关联文档标记为 ready。
func (r *ProcessingJobRepository) MarkProcessingJobSucceeded(
	ctx context.Context,
	jobID int64,
	completion document.ProcessingCompletion,
) error {
	return r.finalizeProcessingJob(
		ctx,
		jobID,
		document.ProcessingJobStatusSucceeded,
		document.StatusReady,
		nil,
		completion.DetectedTitle,
		completion.Metrics,
	)
}

// MarkProcessingJobFailed 原子地把任务和关联文档标记为 failed，
// 并保存可以安全展示的失败说明。
func (r *ProcessingJobRepository) MarkProcessingJobFailed(
	ctx context.Context,
	jobID int64,
	failure document.ProcessingFailure,
) error {
	return r.finalizeProcessingJob(
		ctx,
		jobID,
		document.ProcessingJobStatusFailed,
		document.StatusFailed,
		&failure.Message,
		nil,
		failure.Metrics,
	)
}

// MarkInterruptedProcessingJobsFailed 在应用启动时，把上一次进程异常退出
// 遗留的 processing 任务及其文档原子地标记为 failed。
//
// 当前实现建立在单 Worker 实例约束上：新进程启动时，不应存在另一个仍
// 合法处理任务的实例。未来扩展为多实例时，需要使用 lease/heartbeat
// 判断任务是否真正失联，不能继续恢复全部 processing 任务。
func (r *ProcessingJobRepository) MarkInterruptedProcessingJobsFailed(
	ctx context.Context,
	errorMessage string,
) (int64, error) {
	transaction, err := r.pool.Begin(ctx)
	if err != nil {
		return 0, fmt.Errorf(
			"begin interrupted processing job recovery transaction: %w",
			err,
		)
	}
	defer func() {
		_ = transaction.Rollback(context.Background())
	}()

	// interrupted_jobs 先锁定恢复范围；两个 UPDATE CTE 分别修改文档和任务。
	// 最终 SELECT 同时返回两张表的更新数量，用于验证事务内状态仍然一致。
	const recoveryQuery = `
		WITH interrupted_jobs AS (
			SELECT id, document_id
			FROM document_jobs
			WHERE status = 'processing'
			FOR UPDATE
		),
		updated_documents AS (
			UPDATE documents AS d
			SET
				status = 'failed',
				error_message = $1,
				updated_at = CURRENT_TIMESTAMP
			FROM interrupted_jobs AS interrupted
			WHERE d.id = interrupted.document_id
				AND d.status = 'processing'
			RETURNING d.id
		),
		updated_jobs AS (
			UPDATE document_jobs AS j
			SET
				status = 'failed',
				error_message = $1,
				error_code = 'worker_interrupted',
				queue_wait_ms = GREATEST(
					0,
					ROUND(EXTRACT(EPOCH FROM (j.started_at - j.created_at)) * 1000)::BIGINT
				),
				total_ms = GREATEST(
					0,
					ROUND(EXTRACT(EPOCH FROM (CURRENT_TIMESTAMP - j.started_at)) * 1000)::BIGINT
				),
				updated_at = CURRENT_TIMESTAMP,
				completed_at = CURRENT_TIMESTAMP
			FROM interrupted_jobs AS interrupted
			WHERE j.id = interrupted.id
				AND j.status = 'processing'
			RETURNING j.id
		)
		SELECT
			(SELECT COUNT(*) FROM updated_jobs),
			(SELECT COUNT(*) FROM updated_documents)
	`

	var recoveredJobCount int64
	var recoveredDocumentCount int64
	if err := transaction.QueryRow(
		ctx,
		recoveryQuery,
		errorMessage,
	).Scan(
		&recoveredJobCount,
		&recoveredDocumentCount,
	); err != nil {
		return 0, fmt.Errorf(
			"recover interrupted processing jobs: %w",
			err,
		)
	}

	if recoveredJobCount != recoveredDocumentCount {
		return 0, fmt.Errorf(
			"recover interrupted processing jobs: updated %d jobs and %d documents",
			recoveredJobCount,
			recoveredDocumentCount,
		)
	}

	if err := transaction.Commit(ctx); err != nil {
		return 0, fmt.Errorf(
			"commit interrupted processing job recovery: %w",
			err,
		)
	}

	return recoveredJobCount, nil
}

func (r *ProcessingJobRepository) finalizeProcessingJob(
	ctx context.Context,
	jobID int64,
	jobStatus document.ProcessingJobStatus,
	documentStatus document.Status,
	errorMessage *string,
	detectedTitle *string,
	metrics document.ProcessingExecutionMetrics,
) error {
	transaction, err := r.pool.Begin(ctx)
	if err != nil {
		return fmt.Errorf(
			"begin finalize processing job transaction: %w",
			err,
		)
	}
	defer func() {
		_ = transaction.Rollback(context.Background())
	}()

	// 同时锁定任务和文档，保证检查状态与后续更新之间不会被其他事务修改。
	const lockQuery = `
		SELECT j.document_id
		FROM document_jobs AS j
		INNER JOIN documents AS d ON d.id = j.document_id
		WHERE j.id = $1
			AND j.status = 'processing'
			AND d.status = 'processing'
		FOR UPDATE OF j, d
	`

	var documentID int64
	err = transaction.QueryRow(
		ctx,
		lockQuery,
		jobID,
	).Scan(&documentID)
	if errors.Is(err, pgx.ErrNoRows) {
		return document.ErrProcessingJobNotProcessing
	}
	if err != nil {
		return fmt.Errorf(
			"lock processing job for finalization: %w",
			err,
		)
	}

	const updateJobQuery = `
		UPDATE document_jobs
		SET
			status = $2,
			error_message = $3,
			queue_wait_ms = GREATEST(
				0,
				ROUND(EXTRACT(EPOCH FROM (started_at - created_at)) * 1000)::BIGINT
			),
			processor_ms = $4,
			total_ms = GREATEST(
				0,
				ROUND(EXTRACT(EPOCH FROM (CURRENT_TIMESTAMP - started_at)) * 1000)::BIGINT
			),
			file_bytes = $5,
			chunk_count = $6,
			error_code = NULLIF($7, ''),
			updated_at = CURRENT_TIMESTAMP,
			completed_at = CURRENT_TIMESTAMP
		WHERE id = $1
			AND status = 'processing'
	`

	jobCommandTag, err := transaction.Exec(
		ctx,
		updateJobQuery,
		jobID,
		jobStatus,
		errorMessage,
		durationMilliseconds(metrics.ProcessorDuration),
		metrics.FileBytes,
		metrics.ChunkCount,
		string(metrics.ErrorCode),
	)
	if err != nil {
		return fmt.Errorf(
			"update finalized processing job: %w",
			err,
		)
	}
	if jobCommandTag.RowsAffected() != 1 {
		return fmt.Errorf(
			"update finalized processing job: expected 1 updated row, got %d",
			jobCommandTag.RowsAffected(),
		)
	}

	const updateDocumentQuery = `
		UPDATE documents
		SET
			status = $2,
			error_message = $3,
			title = COALESCE(title, $4),
			updated_at = CURRENT_TIMESTAMP
		WHERE id = $1
			AND status = 'processing'
	`

	documentCommandTag, err := transaction.Exec(
		ctx,
		updateDocumentQuery,
		documentID,
		documentStatus,
		errorMessage,
		detectedTitle,
	)
	if err != nil {
		return fmt.Errorf(
			"update finalized processing job document: %w",
			err,
		)
	}
	if documentCommandTag.RowsAffected() != 1 {
		return fmt.Errorf(
			"update finalized processing job document: expected 1 updated row, got %d",
			documentCommandTag.RowsAffected(),
		)
	}

	// 文档解析成功后，在同一事务中激活用户预先保存的向量化意图。
	// 失败收尾不能激活任务，因为此时还没有可供 Worker 使用的正式 chunks。
	if documentStatus == document.StatusReady {
		const activateWaitingEmbeddingJobsQuery = `
			UPDATE embedding_jobs
			SET
				status = 'queued',
				next_attempt_at = CURRENT_TIMESTAMP,
				updated_at = CURRENT_TIMESTAMP,
				error_message = NULL
			WHERE document_id = $1
			  AND status = 'waiting_document'
		`
		if _, err := transaction.Exec(
			ctx,
			activateWaitingEmbeddingJobsQuery,
			documentID,
		); err != nil {
			return fmt.Errorf(
				"activate waiting embedding jobs: %w",
				err,
			)
		}
	}

	if err := transaction.Commit(ctx); err != nil {
		return fmt.Errorf(
			"commit finalized processing job: %w",
			err,
		)
	}

	return nil
}

// durationMilliseconds 统一把 Go 耗时转换为数据库使用的非负毫秒数。
// time.Duration.Milliseconds 会舍去不足一毫秒的部分，这是当前性能基线
// 所需的精度，也避免为短操作引入微秒级噪声。
func durationMilliseconds(duration time.Duration) int64 {
	if duration < 0 {
		return 0
	}

	return duration.Milliseconds()
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
