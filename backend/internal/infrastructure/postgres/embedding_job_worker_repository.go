package postgres

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/pgvector/pgvector-go"

	embeddingdomain "rag-reasoning-platform/backend/internal/domain/embedding"
)

var _ embeddingdomain.JobClaimer = (*EmbeddingJobRepository)(nil)
var _ embeddingdomain.JobFinalizer = (*EmbeddingJobRepository)(nil)
var _ embeddingdomain.InterruptedJobRecoverer = (*EmbeddingJobRepository)(nil)

// RequeueInterruptedEmbeddingJobs 在单实例应用启动时恢复上次进程遗留的任务。
//
// attempt_count 不清零，因为被中断的一轮已经真实领取过任务；下次领取时会继续递增。
func (r *EmbeddingJobRepository) RequeueInterruptedEmbeddingJobs(
	ctx context.Context,
	recoveredAt time.Time,
	errorMessage string,
) (int64, error) {
	const query = `
		UPDATE embedding_jobs
		SET
			status = 'queued',
			error_message = $2,
			next_attempt_at = $1,
			updated_at = CURRENT_TIMESTAMP,
			started_at = NULL,
			completed_at = NULL,
			prompt_tokens = NULL,
			total_tokens = NULL
		WHERE status = 'processing'
	`

	commandTag, err := r.pool.Exec(ctx, query, recoveredAt, errorMessage)
	if err != nil {
		return 0, fmt.Errorf("requeue interrupted embedding jobs: %w", err)
	}

	return commandTag.RowsAffected(), nil
}

// ClaimNextEmbeddingJob 按 Owner 公平策略原子领取已经到期的 queued 任务。
//
// 第一遍优先选择未达到基础并发上限的 Owner；只有没有此类 Owner 时，
// 第二遍才允许借用空闲容量。Embedding 生命周期不会修改 documents.status，
// 但只允许为仍处于 ready 的文档执行向量任务。
func (r *EmbeddingJobRepository) ClaimNextEmbeddingJob(
	ctx context.Context,
) (embeddingdomain.Job, error) {
	if !r.schedulingPolicy.IsValid() {
		return embeddingdomain.Job{},
			embeddingdomain.ErrInvalidJobSchedulingPolicy
	}

	transaction, err := r.pool.Begin(ctx)
	if err != nil {
		return embeddingdomain.Job{}, fmt.Errorf(
			"begin claim embedding job transaction: %w",
			err,
		)
	}
	defer func() {
		_ = transaction.Rollback(context.Background())
	}()

	ownerUserID, err := selectNextEmbeddingOwner(
		ctx,
		transaction,
		r.schedulingPolicy.MaxInFlightPerOwner,
		r.schedulingPolicy.StarvationThreshold,
	)
	if errors.Is(err, pgx.ErrNoRows) &&
		r.schedulingPolicy.MaxBorrowedInFlightPerOwner >
			r.schedulingPolicy.MaxInFlightPerOwner {
		ownerUserID, err = selectNextEmbeddingOwner(
			ctx,
			transaction,
			r.schedulingPolicy.MaxBorrowedInFlightPerOwner,
			r.schedulingPolicy.StarvationThreshold,
		)
	}
	if errors.Is(err, pgx.ErrNoRows) {
		return embeddingdomain.Job{}, embeddingdomain.ErrNoQueuedJob
	}
	if err != nil {
		return embeddingdomain.Job{}, fmt.Errorf(
			"select next embedding job owner: %w",
			err,
		)
	}

	const selectJobQuery = `
		SELECT
			j.id,
			j.document_id,
			j.model_name,
			j.dimensions,
			j.status,
			j.attempt_count,
			j.error_message,
			j.next_attempt_at,
			j.prompt_tokens,
			j.total_tokens,
			j.created_at,
			j.updated_at,
			j.started_at,
			j.completed_at
		FROM embedding_jobs AS j
		INNER JOIN documents AS d ON d.id = j.document_id
		WHERE j.status = 'queued'
			AND j.next_attempt_at <= CURRENT_TIMESTAMP
			AND d.status = 'ready'
			AND d.owner_user_id = $1
		ORDER BY j.next_attempt_at ASC, j.created_at ASC, j.id ASC
		FOR UPDATE OF j SKIP LOCKED
		LIMIT 1
	`

	queuedJob, err := scanEmbeddingJob(
		transaction.QueryRow(ctx, selectJobQuery, ownerUserID),
	)
	if errors.Is(err, pgx.ErrNoRows) {
		// Owner 调度行已经被当前事务锁定。若任务在两条语句之间被取消或
		// 删除，本轮按空队列处理，下一次轮询会重新选择 Owner。
		return embeddingdomain.Job{}, embeddingdomain.ErrNoQueuedJob
	}
	if err != nil {
		return embeddingdomain.Job{}, fmt.Errorf(
			"select next queued embedding job: %w",
			err,
		)
	}

	const updateQuery = `
		UPDATE embedding_jobs
		SET
			status = 'processing',
			attempt_count = attempt_count + 1,
			error_message = NULL,
			prompt_tokens = NULL,
			total_tokens = NULL,
			updated_at = CURRENT_TIMESTAMP,
			started_at = CURRENT_TIMESTAMP,
			completed_at = NULL
		WHERE id = $1
			AND status = 'queued'
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

	claimedJob, err := scanEmbeddingJob(
		transaction.QueryRow(ctx, updateQuery, queuedJob.ID),
	)
	if err != nil {
		return embeddingdomain.Job{}, fmt.Errorf(
			"mark embedding job as processing: %w",
			err,
		)
	}

	const updateScheduleQuery = `
		UPDATE embedding_owner_schedules
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
		return embeddingdomain.Job{}, fmt.Errorf(
			"update embedding owner schedule: %w",
			err,
		)
	}
	if scheduleCommandTag.RowsAffected() != 1 {
		return embeddingdomain.Job{}, fmt.Errorf(
			"update embedding owner schedule: expected 1 updated row, got %d",
			scheduleCommandTag.RowsAffected(),
		)
	}

	if err := transaction.Commit(ctx); err != nil {
		return embeddingdomain.Job{}, fmt.Errorf(
			"commit claimed embedding job: %w",
			err,
		)
	}

	return claimedJob, nil
}

// selectNextEmbeddingOwner 锁定一个存在到期 queued 任务且尚有并发容量的
// Owner。重试任务从 next_attempt_at 开始计算等待时间，而不是从最初创建时间
// 开始，防止刚结束退避的失败任务被错误标记为长期饥饿。
func selectNextEmbeddingOwner(
	ctx context.Context,
	transaction pgx.Tx,
	maxInFlight int,
	starvationThreshold time.Duration,
) (int64, error) {
	const query = `
		SELECT schedule.owner_user_id
		FROM embedding_owner_schedules AS schedule
		JOIN LATERAL (
			SELECT MIN(
				GREATEST(job.created_at, job.next_attempt_at)
			) AS oldest_eligible_at
			FROM embedding_jobs AS job
			JOIN documents AS source_document
			  ON source_document.id = job.document_id
			WHERE source_document.owner_user_id = schedule.owner_user_id
			  AND source_document.status = 'ready'
			  AND job.status = 'queued'
			  AND job.next_attempt_at <= CURRENT_TIMESTAMP
		) AS queued_work
		  ON queued_work.oldest_eligible_at IS NOT NULL
		JOIN LATERAL (
			SELECT COUNT(*) AS in_flight_count
			FROM embedding_jobs AS job
			JOIN documents AS source_document
			  ON source_document.id = job.document_id
			WHERE source_document.owner_user_id = schedule.owner_user_id
			  AND job.status = 'processing'
		) AS active_work ON TRUE
		WHERE active_work.in_flight_count < $1
		ORDER BY
			CASE
				WHEN queued_work.oldest_eligible_at <=
					CURRENT_TIMESTAMP - ($2::BIGINT * INTERVAL '1 millisecond')
				THEN 0
				ELSE 1
			END,
			schedule.last_dispatched_at ASC NULLS FIRST,
			queued_work.oldest_eligible_at ASC,
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

func ensureEmbeddingOwnerSchedule(
	ctx context.Context,
	transaction pgx.Tx,
	ownerUserID int64,
) error {
	const query = `
		INSERT INTO embedding_owner_schedules (owner_user_id)
		VALUES ($1)
		ON CONFLICT (owner_user_id) DO NOTHING
	`
	if _, err := transaction.Exec(ctx, query, ownerUserID); err != nil {
		return fmt.Errorf("ensure embedding owner schedule: %w", err)
	}
	return nil
}

// MarkEmbeddingJobSucceeded 原子覆盖一份文档的全部当前向量，并把任务标记为 succeeded。
//
// 远程 API 调用必须在进入本方法前完成。本事务只负责快速锁定、核对、写入和状态更新；
// 任何一步失败都会回滚，旧向量不会被半份新结果覆盖。
func (r *EmbeddingJobRepository) MarkEmbeddingJobSucceeded(
	ctx context.Context,
	jobID int64,
	completion embeddingdomain.JobCompletion,
) error {
	if len(completion.Vectors) == 0 {
		return errors.New("embedding completion must contain vectors")
	}
	if completion.PromptTokens < 0 || completion.TotalTokens < 0 ||
		completion.PromptTokens > completion.TotalTokens {
		return errors.New("embedding completion contains invalid token usage")
	}

	transaction, err := r.pool.Begin(ctx)
	if err != nil {
		return fmt.Errorf("begin embedding success transaction: %w", err)
	}
	defer func() {
		_ = transaction.Rollback(context.Background())
	}()

	const lockQuery = `
		SELECT j.document_id, j.dimensions
		FROM embedding_jobs AS j
		INNER JOIN documents AS d ON d.id = j.document_id
		WHERE j.id = $1
			AND j.status = 'processing'
			AND d.status = 'ready'
		FOR UPDATE OF j
	`

	var documentID int64
	var dimensions int
	err = transaction.QueryRow(ctx, lockQuery, jobID).Scan(
		&documentID,
		&dimensions,
	)
	if errors.Is(err, pgx.ErrNoRows) {
		return embeddingdomain.ErrJobNotProcessing
	}
	if err != nil {
		return fmt.Errorf("lock embedding job for success: %w", err)
	}

	var chunkCount int
	if err := transaction.QueryRow(
		ctx,
		"SELECT COUNT(*) FROM text_chunks WHERE document_id = $1",
		documentID,
	).Scan(&chunkCount); err != nil {
		return fmt.Errorf("count embedding job document chunks: %w", err)
	}
	if chunkCount != len(completion.Vectors) {
		return fmt.Errorf(
			"embedding completion has %d vectors for %d document chunks",
			len(completion.Vectors),
			chunkCount,
		)
	}

	// 旧向量删除和新向量写入处于同一个事务；后续失败时删除也会一起回滚。
	const deleteCurrentQuery = `
		DELETE FROM chunk_embeddings AS embedding
		USING text_chunks AS chunk
		WHERE embedding.chunk_id = chunk.id
			AND chunk.document_id = $1
	`
	if _, err := transaction.Exec(ctx, deleteCurrentQuery, documentID); err != nil {
		return fmt.Errorf("delete current document embeddings: %w", err)
	}

	const insertQuery = `
		INSERT INTO chunk_embeddings (
			chunk_id,
			embedding_job_id,
			embedding
		)
		SELECT chunk.id, $2, $3
		FROM text_chunks AS chunk
		WHERE chunk.id = $1
			AND chunk.document_id = $4
	`

	seenChunkIDs := make(map[int64]struct{}, len(completion.Vectors))
	for _, chunkVector := range completion.Vectors {
		if chunkVector.ChunkID <= 0 {
			return errors.New("embedding completion contains invalid chunk ID")
		}
		if _, duplicate := seenChunkIDs[chunkVector.ChunkID]; duplicate {
			return fmt.Errorf(
				"embedding completion contains duplicate chunk ID %d",
				chunkVector.ChunkID,
			)
		}
		seenChunkIDs[chunkVector.ChunkID] = struct{}{}

		if len(chunkVector.Values) != dimensions {
			return fmt.Errorf(
				"chunk %d vector has %d dimensions, want %d",
				chunkVector.ChunkID,
				len(chunkVector.Values),
				dimensions,
			)
		}

		commandTag, err := transaction.Exec(
			ctx,
			insertQuery,
			chunkVector.ChunkID,
			jobID,
			pgvector.NewVector(chunkVector.Values),
			documentID,
		)
		if err != nil {
			return fmt.Errorf(
				"insert embedding for chunk %d: %w",
				chunkVector.ChunkID,
				err,
			)
		}
		if commandTag.RowsAffected() != 1 {
			return fmt.Errorf(
				"insert embedding for chunk %d: chunk does not belong to document %d",
				chunkVector.ChunkID,
				documentID,
			)
		}
	}

	const updateJobQuery = `
		UPDATE embedding_jobs
		SET
			status = 'succeeded',
			error_message = NULL,
			prompt_tokens = $2,
			total_tokens = $3,
			updated_at = CURRENT_TIMESTAMP,
			completed_at = CURRENT_TIMESTAMP
		WHERE id = $1
			AND status = 'processing'
	`
	commandTag, err := transaction.Exec(
		ctx,
		updateJobQuery,
		jobID,
		completion.PromptTokens,
		completion.TotalTokens,
	)
	if err != nil {
		return fmt.Errorf("mark embedding job succeeded: %w", err)
	}
	if commandTag.RowsAffected() != 1 {
		return embeddingdomain.ErrJobNotProcessing
	}

	if err := transaction.Commit(ctx); err != nil {
		return fmt.Errorf("commit embedding job success: %w", err)
	}

	return nil
}

// RequeueEmbeddingJob 把临时失败的 processing 任务重新放回 queued。
//
// TODO(learner): 按实践任务卡实现这个真实生产方法。
func (r *EmbeddingJobRepository) RequeueEmbeddingJob(
	ctx context.Context,
	jobID int64,
	nextAttemptAt time.Time,
	errorMessage string,
) error {
	const query = `
		UPDATE embedding_jobs
		SET
			status = 'queued',
			error_message = $3,
			next_attempt_at = $2,
			updated_at = CURRENT_TIMESTAMP,
			started_at = NULL,
			completed_at = NULL,
			prompt_tokens = NULL,
			total_tokens = NULL
		WHERE id = $1
			AND status = 'processing'
	`
	commandTag, err := r.pool.Exec(
		ctx,
		query,
		jobID,
		nextAttemptAt,
		errorMessage,
	)

	if err != nil {
		return fmt.Errorf("requeue embedding job: %w", err)
	}

	if commandTag.RowsAffected() != 1 {
		return embeddingdomain.ErrJobNotProcessing
	}

	return nil
}

// MarkEmbeddingJobFailed 把无需继续重试的 processing 任务标记为 failed。
func (r *EmbeddingJobRepository) MarkEmbeddingJobFailed(
	ctx context.Context,
	jobID int64,
	errorMessage string,
) error {
	const query = `
		UPDATE embedding_jobs
		SET
			status = 'failed',
			error_message = $2,
			updated_at = CURRENT_TIMESTAMP,
			completed_at = CURRENT_TIMESTAMP
		WHERE id = $1
			AND status = 'processing'
	`

	commandTag, err := r.pool.Exec(ctx, query, jobID, errorMessage)
	if err != nil {
		return fmt.Errorf("mark embedding job failed: %w", err)
	}
	if commandTag.RowsAffected() != 1 {
		return embeddingdomain.ErrJobNotProcessing
	}

	return nil
}
