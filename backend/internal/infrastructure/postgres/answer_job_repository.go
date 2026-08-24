package postgres

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	answerapplication "rag-reasoning-platform/backend/internal/application/answer"
	accessdomain "rag-reasoning-platform/backend/internal/domain/access"
	documentdomain "rag-reasoning-platform/backend/internal/domain/document"
)

// answerJobAdmissionAdvisoryLockID 只保护“统计 queued + INSERT”的短临界区。
// 它不覆盖 Worker 领取，更不会在远程模型调用期间持有。
const answerJobAdmissionAdvisoryLockID int64 = 0x524147414E535745

// AnswerJobRepository 实现异步问答的用户入口和 Worker 持久化端口。
type AnswerJobRepository struct {
	pool             *pgxpool.Pool
	admissionLimits  answerapplication.JobAdmissionLimits
	schedulingPolicy answerapplication.JobSchedulingPolicy
}

var _ answerapplication.ScopedJobRepository = (*AnswerJobRepository)(nil)
var _ answerapplication.JobWorkerRepository = (*AnswerJobRepository)(nil)
var _ answerapplication.InterruptedJobRecoverer = (*AnswerJobRepository)(nil)
var _ answerapplication.JobRetentionRepository = (*AnswerJobRepository)(nil)

// NewAnswerJobRepository 创建带容量和 Owner 公平策略的 PostgreSQL 仓储。
func NewAnswerJobRepository(
	pool *pgxpool.Pool,
	admissionLimits answerapplication.JobAdmissionLimits,
	schedulingPolicy answerapplication.JobSchedulingPolicy,
) *AnswerJobRepository {
	return &AnswerJobRepository{
		pool:             pool,
		admissionLimits:  admissionLimits,
		schedulingPolicy: schedulingPolicy,
	}
}

// CreateAnswerJob 在 OwnerScope 和全局队列容量边界内创建 queued 任务。
func (r *AnswerJobRepository) CreateAnswerJob(
	ctx context.Context,
	scope accessdomain.OwnerScope,
	input answerapplication.Input,
) (answerapplication.Job, error) {
	if !scope.IsValid() {
		return answerapplication.Job{}, accessdomain.ErrInvalidOwnerScope
	}
	if !r.admissionLimits.IsValid() {
		return answerapplication.Job{}, answerapplication.ErrInvalidAnswerJobAdmissionLimits
	}

	tx, err := r.pool.Begin(ctx)
	if err != nil {
		return answerapplication.Job{}, fmt.Errorf("begin answer job creation: %w", err)
	}
	defer func() { _ = tx.Rollback(context.Background()) }()

	// 指定文档问答必须在写任务前证明文档属于当前用户。
	// FOR SHARE 与删除事务协调，防止校验后到 INSERT 前文档被并发删除。
	if input.DocumentID != nil {
		const lockDocumentQuery = `
			SELECT id
			FROM documents
			WHERE id = $1
			  AND owner_user_id = $2
			FOR SHARE
		`
		var documentID int64
		err := tx.QueryRow(
			ctx,
			lockDocumentQuery,
			*input.DocumentID,
			scope.OwnerUserID(),
		).Scan(&documentID)
		if errors.Is(err, pgx.ErrNoRows) {
			return answerapplication.Job{}, documentdomain.ErrNotFound
		}
		if err != nil {
			return answerapplication.Job{}, fmt.Errorf(
				"lock scoped document before answer job creation: %w",
				err,
			)
		}
	}

	if _, err := tx.Exec(
		ctx,
		`SELECT pg_advisory_xact_lock($1)`,
		answerJobAdmissionAdvisoryLockID,
	); err != nil {
		return answerapplication.Job{}, fmt.Errorf("lock answer job admission: %w", err)
	}

	const countQueuedQuery = `
		SELECT
			COUNT(*) FILTER (WHERE owner_user_id = $1),
			COUNT(*)
		FROM answer_jobs
		WHERE status = 'queued'
	`
	var ownerQueued int64
	var globalQueued int64
	if err := tx.QueryRow(
		ctx,
		countQueuedQuery,
		scope.OwnerUserID(),
	).Scan(&ownerQueued, &globalQueued); err != nil {
		return answerapplication.Job{}, fmt.Errorf("count queued answer jobs: %w", err)
	}
	if ownerQueued >= int64(r.admissionLimits.MaxQueuedJobsPerOwner) {
		return answerapplication.Job{}, answerapplication.ErrAnswerOwnerQueueCapacity
	}
	if globalQueued >= int64(r.admissionLimits.MaxQueuedJobsGlobal) {
		return answerapplication.Job{}, answerapplication.ErrAnswerGlobalQueueCapacity
	}

	if err := ensureAnswerOwnerSchedule(ctx, tx, scope.OwnerUserID()); err != nil {
		return answerapplication.Job{}, err
	}

	const insertQuery = `
		INSERT INTO answer_jobs (
			owner_user_id,
			document_id,
			query,
			top_k,
			requested_response_language
		)
		VALUES ($1, $2, $3, $4, $5)
		RETURNING
			id, owner_user_id, document_id, query, top_k,
			requested_response_language, status, attempt_count,
			error_code, error_message, next_attempt_at,
			answer_text, resolved_response_language, sources,
			prompt_tokens, completion_tokens, total_tokens,
			created_at, updated_at, started_at, completed_at
	`
	job, err := scanAnswerJob(tx.QueryRow(
		ctx,
		insertQuery,
		scope.OwnerUserID(),
		input.DocumentID,
		input.Query,
		input.TopK,
		input.ResponseLanguage,
	))
	if isForeignKeyViolation(err) {
		return answerapplication.Job{}, documentdomain.ErrNotFound
	}
	if err != nil {
		return answerapplication.Job{}, fmt.Errorf("insert answer job: %w", err)
	}
	if err := tx.Commit(ctx); err != nil {
		return answerapplication.Job{}, fmt.Errorf("commit answer job creation: %w", err)
	}
	return job, nil
}

// GetAnswerJobByID 只返回当前 OwnerScope 可见的任务。
func (r *AnswerJobRepository) GetAnswerJobByID(
	ctx context.Context,
	scope accessdomain.OwnerScope,
	jobID int64,
) (answerapplication.Job, error) {
	if !scope.IsValid() {
		return answerapplication.Job{}, accessdomain.ErrInvalidOwnerScope
	}

	const query = `
		SELECT
			id, owner_user_id, document_id, query, top_k,
			requested_response_language, status, attempt_count,
			error_code, error_message, next_attempt_at,
			answer_text, resolved_response_language, sources,
			prompt_tokens, completion_tokens, total_tokens,
			created_at, updated_at, started_at, completed_at
		FROM answer_jobs
		WHERE id = $1 AND owner_user_id = $2
	`
	job, err := scanAnswerJob(r.pool.QueryRow(ctx, query, jobID, scope.OwnerUserID()))
	if errors.Is(err, pgx.ErrNoRows) {
		return answerapplication.Job{}, answerapplication.ErrAnswerJobNotFound
	}
	if err != nil {
		return answerapplication.Job{}, fmt.Errorf("get scoped answer job: %w", err)
	}
	return job, nil
}

// CancelAnswerJob 原子取消 queued 任务，canceled 重复调用保持幂等。
func (r *AnswerJobRepository) CancelAnswerJob(
	ctx context.Context,
	scope accessdomain.OwnerScope,
	jobID int64,
) (answerapplication.Job, error) {
	if !scope.IsValid() {
		return answerapplication.Job{}, accessdomain.ErrInvalidOwnerScope
	}

	tx, err := r.pool.Begin(ctx)
	if err != nil {
		return answerapplication.Job{}, fmt.Errorf("begin answer job cancellation: %w", err)
	}
	defer func() { _ = tx.Rollback(context.Background()) }()

	const lockQuery = `
		SELECT
			id, owner_user_id, document_id, query, top_k,
			requested_response_language, status, attempt_count,
			error_code, error_message, next_attempt_at,
			answer_text, resolved_response_language, sources,
			prompt_tokens, completion_tokens, total_tokens,
			created_at, updated_at, started_at, completed_at
		FROM answer_jobs
		WHERE id = $1 AND owner_user_id = $2
		FOR UPDATE
	`
	job, err := scanAnswerJob(tx.QueryRow(ctx, lockQuery, jobID, scope.OwnerUserID()))
	if errors.Is(err, pgx.ErrNoRows) {
		return answerapplication.Job{}, answerapplication.ErrAnswerJobNotFound
	}
	if err != nil {
		return answerapplication.Job{}, fmt.Errorf("lock answer job for cancellation: %w", err)
	}

	switch job.Status {
	case answerapplication.JobStatusCanceled:
		if err := tx.Commit(ctx); err != nil {
			return answerapplication.Job{}, fmt.Errorf("commit repeated answer cancellation: %w", err)
		}
		return job, nil
	case answerapplication.JobStatusProcessing:
		return answerapplication.Job{}, answerapplication.ErrAnswerJobProcessingCannotCancel
	case answerapplication.JobStatusSucceeded, answerapplication.JobStatusFailed:
		return answerapplication.Job{}, answerapplication.ErrAnswerJobTerminalCannotCancel
	case answerapplication.JobStatusQueued:
		// 继续更新。
	default:
		return answerapplication.Job{}, fmt.Errorf("cancel answer job with invalid status %q", job.Status)
	}

	const cancelQuery = `
		UPDATE answer_jobs
		SET
			status = 'canceled',
			error_code = NULL,
			error_message = NULL,
			updated_at = CURRENT_TIMESTAMP,
			started_at = NULL,
			completed_at = CURRENT_TIMESTAMP
		WHERE id = $1 AND status = 'queued'
		RETURNING
			id, owner_user_id, document_id, query, top_k,
			requested_response_language, status, attempt_count,
			error_code, error_message, next_attempt_at,
			answer_text, resolved_response_language, sources,
			prompt_tokens, completion_tokens, total_tokens,
			created_at, updated_at, started_at, completed_at
	`
	canceled, err := scanAnswerJob(tx.QueryRow(ctx, cancelQuery, job.ID))
	if errors.Is(err, pgx.ErrNoRows) {
		return answerapplication.Job{}, answerapplication.ErrAnswerJobProcessingCannotCancel
	}
	if err != nil {
		return answerapplication.Job{}, fmt.Errorf("cancel answer job: %w", err)
	}
	if err := tx.Commit(ctx); err != nil {
		return answerapplication.Job{}, fmt.Errorf("commit answer job cancellation: %w", err)
	}
	return canceled, nil
}

// ClaimNextAnswerJob 按 Owner 公平策略原子领取一条到期 queued 任务。
func (r *AnswerJobRepository) ClaimNextAnswerJob(
	ctx context.Context,
) (answerapplication.Job, error) {
	if !r.schedulingPolicy.IsValid() {
		return answerapplication.Job{}, answerapplication.ErrInvalidAnswerJobSchedulingPolicy
	}

	tx, err := r.pool.Begin(ctx)
	if err != nil {
		return answerapplication.Job{}, fmt.Errorf("begin answer job claim: %w", err)
	}
	defer func() { _ = tx.Rollback(context.Background()) }()

	ownerID, err := selectNextAnswerOwner(
		ctx,
		tx,
		r.schedulingPolicy.MaxInFlightPerOwner,
		r.schedulingPolicy.StarvationThreshold,
	)
	if errors.Is(err, pgx.ErrNoRows) &&
		r.schedulingPolicy.MaxBorrowedInFlightPerOwner > r.schedulingPolicy.MaxInFlightPerOwner {
		ownerID, err = selectNextAnswerOwner(
			ctx,
			tx,
			r.schedulingPolicy.MaxBorrowedInFlightPerOwner,
			r.schedulingPolicy.StarvationThreshold,
		)
	}
	if errors.Is(err, pgx.ErrNoRows) {
		return answerapplication.Job{}, answerapplication.ErrNoQueuedAnswerJob
	}
	if err != nil {
		return answerapplication.Job{}, fmt.Errorf("select next answer owner: %w", err)
	}

	const selectQuery = `
		SELECT
			id, owner_user_id, document_id, query, top_k,
			requested_response_language, status, attempt_count,
			error_code, error_message, next_attempt_at,
			answer_text, resolved_response_language, sources,
			prompt_tokens, completion_tokens, total_tokens,
			created_at, updated_at, started_at, completed_at
		FROM answer_jobs
		WHERE owner_user_id = $1
		  AND status = 'queued'
		  AND next_attempt_at <= CURRENT_TIMESTAMP
		ORDER BY next_attempt_at ASC, created_at ASC, id ASC
		FOR UPDATE SKIP LOCKED
		LIMIT 1
	`
	queued, err := scanAnswerJob(tx.QueryRow(ctx, selectQuery, ownerID))
	if errors.Is(err, pgx.ErrNoRows) {
		return answerapplication.Job{}, answerapplication.ErrNoQueuedAnswerJob
	}
	if err != nil {
		return answerapplication.Job{}, fmt.Errorf("select queued answer job: %w", err)
	}

	const claimQuery = `
		UPDATE answer_jobs
		SET
			status = 'processing',
			attempt_count = attempt_count + 1,
			error_code = NULL,
			error_message = NULL,
			updated_at = CURRENT_TIMESTAMP,
			started_at = CURRENT_TIMESTAMP,
			completed_at = NULL
		WHERE id = $1 AND status = 'queued'
		RETURNING
			id, owner_user_id, document_id, query, top_k,
			requested_response_language, status, attempt_count,
			error_code, error_message, next_attempt_at,
			answer_text, resolved_response_language, sources,
			prompt_tokens, completion_tokens, total_tokens,
			created_at, updated_at, started_at, completed_at
	`
	claimed, err := scanAnswerJob(tx.QueryRow(ctx, claimQuery, queued.ID))
	if err != nil {
		return answerapplication.Job{}, fmt.Errorf("mark answer job processing: %w", err)
	}

	commandTag, err := tx.Exec(
		ctx,
		`UPDATE answer_owner_schedules
		 SET last_dispatched_at = CURRENT_TIMESTAMP,
		     updated_at = CURRENT_TIMESTAMP
		 WHERE owner_user_id = $1`,
		ownerID,
	)
	if err != nil {
		return answerapplication.Job{}, fmt.Errorf("update answer owner schedule: %w", err)
	}
	if commandTag.RowsAffected() != 1 {
		return answerapplication.Job{}, fmt.Errorf(
			"update answer owner schedule: expected 1 row, got %d",
			commandTag.RowsAffected(),
		)
	}
	if err := tx.Commit(ctx); err != nil {
		return answerapplication.Job{}, fmt.Errorf("commit answer job claim: %w", err)
	}
	return claimed, nil
}

// GetAnswerJobQueueStats 返回不含用户内容的全局队列快照。
// 该查询只用于观测，不能参与任务是否成功的业务判断。
func (r *AnswerJobRepository) GetAnswerJobQueueStats(
	ctx context.Context,
) (answerapplication.JobQueueStats, error) {
	const query = `
		SELECT
			COUNT(*) FILTER (WHERE status = 'queued'),
			COUNT(*) FILTER (
				WHERE status = 'queued'
				  AND next_attempt_at <= CURRENT_TIMESTAMP
			),
			COUNT(*) FILTER (WHERE status = 'processing'),
			COALESCE((
				SELECT MAX(owner_processing_count)
				FROM (
					SELECT COUNT(*) AS owner_processing_count
					FROM answer_jobs
					WHERE status = 'processing'
					GROUP BY owner_user_id
				) AS processing_by_owner
			), 0),
			COALESCE((
				EXTRACT(EPOCH FROM (
					CURRENT_TIMESTAMP -
					MIN(GREATEST(created_at, next_attempt_at)) FILTER (
						WHERE status = 'queued'
						  AND next_attempt_at <= CURRENT_TIMESTAMP
					)
				)) * 1000
			)::BIGINT, 0)
		FROM answer_jobs
	`
	var stats answerapplication.JobQueueStats
	var oldestReadyWaitMilliseconds int64
	if err := r.pool.QueryRow(ctx, query).Scan(
		&stats.QueuedCount,
		&stats.ReadyQueuedCount,
		&stats.ProcessingCount,
		&stats.MaxOwnerProcessingCount,
		&oldestReadyWaitMilliseconds,
	); err != nil {
		return answerapplication.JobQueueStats{}, fmt.Errorf(
			"get answer job queue stats: %w",
			err,
		)
	}
	stats.OldestReadyWait = time.Duration(oldestReadyWaitMilliseconds) * time.Millisecond
	if !stats.IsValid() {
		return answerapplication.JobQueueStats{}, errors.New(
			"get answer job queue stats: database returned invalid counts",
		)
	}
	return stats, nil
}

// DeleteExpiredAnswerJobs 原子删除一批超过保留期的终态任务。
// SKIP LOCKED 允许未来多个后端副本并行维护，且不会等待正在被其他事务处理的行。
func (r *AnswerJobRepository) DeleteExpiredAnswerJobs(
	ctx context.Context,
	completedBefore time.Time,
	limit int,
) (int64, error) {
	if completedBefore.IsZero() || limit <= 0 {
		return 0, answerapplication.ErrInvalidAnswerJobRetention
	}
	const query = `
		WITH expired AS (
			SELECT id
			FROM answer_jobs
			WHERE status IN ('succeeded', 'failed', 'canceled')
			  AND completed_at < $1
			ORDER BY completed_at ASC, id ASC
			LIMIT $2
			FOR UPDATE SKIP LOCKED
		)
		DELETE FROM answer_jobs AS job
		USING expired
		WHERE job.id = expired.id
	`
	result, err := r.pool.Exec(ctx, query, completedBefore, limit)
	if err != nil {
		return 0, fmt.Errorf("delete expired answer jobs: %w", err)
	}
	return result.RowsAffected(), nil
}

func selectNextAnswerOwner(
	ctx context.Context,
	tx pgx.Tx,
	maxInFlight int,
	starvationThreshold time.Duration,
) (int64, error) {
	const query = `
		SELECT schedule.owner_user_id
		FROM answer_owner_schedules AS schedule
		JOIN LATERAL (
			SELECT MIN(GREATEST(job.created_at, job.next_attempt_at)) AS oldest_eligible_at
			FROM answer_jobs AS job
			WHERE job.owner_user_id = schedule.owner_user_id
			  AND job.status = 'queued'
			  AND job.next_attempt_at <= CURRENT_TIMESTAMP
		) AS queued_work ON queued_work.oldest_eligible_at IS NOT NULL
		JOIN LATERAL (
			SELECT COUNT(*) AS in_flight_count
			FROM answer_jobs AS job
			WHERE job.owner_user_id = schedule.owner_user_id
			  AND job.status = 'processing'
		) AS active_work ON TRUE
		WHERE active_work.in_flight_count < $1
		ORDER BY
			CASE
				WHEN queued_work.oldest_eligible_at <=
					CURRENT_TIMESTAMP - ($2::BIGINT * INTERVAL '1 millisecond')
				THEN 0 ELSE 1
			END,
			schedule.last_dispatched_at ASC NULLS FIRST,
			queued_work.oldest_eligible_at ASC,
			schedule.owner_user_id ASC
		FOR UPDATE OF schedule SKIP LOCKED
		LIMIT 1
	`
	var ownerID int64
	err := tx.QueryRow(
		ctx,
		query,
		maxInFlight,
		starvationThreshold.Milliseconds(),
	).Scan(&ownerID)
	return ownerID, err
}

func ensureAnswerOwnerSchedule(
	ctx context.Context,
	tx pgx.Tx,
	ownerID int64,
) error {
	_, err := tx.Exec(
		ctx,
		`INSERT INTO answer_owner_schedules (owner_user_id)
		 VALUES ($1)
		 ON CONFLICT (owner_user_id) DO NOTHING`,
		ownerID,
	)
	if err != nil {
		return fmt.Errorf("ensure answer owner schedule: %w", err)
	}
	return nil
}

// MarkAnswerJobSucceeded 原子保存答案快照并进入 succeeded。
func (r *AnswerJobRepository) MarkAnswerJobSucceeded(
	ctx context.Context,
	jobID int64,
	output answerapplication.Output,
) error {
	if strings.TrimSpace(output.Answer) == "" ||
		(output.ResponseLanguage != answerapplication.ResponseLanguageChinese &&
			output.ResponseLanguage != answerapplication.ResponseLanguageEnglish) ||
		output.PromptTokens < 0 || output.CompletionTokens < 0 ||
		output.TotalTokens != output.PromptTokens+output.CompletionTokens {
		return errors.New("answer job completion is invalid")
	}
	sourcesJSON, err := json.Marshal(output.Sources)
	if err != nil {
		return fmt.Errorf("encode answer job sources: %w", err)
	}

	const query = `
		UPDATE answer_jobs
		SET
			status = 'succeeded',
			error_code = NULL,
			error_message = NULL,
			answer_text = $2,
			resolved_response_language = $3,
			sources = $4::JSONB,
			prompt_tokens = $5,
			completion_tokens = $6,
			total_tokens = $7,
			updated_at = CURRENT_TIMESTAMP,
			completed_at = CURRENT_TIMESTAMP
		WHERE id = $1 AND status = 'processing'
	`
	commandTag, err := r.pool.Exec(
		ctx,
		query,
		jobID,
		output.Answer,
		output.ResponseLanguage,
		sourcesJSON,
		output.PromptTokens,
		output.CompletionTokens,
		output.TotalTokens,
	)
	if err != nil {
		return fmt.Errorf("mark answer job succeeded: %w", err)
	}
	if commandTag.RowsAffected() != 1 {
		return answerapplication.ErrAnswerJobNotProcessing
	}
	return nil
}

// RequeueAnswerJob 把暂时不可执行的 processing 任务延后重试。
func (r *AnswerJobRepository) RequeueAnswerJob(
	ctx context.Context,
	jobID int64,
	nextAttemptAt time.Time,
	errorCode answerapplication.JobErrorCode,
	errorMessage string,
) error {
	const query = `
		UPDATE answer_jobs
		SET
			status = 'queued',
			error_code = $3,
			error_message = $4,
			next_attempt_at = $2,
			answer_text = NULL,
			resolved_response_language = NULL,
			sources = NULL,
			prompt_tokens = NULL,
			completion_tokens = NULL,
			total_tokens = NULL,
			updated_at = CURRENT_TIMESTAMP,
			started_at = NULL,
			completed_at = NULL
		WHERE id = $1 AND status = 'processing'
	`
	commandTag, err := r.pool.Exec(ctx, query, jobID, nextAttemptAt, errorCode, errorMessage)
	if err != nil {
		return fmt.Errorf("requeue answer job: %w", err)
	}
	if commandTag.RowsAffected() != 1 {
		return answerapplication.ErrAnswerJobNotProcessing
	}
	return nil
}

// MarkAnswerJobFailed 保存安全失败分类，不暴露底层诊断错误。
func (r *AnswerJobRepository) MarkAnswerJobFailed(
	ctx context.Context,
	jobID int64,
	errorCode answerapplication.JobErrorCode,
	errorMessage string,
) error {
	const query = `
		UPDATE answer_jobs
		SET
			status = 'failed',
			error_code = $2,
			error_message = $3,
			updated_at = CURRENT_TIMESTAMP,
			completed_at = CURRENT_TIMESTAMP
		WHERE id = $1 AND status = 'processing'
	`
	commandTag, err := r.pool.Exec(ctx, query, jobID, errorCode, errorMessage)
	if err != nil {
		return fmt.Errorf("mark answer job failed: %w", err)
	}
	if commandTag.RowsAffected() != 1 {
		return answerapplication.ErrAnswerJobNotProcessing
	}
	return nil
}

// RequeueInterruptedAnswerJobs 恢复上次进程退出时遗留的 processing 任务。
func (r *AnswerJobRepository) RequeueInterruptedAnswerJobs(
	ctx context.Context,
	recoveredAt time.Time,
	errorCode answerapplication.JobErrorCode,
	errorMessage string,
) (int64, error) {
	const query = `
		UPDATE answer_jobs
		SET
			status = 'queued',
			error_code = $2,
			error_message = $3,
			next_attempt_at = $1,
			updated_at = CURRENT_TIMESTAMP,
			started_at = NULL,
			completed_at = NULL
		WHERE status = 'processing'
	`
	commandTag, err := r.pool.Exec(ctx, query, recoveredAt, errorCode, errorMessage)
	if err != nil {
		return 0, fmt.Errorf("requeue interrupted answer jobs: %w", err)
	}
	return commandTag.RowsAffected(), nil
}

func scanAnswerJob(row pgx.Row) (answerapplication.Job, error) {
	var job answerapplication.Job
	var requestedLanguage string
	var status string
	var errorCode *string
	var answerText *string
	var resolvedLanguage *string
	var sourcesJSON []byte
	var promptTokens *int
	var completionTokens *int
	var totalTokens *int

	err := row.Scan(
		&job.ID,
		&job.OwnerUserID,
		&job.DocumentID,
		&job.Query,
		&job.TopK,
		&requestedLanguage,
		&status,
		&job.AttemptCount,
		&errorCode,
		&job.ErrorMessage,
		&job.NextAttemptAt,
		&answerText,
		&resolvedLanguage,
		&sourcesJSON,
		&promptTokens,
		&completionTokens,
		&totalTokens,
		&job.CreatedAt,
		&job.UpdatedAt,
		&job.StartedAt,
		&job.CompletedAt,
	)
	if err != nil {
		return answerapplication.Job{}, err
	}

	job.RequestedResponseLanguage = answerapplication.ResponseLanguage(requestedLanguage)
	job.Status = answerapplication.JobStatus(status)
	if errorCode != nil {
		job.ErrorCode = answerapplication.JobErrorCode(*errorCode)
	}
	if !job.Status.IsValid() {
		return answerapplication.Job{}, fmt.Errorf("invalid answer job status %q", status)
	}

	if job.Status == answerapplication.JobStatusSucceeded {
		if answerText == nil || resolvedLanguage == nil ||
			promptTokens == nil || completionTokens == nil || totalTokens == nil || sourcesJSON == nil {
			return answerapplication.Job{}, errors.New("succeeded answer job is missing result fields")
		}
		var sources []answerapplication.Source
		if err := json.Unmarshal(sourcesJSON, &sources); err != nil {
			return answerapplication.Job{}, fmt.Errorf("decode answer job sources: %w", err)
		}
		if sources == nil {
			sources = make([]answerapplication.Source, 0)
		}
		job.Result = &answerapplication.JobResult{
			Answer:           *answerText,
			ResponseLanguage: answerapplication.ResponseLanguage(*resolvedLanguage),
			Sources:          sources,
			PromptTokens:     *promptTokens,
			CompletionTokens: *completionTokens,
			TotalTokens:      *totalTokens,
		}
	}

	return job, nil
}
