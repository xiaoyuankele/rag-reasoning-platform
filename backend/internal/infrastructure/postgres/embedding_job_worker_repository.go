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

// ClaimNextEmbeddingJob 原子领取已经到达执行时间的最早 queued 任务。
//
// FOR UPDATE SKIP LOCKED 防止并发 Worker 领取同一任务。Embedding 生命周期不会修改
// documents.status，但只允许为仍处于 ready 的文档执行向量任务。
func (r *EmbeddingJobRepository) ClaimNextEmbeddingJob(
	ctx context.Context,
) (embeddingdomain.Job, error) {
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

	const selectQuery = `
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
		ORDER BY j.next_attempt_at ASC, j.created_at ASC, j.id ASC
		FOR UPDATE OF j SKIP LOCKED
		LIMIT 1
	`

	queuedJob, err := scanEmbeddingJob(
		transaction.QueryRow(ctx, selectQuery),
	)
	if errors.Is(err, pgx.ErrNoRows) {
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

	if err := transaction.Commit(ctx); err != nil {
		return embeddingdomain.Job{}, fmt.Errorf(
			"commit claimed embedding job: %w",
			err,
		)
	}

	return claimedJob, nil
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
