package postgres

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	agentapplication "rag-reasoning-platform/backend/internal/application/agent"
	accessdomain "rag-reasoning-platform/backend/internal/domain/access"
	agentdomain "rag-reasoning-platform/backend/internal/domain/agent"
	conversationdomain "rag-reasoning-platform/backend/internal/domain/conversation"
	documentdomain "rag-reasoning-platform/backend/internal/domain/document"
)

const (
	agentAdmissionAdvisoryLockID int64 = 0x5241474147454E54
	agentCanceledMessage               = "Agent 执行已取消。"
	agentFailedMessage                 = "暂时无法完成 Agent 执行，请稍后重试。"
	maxAgentErrorMessageBytes          = 1000
	maxAgentRunResultBytes             = 32768
)

var defaultAgentLeasePolicy = agentdomain.LeasePolicy{
	WorkerID:      "agent-worker",
	LeaseDuration: time.Minute,
	MaxAttempts:   agentdomain.MaxAutomaticAttempts,
}

// AgentRepository owns the durable Agent Run queue, steps and owner-scoped reads.
type AgentRepository struct {
	pool             *pgxpool.Pool
	admissionLimits  agentdomain.AdmissionLimits
	schedulingPolicy agentdomain.SchedulingPolicy
	leasePolicy      agentdomain.LeasePolicy
	newLeaseToken    func() (string, error)
}

var _ agentapplication.ScopedRepository = (*AgentRepository)(nil)
var _ agentapplication.WorkerRepository = (*AgentRepository)(nil)
var _ agentapplication.StepRepository = (*AgentRepository)(nil)

func NewAgentRepository(
	pool *pgxpool.Pool,
	admissionLimits agentdomain.AdmissionLimits,
	schedulingPolicy agentdomain.SchedulingPolicy,
) *AgentRepository {
	return NewAgentRepositoryWithPolicies(
		pool,
		admissionLimits,
		schedulingPolicy,
		defaultAgentLeasePolicy,
	)
}

func NewAgentRepositoryWithPolicies(
	pool *pgxpool.Pool,
	admissionLimits agentdomain.AdmissionLimits,
	schedulingPolicy agentdomain.SchedulingPolicy,
	leasePolicy agentdomain.LeasePolicy,
) *AgentRepository {
	return &AgentRepository{
		pool:             pool,
		admissionLimits:  admissionLimits,
		schedulingPolicy: schedulingPolicy,
		leasePolicy:      leasePolicy,
		newLeaseToken:    newAgentLeaseToken,
	}
}

func newAgentLeaseToken() (string, error) {
	randomBytes := make([]byte, 32)
	if _, err := rand.Read(randomBytes); err != nil {
		return "", fmt.Errorf("read agent run lease randomness: %w", err)
	}
	return hex.EncodeToString(randomBytes), nil
}

// CreateRun atomically creates the conversation turn and its queued Agent Run.
func (r *AgentRepository) CreateRun(
	ctx context.Context,
	scope accessdomain.OwnerScope,
	input agentapplication.CreateRunInput,
) (agentapplication.QueueResult, error) {
	if !scope.IsValid() {
		return agentapplication.QueueResult{}, accessdomain.ErrInvalidOwnerScope
	}
	if !r.admissionLimits.IsValid() {
		return agentapplication.QueueResult{}, agentapplication.ErrInvalidAdmissionLimits
	}
	if input.ConversationID <= 0 || strings.TrimSpace(input.Content) == "" ||
		strings.TrimSpace(input.ClientRunID) == "" || !input.ResponseLanguage.IsValid() ||
		!input.Budget.IsValid() {
		return agentapplication.QueueResult{}, errors.New("agent run creation input is invalid")
	}

	tx, err := r.pool.Begin(ctx)
	if err != nil {
		return agentapplication.QueueResult{}, fmt.Errorf("begin agent run creation: %w", err)
	}
	defer func() { _ = tx.Rollback(context.Background()) }()

	var conversationID int64
	var conversationStatus string
	if err := tx.QueryRow(
		ctx,
		`SELECT id, status
		 FROM conversations
		 WHERE id = $1 AND owner_user_id = $2
		 FOR UPDATE`,
		input.ConversationID,
		scope.OwnerUserID(),
	).Scan(&conversationID, &conversationStatus); errors.Is(err, pgx.ErrNoRows) {
		return agentapplication.QueueResult{}, conversationdomain.ErrNotFound
	} else if err != nil {
		return agentapplication.QueueResult{}, fmt.Errorf("lock scoped conversation before agent run creation: %w", err)
	}
	switch conversationdomain.Status(conversationStatus) {
	case conversationdomain.StatusArchived:
		return agentapplication.QueueResult{}, conversationdomain.ErrArchived
	case conversationdomain.StatusActive:
	default:
		return agentapplication.QueueResult{}, fmt.Errorf(
			"conversation %d has invalid status %q",
			conversationID,
			conversationStatus,
		)
	}

	existing, existingErr := scanAgentRun(tx.QueryRow(
		ctx,
		`SELECT `+agentRunSelectColumns+`
		 FROM agent_runs
		 WHERE conversation_id = $1
		   AND client_run_id = $2
		   AND owner_user_id = $3
		 FOR UPDATE`,
		conversationID,
		input.ClientRunID,
		scope.OwnerUserID(),
	))
	if existingErr == nil {
		userMessage, assistantMessage, err := loadAgentRunMessages(ctx, tx, existing)
		if err != nil {
			return agentapplication.QueueResult{}, err
		}
		if userMessage.Content != input.Content ||
			!optionalInt64Equal(existing.RequestedDocumentID, input.RequestedDocumentID) ||
			existing.ResponseLanguage != input.ResponseLanguage {
			return agentapplication.QueueResult{}, agentapplication.ErrIdempotencyConflict
		}
		if err := tx.Commit(ctx); err != nil {
			return agentapplication.QueueResult{}, fmt.Errorf("commit idempotent agent run lookup: %w", err)
		}
		return agentapplication.QueueResult{
			Run: scopedAgentRun(existing), UserMessage: userMessage, AssistantMessage: assistantMessage, Created: false,
		}, nil
	}
	if !errors.Is(existingErr, pgx.ErrNoRows) {
		return agentapplication.QueueResult{}, fmt.Errorf("find idempotent agent run: %w", existingErr)
	}

	var activeExecution bool
	if err := tx.QueryRow(
		ctx,
		`SELECT EXISTS (
			SELECT 1
			FROM answer_jobs
			WHERE conversation_id = $1
			  AND status IN ('queued', 'processing')
			UNION ALL
			SELECT 1
			FROM agent_runs
			WHERE conversation_id = $1
			  AND status IN ('queued', 'processing')
		)`,
		conversationID,
	).Scan(&activeExecution); err != nil {
		return agentapplication.QueueResult{}, fmt.Errorf("check active conversation execution: %w", err)
	}
	if activeExecution {
		return agentapplication.QueueResult{}, agentapplication.ErrConversationExecutionActive
	}

	if input.RequestedDocumentID != nil {
		var documentID int64
		if err := tx.QueryRow(
			ctx,
			`SELECT id
			 FROM documents
			 WHERE id = $1 AND owner_user_id = $2
			 FOR KEY SHARE`,
			*input.RequestedDocumentID,
			scope.OwnerUserID(),
		).Scan(&documentID); errors.Is(err, pgx.ErrNoRows) {
			return agentapplication.QueueResult{}, documentdomain.ErrNotFound
		} else if err != nil {
			return agentapplication.QueueResult{}, fmt.Errorf("lock scoped requested agent document: %w", err)
		}
	}

	if _, err := tx.Exec(ctx, `SELECT pg_advisory_xact_lock($1)`, agentAdmissionAdvisoryLockID); err != nil {
		return agentapplication.QueueResult{}, fmt.Errorf("lock agent run admission: %w", err)
	}
	var ownerQueued int64
	var globalQueued int64
	if err := tx.QueryRow(
		ctx,
		`SELECT COUNT(*) FILTER (WHERE owner_user_id = $1), COUNT(*)
		 FROM agent_runs
		 WHERE status = 'queued'`,
		scope.OwnerUserID(),
	).Scan(&ownerQueued, &globalQueued); err != nil {
		return agentapplication.QueueResult{}, fmt.Errorf("count queued agent runs: %w", err)
	}
	if ownerQueued >= int64(r.admissionLimits.MaxQueuedRunsPerOwner) {
		return agentapplication.QueueResult{}, agentapplication.ErrOwnerQueueCapacity
	}
	if globalQueued >= int64(r.admissionLimits.MaxQueuedRunsGlobal) {
		return agentapplication.QueueResult{}, agentapplication.ErrGlobalQueueCapacity
	}
	if err := ensureAgentOwnerSchedule(ctx, tx, scope.OwnerUserID()); err != nil {
		return agentapplication.QueueResult{}, err
	}

	userMessage, err := insertConversationMessage(
		tx,
		ctx,
		conversationID,
		conversationdomain.RoleUser,
		input.Content,
		conversationdomain.MessageStatusCompleted,
		nil,
	)
	if err != nil {
		return agentapplication.QueueResult{}, fmt.Errorf("insert agent user message: %w", err)
	}
	assistantMessage, err := insertConversationMessage(
		tx,
		ctx,
		conversationID,
		conversationdomain.RoleAssistant,
		conversationdomain.AssistantGeneratingPlaceholder,
		conversationdomain.MessageStatusGenerating,
		nil,
	)
	if err != nil {
		return agentapplication.QueueResult{}, fmt.Errorf("insert agent assistant placeholder: %w", err)
	}

	run, err := scanAgentRun(tx.QueryRow(
		ctx,
		`INSERT INTO agent_runs (
			owner_user_id, conversation_id, user_message_id, assistant_message_id,
			client_run_id, requested_document_id, response_language,
			max_steps, max_total_tokens, deadline_at
		 )
		 VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10)
		 RETURNING `+agentRunSelectColumns,
		scope.OwnerUserID(),
		conversationID,
		userMessage.ID,
		assistantMessage.ID,
		input.ClientRunID,
		input.RequestedDocumentID,
		input.ResponseLanguage,
		input.Budget.MaxSteps,
		input.Budget.MaxTotalTokens,
		input.Budget.DeadlineAt,
	))
	if isConstraintViolation(err, "uq_agent_runs_active_conversation") {
		return agentapplication.QueueResult{}, agentapplication.ErrConversationExecutionActive
	}
	if err != nil {
		return agentapplication.QueueResult{}, fmt.Errorf("insert agent run: %w", err)
	}
	if _, err := tx.Exec(
		ctx,
		`UPDATE conversations
		 SET last_message_at = $2, updated_at = CURRENT_TIMESTAMP
		 WHERE id = $1`,
		conversationID,
		assistantMessage.CreatedAt,
	); err != nil {
		return agentapplication.QueueResult{}, fmt.Errorf("update agent conversation activity: %w", err)
	}
	if err := tx.Commit(ctx); err != nil {
		return agentapplication.QueueResult{}, fmt.Errorf("commit agent run creation: %w", err)
	}
	return agentapplication.QueueResult{
		Run: scopedAgentRun(run), UserMessage: userMessage, AssistantMessage: assistantMessage, Created: true,
	}, nil
}

func (r *AgentRepository) GetRunByID(
	ctx context.Context,
	scope accessdomain.OwnerScope,
	runID int64,
) (agentdomain.Run, error) {
	if !scope.IsValid() {
		return agentdomain.Run{}, accessdomain.ErrInvalidOwnerScope
	}
	run, err := scanAgentRun(r.pool.QueryRow(
		ctx,
		`SELECT `+agentRunSelectColumns+`
		 FROM agent_runs
		 WHERE id = $1 AND owner_user_id = $2`,
		runID,
		scope.OwnerUserID(),
	))
	if errors.Is(err, pgx.ErrNoRows) {
		return agentdomain.Run{}, agentapplication.ErrRunNotFound
	}
	if err != nil {
		return agentdomain.Run{}, fmt.Errorf("get scoped agent run: %w", err)
	}
	return scopedAgentRun(run), nil
}

func (r *AgentRepository) GetActiveRunByConversationID(
	ctx context.Context,
	scope accessdomain.OwnerScope,
	conversationID int64,
) (agentdomain.Run, error) {
	return r.getConversationRun(ctx, scope, conversationID, true)
}

func (r *AgentRepository) GetLatestRunByConversationID(
	ctx context.Context,
	scope accessdomain.OwnerScope,
	conversationID int64,
) (agentdomain.Run, error) {
	return r.getConversationRun(ctx, scope, conversationID, false)
}

func (r *AgentRepository) getConversationRun(
	ctx context.Context,
	scope accessdomain.OwnerScope,
	conversationID int64,
	activeOnly bool,
) (agentdomain.Run, error) {
	if !scope.IsValid() {
		return agentdomain.Run{}, accessdomain.ErrInvalidOwnerScope
	}
	tx, err := r.pool.Begin(ctx)
	if err != nil {
		return agentdomain.Run{}, fmt.Errorf("begin scoped conversation agent lookup: %w", err)
	}
	defer func() { _ = tx.Rollback(context.Background()) }()
	var lockedConversationID int64
	if err := tx.QueryRow(
		ctx,
		`SELECT id FROM conversations
		 WHERE id = $1 AND owner_user_id = $2
		 FOR KEY SHARE`,
		conversationID,
		scope.OwnerUserID(),
	).Scan(&lockedConversationID); errors.Is(err, pgx.ErrNoRows) {
		return agentdomain.Run{}, agentapplication.ErrRunNotFound
	} else if err != nil {
		return agentdomain.Run{}, fmt.Errorf("lock scoped conversation for agent lookup: %w", err)
	}
	statusClause := ""
	if activeOnly {
		statusClause = " AND status IN ('queued', 'processing')"
	}
	run, err := scanAgentRun(tx.QueryRow(
		ctx,
		`SELECT `+agentRunSelectColumns+`
		 FROM agent_runs
		 WHERE conversation_id = $1 AND owner_user_id = $2`+statusClause+`
		 ORDER BY id DESC
		 LIMIT 1`,
		lockedConversationID,
		scope.OwnerUserID(),
	))
	if errors.Is(err, pgx.ErrNoRows) {
		return agentdomain.Run{}, agentapplication.ErrRunNotFound
	}
	if err != nil {
		return agentdomain.Run{}, fmt.Errorf("get conversation agent run: %w", err)
	}
	if err := tx.Commit(ctx); err != nil {
		return agentdomain.Run{}, fmt.Errorf("commit conversation agent lookup: %w", err)
	}
	return scopedAgentRun(run), nil
}

// CancelRun only cancels queued work and closes its assistant placeholder.
func (r *AgentRepository) CancelRun(
	ctx context.Context,
	scope accessdomain.OwnerScope,
	runID int64,
) (agentdomain.Run, error) {
	if !scope.IsValid() {
		return agentdomain.Run{}, accessdomain.ErrInvalidOwnerScope
	}
	tx, err := r.pool.Begin(ctx)
	if err != nil {
		return agentdomain.Run{}, fmt.Errorf("begin agent run cancellation: %w", err)
	}
	defer func() { _ = tx.Rollback(context.Background()) }()
	run, err := scanAgentRun(tx.QueryRow(
		ctx,
		`SELECT `+agentRunSelectColumns+`
		 FROM agent_runs
		 WHERE id = $1 AND owner_user_id = $2
		 FOR UPDATE`,
		runID,
		scope.OwnerUserID(),
	))
	if errors.Is(err, pgx.ErrNoRows) {
		return agentdomain.Run{}, agentapplication.ErrRunNotFound
	}
	if err != nil {
		return agentdomain.Run{}, fmt.Errorf("lock agent run for cancellation: %w", err)
	}
	switch run.Status {
	case agentdomain.RunStatusProcessing:
		return agentdomain.Run{}, agentapplication.ErrRunProcessingCannotCancel
	case agentdomain.RunStatusSucceeded, agentdomain.RunStatusFailed, agentdomain.RunStatusCanceled:
		return agentdomain.Run{}, agentapplication.ErrRunTerminalCannotCancel
	case agentdomain.RunStatusQueued:
	default:
		return agentdomain.Run{}, fmt.Errorf("cancel agent run with invalid status %q", run.Status)
	}
	if err := updateAssistantMessage(
		ctx,
		tx,
		run.AssistantMessageID,
		agentCanceledMessage,
		conversationdomain.MessageStatusInterrupted,
	); err != nil {
		return agentdomain.Run{}, fmt.Errorf("interrupt canceled agent assistant: %w", err)
	}
	canceled, err := scanAgentRun(tx.QueryRow(
		ctx,
		`UPDATE agent_runs
		 SET status = 'canceled',
		     error_code = NULL,
		     error_message = NULL,
		     updated_at = CURRENT_TIMESTAMP,
		     completed_at = CURRENT_TIMESTAMP,
		     worker_id = NULL,
		     lease_token = NULL,
		     lease_expires_at = NULL,
		     heartbeat_at = NULL
		 WHERE id = $1 AND status = 'queued'
		 RETURNING `+agentRunSelectColumns,
		run.ID,
	))
	if errors.Is(err, pgx.ErrNoRows) {
		return agentdomain.Run{}, agentapplication.ErrRunProcessingCannotCancel
	}
	if err != nil {
		return agentdomain.Run{}, fmt.Errorf("cancel agent run: %w", err)
	}
	if err := tx.Commit(ctx); err != nil {
		return agentdomain.Run{}, fmt.Errorf("commit agent run cancellation: %w", err)
	}
	return scopedAgentRun(canceled), nil
}

const agentRunSelectColumns = `
	id, owner_user_id, conversation_id, user_message_id, assistant_message_id,
	client_run_id, requested_document_id, response_language,
	status, step_count, max_steps, max_total_tokens, deadline_at,
	attempt_count, next_attempt_at, worker_id, lease_token, lease_expires_at, heartbeat_at,
	error_code, error_message, result_json,
	prompt_tokens, completion_tokens, total_tokens,
	created_at, updated_at, started_at, completed_at
`

func scanAgentRun(row pgx.Row) (agentdomain.Run, error) {
	var run agentdomain.Run
	var responseLanguage string
	var status string
	var workerID *string
	var leaseToken *string
	var errorCode *string
	var resultJSON []byte
	var promptTokens *int
	var completionTokens *int
	var totalTokens *int
	err := row.Scan(
		&run.ID,
		&run.OwnerUserID,
		&run.ConversationID,
		&run.UserMessageID,
		&run.AssistantMessageID,
		&run.ClientRunID,
		&run.RequestedDocumentID,
		&responseLanguage,
		&status,
		&run.StepCount,
		&run.Budget.MaxSteps,
		&run.Budget.MaxTotalTokens,
		&run.Budget.DeadlineAt,
		&run.AttemptCount,
		&run.NextAttemptAt,
		&workerID,
		&leaseToken,
		&run.LeaseExpiresAt,
		&run.HeartbeatAt,
		&errorCode,
		&run.ErrorMessage,
		&resultJSON,
		&promptTokens,
		&completionTokens,
		&totalTokens,
		&run.CreatedAt,
		&run.UpdatedAt,
		&run.StartedAt,
		&run.CompletedAt,
	)
	if err != nil {
		return agentdomain.Run{}, err
	}
	run.ResponseLanguage = agentdomain.ResponseLanguage(responseLanguage)
	run.Status = agentdomain.RunStatus(status)
	if workerID != nil {
		run.WorkerID = *workerID
	}
	if leaseToken != nil {
		run.LeaseToken = *leaseToken
	}
	if errorCode != nil {
		run.ErrorCode = agentdomain.ErrorCode(*errorCode)
	}
	if totalTokens != nil {
		run.PromptTokens = *promptTokens
		run.CompletionTokens = *completionTokens
		run.TotalTokens = *totalTokens
	}
	if !run.Status.IsValid() || !run.ResponseLanguage.IsValid() || !run.Budget.IsValid() ||
		!run.ErrorCode.IsValid() {
		return agentdomain.Run{}, errors.New("database returned invalid agent run")
	}
	if run.Status == agentdomain.RunStatusSucceeded {
		if len(resultJSON) == 0 || promptTokens == nil || completionTokens == nil || totalTokens == nil {
			return agentdomain.Run{}, errors.New("succeeded agent run is missing result fields")
		}
		var persisted agentapplication.FinalStepResult
		if err := json.Unmarshal(resultJSON, &persisted); err != nil {
			return agentdomain.Run{}, fmt.Errorf("decode agent run result: %w", err)
		}
		run.Result = &agentdomain.Result{
			Answer:           persisted.Answer,
			ResponseLanguage: persisted.ResponseLanguage,
			Payload:          persisted.Payload,
			PromptTokens:     *promptTokens,
			CompletionTokens: *completionTokens,
			TotalTokens:      *totalTokens,
		}
		if !run.Result.IsValid() {
			return agentdomain.Run{}, errors.New("database returned invalid agent run result")
		}
	}
	return run, nil
}

func loadAgentRunMessages(
	ctx context.Context,
	tx pgx.Tx,
	run agentdomain.Run,
) (conversationdomain.Message, conversationdomain.Message, error) {
	const query = `
		SELECT id, conversation_id, sequence_no, role, content, status,
		       client_message_id, created_at, updated_at
		FROM conversation_messages
		WHERE id = $1 AND conversation_id = $2
	`
	userMessage, err := scanConversationMessage(tx.QueryRow(ctx, query, run.UserMessageID, run.ConversationID))
	if err != nil {
		return conversationdomain.Message{}, conversationdomain.Message{}, fmt.Errorf("load agent user message: %w", err)
	}
	assistantMessage, err := scanConversationMessage(tx.QueryRow(ctx, query, run.AssistantMessageID, run.ConversationID))
	if err != nil {
		return conversationdomain.Message{}, conversationdomain.Message{}, fmt.Errorf("load agent assistant message: %w", err)
	}
	if userMessage.Role != conversationdomain.RoleUser ||
		assistantMessage.Role != conversationdomain.RoleAssistant ||
		assistantMessage.SequenceNo != userMessage.SequenceNo+1 {
		return conversationdomain.Message{}, conversationdomain.Message{}, errors.New("agent run messages are inconsistent")
	}
	return userMessage, assistantMessage, nil
}

func optionalInt64Equal(left, right *int64) bool {
	if left == nil || right == nil {
		return left == nil && right == nil
	}
	return *left == *right
}

func ensureAgentOwnerSchedule(ctx context.Context, tx pgx.Tx, ownerID int64) error {
	if _, err := tx.Exec(
		ctx,
		`INSERT INTO agent_owner_schedules (owner_user_id)
		 VALUES ($1)
		 ON CONFLICT (owner_user_id) DO NOTHING`,
		ownerID,
	); err != nil {
		return fmt.Errorf("ensure agent owner schedule: %w", err)
	}
	return nil
}

func validateAgentError(errorCode agentdomain.ErrorCode, errorMessage string) error {
	if errorCode == agentdomain.ErrorCodeNone || !errorCode.IsValid() {
		return errors.New("agent error code is invalid")
	}
	trimmed := strings.TrimSpace(errorMessage)
	if trimmed == "" || trimmed != errorMessage || len(errorMessage) > maxAgentErrorMessageBytes {
		return errors.New("agent error message is invalid")
	}
	return nil
}

func scopedAgentRun(run agentdomain.Run) agentdomain.Run {
	run.WorkerID = ""
	run.LeaseToken = ""
	run.LeaseExpiresAt = nil
	run.HeartbeatAt = nil
	return run
}
