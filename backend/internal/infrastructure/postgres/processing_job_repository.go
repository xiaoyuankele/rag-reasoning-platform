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
	pool             *pgxpool.Pool
	schedulingPolicy document.ProcessingJobSchedulingPolicy
}

var defaultProcessingJobSchedulingPolicy = document.ProcessingJobSchedulingPolicy{
	MaxInFlightPerOwner:         1,
	MaxBorrowedInFlightPerOwner: 2,
	StarvationThreshold:         2 * time.Minute,
}

var _ document.ProcessingJobCreator = (*ProcessingJobRepository)(nil)
var _ document.ProcessingJobFinder = (*ProcessingJobRepository)(nil)
var _ document.ProcessingJobClaimer = (*ProcessingJobRepository)(nil)
var _ document.ProcessingJobFinalizer = (*ProcessingJobRepository)(nil)

// NewProcessingJobRepository 创建 PostgreSQL 解析任务仓储。
func NewProcessingJobRepository(
	pool *pgxpool.Pool,
) *ProcessingJobRepository {
	return NewProcessingJobRepositoryWithSchedulingPolicy(
		pool,
		defaultProcessingJobSchedulingPolicy,
	)
}

// NewProcessingJobRepositoryWithSchedulingPolicy 创建使用指定 Owner 公平策略的仓储。
func NewProcessingJobRepositoryWithSchedulingPolicy(
	pool *pgxpool.Pool,
	policy document.ProcessingJobSchedulingPolicy,
) *ProcessingJobRepository {
	return &ProcessingJobRepository{
		pool:             pool,
		schedulingPolicy: policy,
	}
}

// CreateProcessingJob 为文档创建 queued 状态的解析任务。
func (r *ProcessingJobRepository) CreateProcessingJob(
	ctx context.Context,
	documentID int64,
) (document.ProcessingJob, error) {
	transaction, err := r.pool.Begin(ctx)
	if err != nil {
		return document.ProcessingJob{}, fmt.Errorf(
			"begin processing job transaction: %w",
			err,
		)
	}
	defer func() {
		_ = transaction.Rollback(context.Background())
	}()

	// 系统级创建入口仍要补齐 Owner 调度行，保证测试、维护命令和未来内部
	// 调用不会绕过公平调度。owner_user_id 已由第 12 号迁移设为 NOT NULL。
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
		return document.ProcessingJob{}, document.ErrNotFound
	}
	if err != nil {
		return document.ProcessingJob{}, fmt.Errorf(
			"lock document before creating processing job: %w",
			err,
		)
	}
	if err := ensureProcessingOwnerSchedule(
		ctx,
		transaction,
		ownerUserID,
	); err != nil {
		return document.ProcessingJob{}, err
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

	row := transaction.QueryRow(ctx, insertJobQuery, documentID)
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
	if err := transaction.Commit(ctx); err != nil {
		return document.ProcessingJob{}, fmt.Errorf(
			"commit processing job: %w",
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

// ClaimNextProcessingJob 按 Owner 公平策略原子领取下一条 queued 任务。
//
// 第一遍优先选择尚未达到基础并发上限的 Owner；只有没有此类 Owner 时，
// 第二遍才允许借用空闲容量。Owner 调度行与任务都使用 FOR UPDATE
// SKIP LOCKED，防止多个 Worker 同时选择同一个 Owner 或同一条任务。
func (r *ProcessingJobRepository) ClaimNextProcessingJob(
	ctx context.Context,
) (document.ProcessingJob, error) {
	if !r.schedulingPolicy.IsValid() {
		return document.ProcessingJob{},
			document.ErrInvalidProcessingJobSchedulingPolicy
	}

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

	ownerUserID, err := selectNextProcessingOwner(
		ctx,
		transaction,
		r.schedulingPolicy.MaxInFlightPerOwner,
		r.schedulingPolicy.StarvationThreshold,
	)
	if errors.Is(err, pgx.ErrNoRows) &&
		r.schedulingPolicy.MaxBorrowedInFlightPerOwner >
			r.schedulingPolicy.MaxInFlightPerOwner {
		ownerUserID, err = selectNextProcessingOwner(
			ctx,
			transaction,
			r.schedulingPolicy.MaxBorrowedInFlightPerOwner,
			r.schedulingPolicy.StarvationThreshold,
		)
	}
	if errors.Is(err, pgx.ErrNoRows) {
		return document.ProcessingJob{}, document.ErrNoQueuedProcessingJob
	}
	if err != nil {
		return document.ProcessingJob{}, fmt.Errorf(
			"select next processing job owner: %w",
			err,
		)
	}

	const selectJobQuery = `
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
			AND d.owner_user_id = $1
		ORDER BY j.created_at ASC, j.id ASC
		FOR UPDATE OF j, d SKIP LOCKED
		LIMIT 1
	`

	queuedJob, err := scanProcessingJob(
		transaction.QueryRow(ctx, selectJobQuery, ownerUserID),
	)
	if errors.Is(err, pgx.ErrNoRows) {
		// Owner 调度行已锁定，正常 Worker 不会并发领取该 Owner；若任务在
		// 两条语句之间被删除，本轮视为空队列并由下一次轮询重新选择。
		return document.ProcessingJob{}, document.ErrNoQueuedProcessingJob
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
			chunk_write_ms = NULL,
			python_total_ms = NULL,
			source_open_ms = NULL,
			metadata_read_ms = NULL,
			text_extract_ms = NULL,
			text_split_ms = NULL,
			page_count = NULL,
			slowest_page_number = NULL,
			slowest_page_ms = NULL,
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

	const updateScheduleQuery = `
		UPDATE document_processing_owner_schedules
		SET
			last_dispatched_at = CURRENT_TIMESTAMP,
			updated_at = CURRENT_TIMESTAMP
		WHERE owner_user_id = $1
	`
	scheduleCommandTag, err := transaction.Exec(
		ctx,
		updateScheduleQuery,
		ownerUserID,
	)
	if err != nil {
		return document.ProcessingJob{}, fmt.Errorf(
			"update processing owner schedule: %w",
			err,
		)
	}
	if scheduleCommandTag.RowsAffected() != 1 {
		return document.ProcessingJob{}, fmt.Errorf(
			"update processing owner schedule: expected 1 updated row, got %d",
			scheduleCommandTag.RowsAffected(),
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

// selectNextProcessingOwner 锁定一个符合并发上限的 Owner 调度行。
// 防饥饿 Owner 优先；其余 Owner 按最近派发时间和最老 queued 时间排序。
func selectNextProcessingOwner(
	ctx context.Context,
	transaction pgx.Tx,
	maxInFlight int,
	starvationThreshold time.Duration,
) (int64, error) {
	const query = `
		SELECT schedule.owner_user_id
		FROM document_processing_owner_schedules AS schedule
		JOIN LATERAL (
			SELECT MIN(job.created_at) AS oldest_queued_at
			FROM document_jobs AS job
			JOIN documents AS source_document
			  ON source_document.id = job.document_id
			WHERE source_document.owner_user_id = schedule.owner_user_id
			  AND source_document.status IN ('uploaded', 'failed')
			  AND job.status = 'queued'
		) AS queued_work
		  ON queued_work.oldest_queued_at IS NOT NULL
		JOIN LATERAL (
			SELECT COUNT(*) AS in_flight_count
			FROM document_jobs AS job
			JOIN documents AS source_document
			  ON source_document.id = job.document_id
			WHERE source_document.owner_user_id = schedule.owner_user_id
			  AND job.status = 'processing'
		) AS active_work ON TRUE
		WHERE active_work.in_flight_count < $1
		ORDER BY
			CASE
				WHEN queued_work.oldest_queued_at <=
					CURRENT_TIMESTAMP - ($2::BIGINT * INTERVAL '1 millisecond')
				THEN 0
				ELSE 1
			END,
			schedule.last_dispatched_at ASC NULLS FIRST,
			queued_work.oldest_queued_at ASC,
			schedule.owner_user_id ASC
		FOR UPDATE OF schedule SKIP LOCKED
		LIMIT 1
	`

	var ownerUserID int64
	err := transaction.QueryRow(
		ctx,
		query,
		maxInFlight,
		starvationThreshold.Milliseconds(),
	).Scan(&ownerUserID)
	return ownerUserID, err
}

func ensureProcessingOwnerSchedule(
	ctx context.Context,
	transaction pgx.Tx,
	ownerUserID int64,
) error {
	const query = `
		INSERT INTO document_processing_owner_schedules (owner_user_id)
		VALUES ($1)
		ON CONFLICT (owner_user_id) DO NOTHING
	`
	if _, err := transaction.Exec(ctx, query, ownerUserID); err != nil {
		return fmt.Errorf(
			"ensure document processing owner schedule: %w",
			err,
		)
	}
	return nil
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
			chunk_write_ms = $8,
			python_total_ms = $9,
			source_open_ms = $10,
			metadata_read_ms = $11,
			text_extract_ms = $12,
			text_split_ms = $13,
			page_count = $14,
			slowest_page_number = $15,
			slowest_page_ms = $16,
			updated_at = CURRENT_TIMESTAMP,
			completed_at = CURRENT_TIMESTAMP
		WHERE id = $1
			AND status = 'processing'
	`

	stageMetrics := newProcessingStageDatabaseMetrics(metrics.ProcessorStages)
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
		optionalDurationMilliseconds(metrics.ChunkWriteDuration),
		stageMetrics.PythonTotalMS,
		stageMetrics.SourceOpenMS,
		stageMetrics.MetadataReadMS,
		stageMetrics.TextExtractMS,
		stageMetrics.TextSplitMS,
		stageMetrics.PageCount,
		stageMetrics.SlowestPageNumber,
		stageMetrics.SlowestPageMS,
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

// optionalDurationMilliseconds 保留“未观测”和“已执行但不足 1ms”的区别。
func optionalDurationMilliseconds(duration *time.Duration) *int64 {
	if duration == nil {
		return nil
	}

	milliseconds := durationMilliseconds(*duration)
	return &milliseconds
}

type processingStageDatabaseMetrics struct {
	PythonTotalMS     *int64
	SourceOpenMS      *int64
	MetadataReadMS    *int64
	TextExtractMS     *int64
	TextSplitMS       *int64
	PageCount         *int
	SlowestPageNumber *int
	SlowestPageMS     *int64
}

// newProcessingStageDatabaseMetrics 把可选处理器阶段指标转换为可空 SQL 参数。
func newProcessingStageDatabaseMetrics(
	metrics *document.ProcessorStageMetrics,
) processingStageDatabaseMetrics {
	if metrics == nil {
		return processingStageDatabaseMetrics{}
	}

	pythonTotalMS := durationMilliseconds(metrics.TotalDuration)
	sourceOpenMS := durationMilliseconds(metrics.SourceOpenDuration)
	metadataReadMS := durationMilliseconds(metrics.MetadataReadDuration)
	textExtractMS := durationMilliseconds(metrics.TextExtractDuration)
	textSplitMS := durationMilliseconds(metrics.TextSplitDuration)
	pageCount := metrics.PageCount
	slowestPageNumber := metrics.SlowestPageNumber
	slowestPageMS := durationMilliseconds(metrics.SlowestPageDuration)

	return processingStageDatabaseMetrics{
		PythonTotalMS:     &pythonTotalMS,
		SourceOpenMS:      &sourceOpenMS,
		MetadataReadMS:    &metadataReadMS,
		TextExtractMS:     &textExtractMS,
		TextSplitMS:       &textSplitMS,
		PageCount:         &pageCount,
		SlowestPageNumber: &slowestPageNumber,
		SlowestPageMS:     &slowestPageMS,
	}
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
