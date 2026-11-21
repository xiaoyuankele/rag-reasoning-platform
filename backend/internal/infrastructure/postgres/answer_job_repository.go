package postgres

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"sort"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	answerapplication "rag-reasoning-platform/backend/internal/application/answer"
	accessdomain "rag-reasoning-platform/backend/internal/domain/access"
	conversationdomain "rag-reasoning-platform/backend/internal/domain/conversation"
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
	leasePolicy      answerapplication.JobLeasePolicy
	newLeaseToken    func() (string, error)
}

var _ answerapplication.ScopedJobRepository = (*AnswerJobRepository)(nil)
var _ answerapplication.JobWorkerRepository = (*AnswerJobRepository)(nil)
var _ answerapplication.ExpiredJobRecoverer = (*AnswerJobRepository)(nil)
var _ answerapplication.JobRetentionRepository = (*AnswerJobRepository)(nil)

var defaultAnswerJobLeasePolicy = answerapplication.JobLeasePolicy{
	WorkerID:      "answer-worker",
	LeaseDuration: time.Minute,
}

// NewAnswerJobRepository 创建带容量和 Owner 公平策略的 PostgreSQL 仓储。
func NewAnswerJobRepository(
	pool *pgxpool.Pool,
	admissionLimits answerapplication.JobAdmissionLimits,
	schedulingPolicy answerapplication.JobSchedulingPolicy,
) *AnswerJobRepository {
	return NewAnswerJobRepositoryWithPolicies(
		pool,
		admissionLimits,
		schedulingPolicy,
		defaultAnswerJobLeasePolicy,
	)
}

// NewAnswerJobRepositoryWithPolicies 创建同时使用容量、公平调度和租约策略的仓储。
func NewAnswerJobRepositoryWithPolicies(
	pool *pgxpool.Pool,
	admissionLimits answerapplication.JobAdmissionLimits,
	schedulingPolicy answerapplication.JobSchedulingPolicy,
	leasePolicy answerapplication.JobLeasePolicy,
) *AnswerJobRepository {
	return &AnswerJobRepository{
		pool:             pool,
		admissionLimits:  admissionLimits,
		schedulingPolicy: schedulingPolicy,
		leasePolicy:      leasePolicy,
		newLeaseToken:    newAnswerJobLeaseToken,
	}
}

// newAnswerJobLeaseToken 生成不可预测的 fencing token。
func newAnswerJobLeaseToken() (string, error) {
	randomBytes := make([]byte, 32)
	if _, err := rand.Read(randomBytes); err != nil {
		return "", fmt.Errorf("read answer job lease randomness: %w", err)
	}
	return hex.EncodeToString(randomBytes), nil
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
	if (input.ConversationID == nil) != (input.UserMessageID == nil) ||
		(input.ConversationID == nil) != (input.AssistantMessageID == nil) {
		return answerapplication.Job{}, errors.New("conversation answer job links must be all present or all absent")
	}
	if input.ConversationID != nil {
		if *input.ConversationID <= 0 || *input.UserMessageID <= 0 || *input.AssistantMessageID <= 0 {
			return answerapplication.Job{}, errors.New("conversation answer job link IDs must be positive")
		}
	}

	tx, err := r.pool.Begin(ctx)
	if err != nil {
		return answerapplication.Job{}, fmt.Errorf("begin answer job creation: %w", err)
	}
	defer func() { _ = tx.Rollback(context.Background()) }()

	const selectByUserMessageQuery = `
		SELECT
			id, owner_user_id, requested_document_id, query, top_k,
			requested_response_language, status, attempt_count,
			error_code, error_message, next_attempt_at,
			answer_text, resolved_response_language, sources,
			prompt_tokens, completion_tokens, total_tokens,
			created_at, updated_at, started_at, completed_at,
			conversation_id, user_message_id, assistant_message_id
		FROM answer_jobs
		WHERE user_message_id = $1 AND owner_user_id = $2
		ORDER BY id DESC
		LIMIT 1
	`
	if input.ConversationID != nil {
		const conversationQuery = `
			SELECT id, status
			FROM conversations
			WHERE id = $1 AND owner_user_id = $2
			FOR UPDATE
		`
		var linkedConversationID int64
		var conversationStatus string
		if err := tx.QueryRow(
			ctx,
			conversationQuery,
			*input.ConversationID,
			scope.OwnerUserID(),
		).Scan(&linkedConversationID, &conversationStatus); errors.Is(err, pgx.ErrNoRows) {
			return answerapplication.Job{}, conversationdomain.ErrNotFound
		} else if err != nil {
			return answerapplication.Job{}, fmt.Errorf("lock scoped conversation before answer job creation: %w", err)
		}
		switch conversationdomain.Status(conversationStatus) {
		case conversationdomain.StatusArchived:
			return answerapplication.Job{}, conversationdomain.ErrArchived
		case conversationdomain.StatusActive:
			// 继续验证消息关联。
		default:
			return answerapplication.Job{}, fmt.Errorf("conversation %d has invalid status %q", linkedConversationID, conversationStatus)
		}

		const linkQuery = `
			SELECT assistant_message.status
			FROM conversation_messages AS user_message
			JOIN conversation_messages AS assistant_message
			  ON assistant_message.id = $3
			 AND assistant_message.conversation_id = $1
			 AND assistant_message.role = 'assistant'
			WHERE user_message.id = $2
			  AND user_message.conversation_id = $1
			  AND user_message.role = 'user'
			FOR UPDATE OF assistant_message
		`
		var assistantStatus string
		if err := tx.QueryRow(
			ctx,
			linkQuery,
			linkedConversationID,
			*input.UserMessageID,
			*input.AssistantMessageID,
		).Scan(&assistantStatus); errors.Is(err, pgx.ErrNoRows) {
			return answerapplication.Job{}, documentdomain.ErrNotFound
		} else if err != nil {
			return answerapplication.Job{}, fmt.Errorf("validate conversation answer links: %w", err)
		}

		existing, existingErr := scanAnswerJob(tx.QueryRow(
			ctx,
			selectByUserMessageQuery,
			*input.UserMessageID,
			scope.OwnerUserID(),
		))
		if existingErr == nil {
			if err := tx.Commit(ctx); err != nil {
				return answerapplication.Job{}, fmt.Errorf("commit idempotent answer job lookup: %w", err)
			}
			return existing, nil
		}
		if !errors.Is(existingErr, pgx.ErrNoRows) {
			return answerapplication.Job{}, fmt.Errorf("find answer job before creation: %w", existingErr)
		}

		var activeAgentRunExists bool
		if err := tx.QueryRow(
			ctx,
			`SELECT EXISTS (
				SELECT 1
				FROM agent_runs
				WHERE conversation_id = $1
				  AND status IN ('queued', 'processing')
			)`,
			linkedConversationID,
		).Scan(&activeAgentRunExists); err != nil {
			return answerapplication.Job{}, fmt.Errorf("check active agent run before answer job creation: %w", err)
		}
		if activeAgentRunExists {
			return answerapplication.Job{}, answerapplication.ErrConversationAnswerActive
		}

		switch conversationdomain.MessageStatus(assistantStatus) {
		case conversationdomain.MessageStatusGenerating:
			// 新 turn 已经处于可入队状态。
		case conversationdomain.MessageStatusFailed, conversationdomain.MessageStatusInterrupted:
			commandTag, err := tx.Exec(
				ctx,
				`UPDATE conversation_messages
				 SET content = $2, status = 'generating', updated_at = CURRENT_TIMESTAMP
				 WHERE id = $1 AND status IN ('failed', 'interrupted')`,
				*input.AssistantMessageID,
				conversationdomain.AssistantGeneratingPlaceholder,
			)
			if err != nil {
				return answerapplication.Job{}, fmt.Errorf("rearm assistant message before answer job creation: %w", err)
			}
			if commandTag.RowsAffected() != 1 {
				return answerapplication.Job{}, fmt.Errorf("assistant message %d changed before answer job creation", *input.AssistantMessageID)
			}
		case conversationdomain.MessageStatusCompleted:
			return answerapplication.Job{}, fmt.Errorf("completed assistant message %d has no answer job", *input.AssistantMessageID)
		default:
			return answerapplication.Job{}, fmt.Errorf("assistant message %d has invalid status %q", *input.AssistantMessageID, assistantStatus)
		}
	}

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
			requested_document_id,
			conversation_id,
			user_message_id,
			assistant_message_id,
			query,
			top_k,
			requested_response_language
		)
		VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9)
		ON CONFLICT DO NOTHING
		RETURNING
			id, owner_user_id, requested_document_id, query, top_k,
			requested_response_language, status, attempt_count,
			error_code, error_message, next_attempt_at,
			answer_text, resolved_response_language, sources,
			prompt_tokens, completion_tokens, total_tokens,
			created_at, updated_at, started_at, completed_at,
			conversation_id, user_message_id, assistant_message_id
	`
	job, err := scanAnswerJob(tx.QueryRow(
		ctx,
		insertQuery,
		scope.OwnerUserID(),
		input.DocumentID,
		input.DocumentID,
		input.ConversationID,
		input.UserMessageID,
		input.AssistantMessageID,
		input.Query,
		input.TopK,
		input.ResponseLanguage,
	))
	if errors.Is(err, pgx.ErrNoRows) && input.UserMessageID != nil {
		existing, existingErr := scanAnswerJob(tx.QueryRow(
			ctx,
			selectByUserMessageQuery,
			*input.UserMessageID,
			scope.OwnerUserID(),
		))
		if existingErr == nil {
			if err := tx.Commit(ctx); err != nil {
				return answerapplication.Job{}, fmt.Errorf("commit concurrent answer job lookup: %w", err)
			}
			return existing, nil
		}
		if !errors.Is(existingErr, pgx.ErrNoRows) {
			return answerapplication.Job{}, fmt.Errorf("resolve concurrent answer job creation: %w", existingErr)
		}
		return answerapplication.Job{}, answerapplication.ErrConversationAnswerActive
	}
	if errors.Is(err, pgx.ErrNoRows) {
		return answerapplication.Job{}, fmt.Errorf("answer job insert conflicted without a conversation link")
	}
	if isForeignKeyViolation(err) {
		return answerapplication.Job{}, documentdomain.ErrNotFound
	}
	if isConstraintViolation(err, "uq_answer_jobs_active_conversation") {
		return answerapplication.Job{}, answerapplication.ErrConversationAnswerActive
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
			id, owner_user_id, requested_document_id, query, top_k,
			requested_response_language, status, attempt_count,
			error_code, error_message, next_attempt_at,
			answer_text, resolved_response_language, sources,
			prompt_tokens, completion_tokens, total_tokens,
			created_at, updated_at, started_at, completed_at,
			conversation_id, user_message_id, assistant_message_id
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

// GetAnswerJobByUserMessageID 返回和会话用户消息关联的回答任务。
func (r *AnswerJobRepository) GetAnswerJobByUserMessageID(
	ctx context.Context,
	scope accessdomain.OwnerScope,
	userMessageID int64,
) (answerapplication.Job, error) {
	if !scope.IsValid() {
		return answerapplication.Job{}, accessdomain.ErrInvalidOwnerScope
	}
	if userMessageID <= 0 {
		return answerapplication.Job{}, answerapplication.ErrAnswerJobNotFound
	}

	const query = `
		SELECT
			id, owner_user_id, requested_document_id, query, top_k,
			requested_response_language, status, attempt_count,
			error_code, error_message, next_attempt_at,
			answer_text, resolved_response_language, sources,
			prompt_tokens, completion_tokens, total_tokens,
			created_at, updated_at, started_at, completed_at,
			conversation_id, user_message_id, assistant_message_id
		FROM answer_jobs
		WHERE user_message_id = $1 AND owner_user_id = $2
		ORDER BY id DESC
		LIMIT 1
	`
	job, err := scanAnswerJob(r.pool.QueryRow(ctx, query, userMessageID, scope.OwnerUserID()))
	if errors.Is(err, pgx.ErrNoRows) {
		return answerapplication.Job{}, answerapplication.ErrAnswerJobNotFound
	}
	if err != nil {
		return answerapplication.Job{}, fmt.Errorf("get answer job by user message: %w", err)
	}
	return job, nil
}

// GetActiveAnswerJobByConversationID 返回当前 OwnerScope 会话唯一的活跃回答任务。
// 会话行的 KEY SHARE 锁让“会话存在”与后续任务查询共享同一事务快照边界，
// 同时允许普通会话更新继续进行。
func (r *AnswerJobRepository) GetActiveAnswerJobByConversationID(
	ctx context.Context,
	scope accessdomain.OwnerScope,
	conversationID int64,
) (answerapplication.Job, error) {
	if !scope.IsValid() {
		return answerapplication.Job{}, accessdomain.ErrInvalidOwnerScope
	}
	if conversationID <= 0 {
		return answerapplication.Job{}, conversationdomain.ErrNotFound
	}

	tx, err := r.pool.Begin(ctx)
	if err != nil {
		return answerapplication.Job{}, fmt.Errorf("begin active conversation answer lookup: %w", err)
	}
	defer func() { _ = tx.Rollback(context.Background()) }()

	var scopedConversationID int64
	if err := tx.QueryRow(
		ctx,
		`SELECT id
		 FROM conversations
		 WHERE id = $1 AND owner_user_id = $2
		 FOR KEY SHARE`,
		conversationID,
		scope.OwnerUserID(),
	).Scan(&scopedConversationID); errors.Is(err, pgx.ErrNoRows) {
		return answerapplication.Job{}, conversationdomain.ErrNotFound
	} else if err != nil {
		return answerapplication.Job{}, fmt.Errorf("lock scoped conversation for active answer lookup: %w", err)
	}

	const query = `
		SELECT
			id, owner_user_id, requested_document_id, query, top_k,
			requested_response_language, status, attempt_count,
			error_code, error_message, next_attempt_at,
			answer_text, resolved_response_language, sources,
			prompt_tokens, completion_tokens, total_tokens,
			created_at, updated_at, started_at, completed_at,
			conversation_id, user_message_id, assistant_message_id
		FROM answer_jobs
		WHERE conversation_id = $1
		  AND owner_user_id = $2
		  AND status IN ('queued', 'processing')
		ORDER BY id DESC
		LIMIT 1
	`
	job, err := scanAnswerJob(tx.QueryRow(ctx, query, scopedConversationID, scope.OwnerUserID()))
	if errors.Is(err, pgx.ErrNoRows) {
		if commitErr := tx.Commit(ctx); commitErr != nil {
			return answerapplication.Job{}, fmt.Errorf("commit empty active conversation answer lookup: %w", commitErr)
		}
		return answerapplication.Job{}, answerapplication.ErrAnswerJobNotFound
	}
	if err != nil {
		return answerapplication.Job{}, fmt.Errorf("get active scoped conversation answer job: %w", err)
	}
	if err := tx.Commit(ctx); err != nil {
		return answerapplication.Job{}, fmt.Errorf("commit active conversation answer lookup: %w", err)
	}
	return job, nil
}

// GetLatestAnswerJobByConversationID 返回当前 OwnerScope 会话保留期内最新的任务。
func (r *AnswerJobRepository) GetLatestAnswerJobByConversationID(
	ctx context.Context,
	scope accessdomain.OwnerScope,
	conversationID int64,
) (answerapplication.Job, error) {
	if !scope.IsValid() {
		return answerapplication.Job{}, accessdomain.ErrInvalidOwnerScope
	}
	if conversationID <= 0 {
		return answerapplication.Job{}, conversationdomain.ErrNotFound
	}

	tx, err := r.pool.Begin(ctx)
	if err != nil {
		return answerapplication.Job{}, fmt.Errorf("begin latest conversation answer lookup: %w", err)
	}
	defer func() { _ = tx.Rollback(context.Background()) }()

	var scopedConversationID int64
	if err := tx.QueryRow(
		ctx,
		`SELECT id
		 FROM conversations
		 WHERE id = $1 AND owner_user_id = $2
		 FOR KEY SHARE`,
		conversationID,
		scope.OwnerUserID(),
	).Scan(&scopedConversationID); errors.Is(err, pgx.ErrNoRows) {
		return answerapplication.Job{}, conversationdomain.ErrNotFound
	} else if err != nil {
		return answerapplication.Job{}, fmt.Errorf("lock scoped conversation for latest answer lookup: %w", err)
	}

	const query = `
		SELECT
			id, owner_user_id, requested_document_id, query, top_k,
			requested_response_language, status, attempt_count,
			error_code, error_message, next_attempt_at,
			answer_text, resolved_response_language, sources,
			prompt_tokens, completion_tokens, total_tokens,
			created_at, updated_at, started_at, completed_at,
			conversation_id, user_message_id, assistant_message_id
		FROM answer_jobs
		WHERE conversation_id = $1
		  AND owner_user_id = $2
		ORDER BY id DESC
		LIMIT 1
	`
	job, err := scanAnswerJob(tx.QueryRow(ctx, query, scopedConversationID, scope.OwnerUserID()))
	if errors.Is(err, pgx.ErrNoRows) {
		if commitErr := tx.Commit(ctx); commitErr != nil {
			return answerapplication.Job{}, fmt.Errorf("commit empty latest conversation answer lookup: %w", commitErr)
		}
		return answerapplication.Job{}, answerapplication.ErrAnswerJobNotFound
	}
	if err != nil {
		return answerapplication.Job{}, fmt.Errorf("get latest scoped conversation answer job: %w", err)
	}
	if err := tx.Commit(ctx); err != nil {
		return answerapplication.Job{}, fmt.Errorf("commit latest conversation answer lookup: %w", err)
	}
	return job, nil
}

// ListAnswerJobs 按创建时间倒序返回当前 OwnerScope 的 standalone 任务。
func (r *AnswerJobRepository) ListAnswerJobs(
	ctx context.Context,
	scope accessdomain.OwnerScope,
	options answerapplication.JobListOptions,
) (answerapplication.JobListResult, error) {
	if !scope.IsValid() {
		return answerapplication.JobListResult{}, accessdomain.ErrInvalidOwnerScope
	}

	const countQuery = `
		SELECT COUNT(*)
		FROM answer_jobs
		WHERE owner_user_id = $1
		  AND conversation_id IS NULL
	`
	var total int64
	if err := r.pool.QueryRow(ctx, countQuery, scope.OwnerUserID()).Scan(&total); err != nil {
		return answerapplication.JobListResult{}, fmt.Errorf("count scoped answer jobs: %w", err)
	}

	const listQuery = `
		SELECT
			id, owner_user_id, requested_document_id, query, top_k,
			requested_response_language, status, attempt_count,
			error_code, error_message, next_attempt_at,
			answer_text, resolved_response_language, sources,
			prompt_tokens, completion_tokens, total_tokens,
			created_at, updated_at, started_at, completed_at,
			conversation_id, user_message_id, assistant_message_id
		FROM answer_jobs
		WHERE owner_user_id = $1
		  AND conversation_id IS NULL
		ORDER BY created_at DESC, id DESC
		LIMIT $2 OFFSET $3
	`
	rows, err := r.pool.Query(
		ctx,
		listQuery,
		scope.OwnerUserID(),
		options.Limit,
		options.Offset,
	)
	if err != nil {
		return answerapplication.JobListResult{}, fmt.Errorf("query scoped answer jobs: %w", err)
	}
	defer rows.Close()

	jobs := make([]answerapplication.Job, 0)
	for rows.Next() {
		job, err := scanAnswerJob(rows)
		if err != nil {
			return answerapplication.JobListResult{}, fmt.Errorf("scan scoped answer job list: %w", err)
		}
		jobs = append(jobs, job)
	}
	if err := rows.Err(); err != nil {
		return answerapplication.JobListResult{}, fmt.Errorf("iterate scoped answer jobs: %w", err)
	}
	return answerapplication.JobListResult{Jobs: jobs, Total: total}, nil
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
			id, owner_user_id, requested_document_id, query, top_k,
			requested_response_language, status, attempt_count,
			error_code, error_message, next_attempt_at,
			answer_text, resolved_response_language, sources,
			prompt_tokens, completion_tokens, total_tokens,
			created_at, updated_at, started_at, completed_at,
			conversation_id, user_message_id, assistant_message_id
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
			id, owner_user_id, requested_document_id, query, top_k,
			requested_response_language, status, attempt_count,
			error_code, error_message, next_attempt_at,
			answer_text, resolved_response_language, sources,
			prompt_tokens, completion_tokens, total_tokens,
			created_at, updated_at, started_at, completed_at,
			conversation_id, user_message_id, assistant_message_id
	`
	canceled, err := scanAnswerJob(tx.QueryRow(ctx, cancelQuery, job.ID))
	if errors.Is(err, pgx.ErrNoRows) {
		return answerapplication.Job{}, answerapplication.ErrAnswerJobProcessingCannotCancel
	}
	if err != nil {
		return answerapplication.Job{}, fmt.Errorf("cancel answer job: %w", err)
	}
	if job.AssistantMessageID != nil {
		if err := updateAssistantMessage(
			ctx,
			tx,
			*job.AssistantMessageID,
			"回答已取消。",
			conversationdomain.MessageStatusInterrupted,
		); err != nil {
			return answerapplication.Job{}, fmt.Errorf("mark canceled assistant message: %w", err)
		}
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
	if !r.leasePolicy.IsValid() {
		return answerapplication.Job{}, answerapplication.ErrInvalidAnswerJobLeasePolicy
	}

	leaseToken, err := r.newLeaseToken()
	if err != nil {
		return answerapplication.Job{}, fmt.Errorf(
			"create answer job lease token: %w",
			err,
		)
	}

	tx, err := r.pool.BeginTx(ctx, pgx.TxOptions{IsoLevel: pgx.ReadCommitted})
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
			id, owner_user_id, requested_document_id, query, top_k,
			requested_response_language, status, attempt_count,
			error_code, error_message, next_attempt_at,
			answer_text, resolved_response_language, sources,
			prompt_tokens, completion_tokens, total_tokens,
			created_at, updated_at, started_at, completed_at,
			conversation_id, user_message_id, assistant_message_id
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
			completed_at = NULL,
			worker_id = $2,
			lease_token = $3,
			lease_expires_at = CURRENT_TIMESTAMP + ($4::BIGINT * INTERVAL '1 millisecond'),
			heartbeat_at = CURRENT_TIMESTAMP
		WHERE id = $1 AND status = 'queued'
		RETURNING
			id, owner_user_id, requested_document_id, query, top_k,
			requested_response_language, status, attempt_count,
			error_code, error_message, next_attempt_at,
			answer_text, resolved_response_language, sources,
			prompt_tokens, completion_tokens, total_tokens,
			created_at, updated_at, started_at, completed_at,
			conversation_id, user_message_id, assistant_message_id,
			lease_token
	`
	claimed, err := scanClaimedAnswerJob(tx.QueryRow(
		ctx,
		claimQuery,
		queued.ID,
		r.leasePolicy.WorkerID,
		leaseToken,
		r.leasePolicy.LeaseDuration.Milliseconds(),
	))
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

// DeleteExpiredStandaloneAnswerJobs 原子删除一批超过保留期的 standalone 终态任务。
// 会话关联任务随 conversation 生命周期保留，并只通过会话级联删除。
// SKIP LOCKED 允许未来多个后端副本并行维护，且不会等待正在被其他事务处理的行。
func (r *AnswerJobRepository) DeleteExpiredStandaloneAnswerJobs(
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
			  AND conversation_id IS NULL
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
		return 0, fmt.Errorf("delete expired standalone answer jobs: %w", err)
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
	const recheck = `SELECT count(*) < $2 FROM answer_jobs
		WHERE owner_user_id = $1 AND status = 'processing'`
	return selectLockedJobOwner(ctx, tx, query, recheck, maxInFlight, starvationThreshold)
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
	leaseToken string,
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

	tx, err := r.pool.Begin(ctx)
	if err != nil {
		return fmt.Errorf("begin answer job success: %w", err)
	}
	defer func() { _ = tx.Rollback(context.Background()) }()

	var ownerUserID int64
	var conversationID *int64
	if err := tx.QueryRow(
		ctx,
		`SELECT owner_user_id, conversation_id
		 FROM answer_jobs
		 WHERE id = $1
		   AND status = 'processing'
		   AND lease_token = $2
		   AND lease_expires_at > CURRENT_TIMESTAMP`,
		jobID,
		leaseToken,
	).Scan(&ownerUserID, &conversationID); errors.Is(err, pgx.ErrNoRows) {
		return answerapplication.ErrAnswerJobLeaseLost
	} else if err != nil {
		return fmt.Errorf("read answer job scope before success: %w", err)
	}
	if conversationID == nil {
		if err := lockStandaloneAnswerSourceDocuments(ctx, tx, ownerUserID, output.Sources); err != nil {
			return err
		}
	}

	var lockedOwnerUserID int64
	var lockedConversationID *int64
	var assistantMessageID *int64
	if err := tx.QueryRow(
		ctx,
		`SELECT owner_user_id, conversation_id, assistant_message_id
		 FROM answer_jobs
		 WHERE id = $1
		   AND status = 'processing'
		   AND lease_token = $2
		   AND lease_expires_at > CURRENT_TIMESTAMP
		 FOR UPDATE`,
		jobID,
		leaseToken,
	).Scan(&lockedOwnerUserID, &lockedConversationID, &assistantMessageID); errors.Is(err, pgx.ErrNoRows) {
		return answerapplication.ErrAnswerJobLeaseLost
	} else if err != nil {
		return fmt.Errorf("lock answer job for success: %w", err)
	}
	if lockedOwnerUserID != ownerUserID || (lockedConversationID == nil) != (conversationID == nil) ||
		(lockedConversationID != nil && *lockedConversationID != *conversationID) {
		return answerapplication.ErrAnswerJobLeaseLost
	}
	if assistantMessageID != nil {
		if err := updateAssistantMessage(
			ctx,
			tx,
			*assistantMessageID,
			output.Answer,
			conversationdomain.MessageStatusCompleted,
		); err != nil {
			return fmt.Errorf("complete assistant message: %w", err)
		}
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
			completed_at = CURRENT_TIMESTAMP,
			worker_id = NULL,
			lease_token = NULL,
			lease_expires_at = NULL,
			heartbeat_at = NULL
		WHERE id = $1
		  AND status = 'processing'
		  AND lease_token = $8
		  AND lease_expires_at > CURRENT_TIMESTAMP
	`
	commandTag, err := tx.Exec(
		ctx,
		query,
		jobID,
		output.Answer,
		output.ResponseLanguage,
		sourcesJSON,
		output.PromptTokens,
		output.CompletionTokens,
		output.TotalTokens,
		leaseToken,
	)
	if err != nil {
		return fmt.Errorf("mark answer job succeeded: %w", err)
	}
	if commandTag.RowsAffected() != 1 {
		return answerapplication.ErrAnswerJobLeaseLost
	}
	if err := tx.Commit(ctx); err != nil {
		return fmt.Errorf("commit answer job success: %w", err)
	}
	return nil
}

func lockStandaloneAnswerSourceDocuments(
	ctx context.Context,
	tx pgx.Tx,
	ownerUserID int64,
	sources []answerapplication.Source,
) error {
	unique := make(map[int64]struct{}, len(sources))
	for _, source := range sources {
		if source.DocumentID <= 0 {
			return answerapplication.ErrAnswerJobSourceDocumentsUnavailable
		}
		unique[source.DocumentID] = struct{}{}
	}
	if len(unique) == 0 {
		return nil
	}
	documentIDs := make([]int64, 0, len(unique))
	for documentID := range unique {
		documentIDs = append(documentIDs, documentID)
	}
	sort.Slice(documentIDs, func(left, right int) bool { return documentIDs[left] < documentIDs[right] })
	rows, err := tx.Query(
		ctx,
		`SELECT id
		 FROM documents
		 WHERE owner_user_id = $1
		   AND id = ANY($2::BIGINT[])
		 ORDER BY id ASC
		 FOR KEY SHARE`,
		ownerUserID,
		documentIDs,
	)
	if err != nil {
		return fmt.Errorf("lock standalone answer source documents: %w", err)
	}
	lockedCount := 0
	for rows.Next() {
		var documentID int64
		if err := rows.Scan(&documentID); err != nil {
			rows.Close()
			return fmt.Errorf("scan standalone answer source document: %w", err)
		}
		lockedCount++
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return fmt.Errorf("iterate standalone answer source documents: %w", err)
	}
	if lockedCount != len(documentIDs) {
		return answerapplication.ErrAnswerJobSourceDocumentsUnavailable
	}
	return nil
}

// RequeueAnswerJob 把暂时不可执行的 processing 任务延后重试。
func (r *AnswerJobRepository) RequeueAnswerJob(
	ctx context.Context,
	jobID int64,
	leaseToken string,
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
			completed_at = NULL,
			worker_id = NULL,
			lease_token = NULL,
			lease_expires_at = NULL,
			heartbeat_at = NULL
		WHERE id = $1
		  AND status = 'processing'
		  AND lease_token = $5
		  AND lease_expires_at > CURRENT_TIMESTAMP
	`
	commandTag, err := r.pool.Exec(
		ctx,
		query,
		jobID,
		nextAttemptAt,
		errorCode,
		errorMessage,
		leaseToken,
	)
	if err != nil {
		return fmt.Errorf("requeue answer job: %w", err)
	}
	if commandTag.RowsAffected() != 1 {
		return answerapplication.ErrAnswerJobLeaseLost
	}
	return nil
}

// MarkAnswerJobFailed 保存安全失败分类，不暴露底层诊断错误。
func (r *AnswerJobRepository) MarkAnswerJobFailed(
	ctx context.Context,
	jobID int64,
	leaseToken string,
	errorCode answerapplication.JobErrorCode,
	errorMessage string,
) error {
	tx, err := r.pool.Begin(ctx)
	if err != nil {
		return fmt.Errorf("begin answer job failure: %w", err)
	}
	defer func() { _ = tx.Rollback(context.Background()) }()

	var assistantMessageID *int64
	if err := tx.QueryRow(
		ctx,
		`SELECT assistant_message_id
		 FROM answer_jobs
		 WHERE id = $1
		   AND status = 'processing'
		   AND lease_token = $2
		   AND lease_expires_at > CURRENT_TIMESTAMP
		 FOR UPDATE`,
		jobID,
		leaseToken,
	).Scan(&assistantMessageID); errors.Is(err, pgx.ErrNoRows) {
		return answerapplication.ErrAnswerJobLeaseLost
	} else if err != nil {
		return fmt.Errorf("lock answer job for failure: %w", err)
	}
	if assistantMessageID != nil {
		if err := updateAssistantMessage(
			ctx,
			tx,
			*assistantMessageID,
			"暂时无法生成回答，请稍后重试。",
			conversationdomain.MessageStatusFailed,
		); err != nil {
			return fmt.Errorf("fail assistant message: %w", err)
		}
	}

	const query = `
		UPDATE answer_jobs
		SET
			status = 'failed',
			error_code = $2,
			error_message = $3,
			updated_at = CURRENT_TIMESTAMP,
			completed_at = CURRENT_TIMESTAMP,
			worker_id = NULL,
			lease_token = NULL,
			lease_expires_at = NULL,
			heartbeat_at = NULL
		WHERE id = $1
		  AND status = 'processing'
		  AND lease_token = $4
		  AND lease_expires_at > CURRENT_TIMESTAMP
	`
	commandTag, err := tx.Exec(
		ctx,
		query,
		jobID,
		errorCode,
		errorMessage,
		leaseToken,
	)
	if err != nil {
		return fmt.Errorf("mark answer job failed: %w", err)
	}
	if commandTag.RowsAffected() != 1 {
		return answerapplication.ErrAnswerJobLeaseLost
	}
	if err := tx.Commit(ctx); err != nil {
		return fmt.Errorf("commit answer job failure: %w", err)
	}
	return nil
}

func updateAssistantMessage(
	ctx context.Context,
	tx pgx.Tx,
	messageID int64,
	content string,
	status conversationdomain.MessageStatus,
) error {
	commandTag, err := tx.Exec(
		ctx,
		`UPDATE conversation_messages
		 SET content = $2, status = $3, updated_at = CURRENT_TIMESTAMP
		 WHERE id = $1 AND role = 'assistant'`,
		messageID,
		content,
		string(status),
	)
	if err != nil {
		return err
	}
	if commandTag.RowsAffected() != 1 {
		return fmt.Errorf("assistant message %d not found", messageID)
	}
	return nil
}

// RequeueExpiredAnswerJobs 只恢复租约已经到期（或升级前没有租约）的
// processing 任务。仍在其他进程心跳的任务不会进入恢复范围。
func (r *AnswerJobRepository) RequeueExpiredAnswerJobs(
	ctx context.Context,
	errorCode answerapplication.JobErrorCode,
	errorMessage string,
) (int64, error) {
	const query = `
		WITH expired_jobs AS (
			SELECT id
			FROM answer_jobs
			WHERE status = 'processing'
			  AND (
				lease_expires_at IS NULL
				OR lease_expires_at <= CURRENT_TIMESTAMP
			  )
			FOR UPDATE SKIP LOCKED
		)
		UPDATE answer_jobs AS job
		SET
			status = 'queued',
			error_code = $1,
			error_message = $2,
			next_attempt_at = CURRENT_TIMESTAMP,
			updated_at = CURRENT_TIMESTAMP,
			started_at = NULL,
			completed_at = NULL,
			worker_id = NULL,
			lease_token = NULL,
			lease_expires_at = NULL,
			heartbeat_at = NULL
		FROM expired_jobs AS expired
		WHERE job.id = expired.id
		  AND job.status = 'processing'
	`
	commandTag, err := r.pool.Exec(ctx, query, errorCode, errorMessage)
	if err != nil {
		return 0, fmt.Errorf("requeue expired answer jobs: %w", err)
	}
	return commandTag.RowsAffected(), nil
}

// RenewAnswerJobLease 只允许仍持有有效 fencing token 的 Worker 续租。
// 已经过期的租约不能复活，否则可能与接管任务的新 Worker 同时写答案。
func (r *AnswerJobRepository) RenewAnswerJobLease(
	ctx context.Context,
	jobID int64,
	leaseToken string,
) error {
	if !r.leasePolicy.IsValid() {
		return answerapplication.ErrInvalidAnswerJobLeasePolicy
	}

	const query = `
		UPDATE answer_jobs
		SET
			lease_expires_at = CURRENT_TIMESTAMP + ($3::BIGINT * INTERVAL '1 millisecond'),
			heartbeat_at = CURRENT_TIMESTAMP,
			updated_at = CURRENT_TIMESTAMP
		WHERE id = $1
		  AND status = 'processing'
		  AND lease_token = $2
		  AND lease_expires_at > CURRENT_TIMESTAMP
	`
	commandTag, err := r.pool.Exec(
		ctx,
		query,
		jobID,
		leaseToken,
		r.leasePolicy.LeaseDuration.Milliseconds(),
	)
	if err != nil {
		return fmt.Errorf("renew answer job lease: %w", err)
	}
	if commandTag.RowsAffected() != 1 {
		return answerapplication.ErrAnswerJobLeaseLost
	}
	return nil
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
	var conversationID *int64
	var userMessageID *int64
	var assistantMessageID *int64

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
		&conversationID,
		&userMessageID,
		&assistantMessageID,
	)
	if err != nil {
		return answerapplication.Job{}, err
	}

	job.RequestedResponseLanguage = answerapplication.ResponseLanguage(requestedLanguage)
	job.Status = answerapplication.JobStatus(status)
	job.ConversationID = conversationID
	job.UserMessageID = userMessageID
	job.AssistantMessageID = assistantMessageID
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

// scanClaimedAnswerJob 比普通用户查询多读取内部 lease_token。
func scanClaimedAnswerJob(row pgx.Row) (answerapplication.Job, error) {
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
	var conversationID *int64
	var userMessageID *int64
	var assistantMessageID *int64

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
		&conversationID,
		&userMessageID,
		&assistantMessageID,
		&job.LeaseToken,
	)
	if err != nil {
		return answerapplication.Job{}, err
	}

	job.RequestedResponseLanguage = answerapplication.ResponseLanguage(requestedLanguage)
	job.Status = answerapplication.JobStatus(status)
	job.ConversationID = conversationID
	job.UserMessageID = userMessageID
	job.AssistantMessageID = assistantMessageID
	if errorCode != nil {
		job.ErrorCode = answerapplication.JobErrorCode(*errorCode)
	}
	if !job.Status.IsValid() {
		return answerapplication.Job{}, fmt.Errorf("invalid answer job status %q", status)
	}
	if job.LeaseToken == "" {
		return answerapplication.Job{}, errors.New(
			"claimed answer job has empty lease token",
		)
	}
	return job, nil
}
