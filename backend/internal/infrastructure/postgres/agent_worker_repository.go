package postgres

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"

	agentapplication "rag-reasoning-platform/backend/internal/application/agent"
	agentdomain "rag-reasoning-platform/backend/internal/domain/agent"
	conversationdomain "rag-reasoning-platform/backend/internal/domain/conversation"
)

// ClaimNextRun atomically leases one eligible Run using owner-fair scheduling.
func (r *AgentRepository) ClaimNextRun(ctx context.Context) (agentdomain.Run, error) {
	if !r.schedulingPolicy.IsValid() {
		return agentdomain.Run{}, agentapplication.ErrInvalidSchedulingPolicy
	}
	if !r.leasePolicy.IsValid() {
		return agentdomain.Run{}, agentapplication.ErrInvalidLeasePolicy
	}
	leaseToken, err := r.newLeaseToken()
	if err != nil {
		return agentdomain.Run{}, fmt.Errorf("create agent run lease token: %w", err)
	}
	tx, err := r.pool.Begin(ctx)
	if err != nil {
		return agentdomain.Run{}, fmt.Errorf("begin agent run claim: %w", err)
	}
	defer func() { _ = tx.Rollback(context.Background()) }()

	ownerID, err := selectNextAgentOwner(
		ctx,
		tx,
		r.schedulingPolicy.MaxInFlightPerOwner,
		r.schedulingPolicy.StarvationThreshold,
		r.leasePolicy.MaxAttempts,
	)
	if errors.Is(err, pgx.ErrNoRows) &&
		r.schedulingPolicy.MaxBorrowedInFlightPerOwner > r.schedulingPolicy.MaxInFlightPerOwner {
		ownerID, err = selectNextAgentOwner(
			ctx,
			tx,
			r.schedulingPolicy.MaxBorrowedInFlightPerOwner,
			r.schedulingPolicy.StarvationThreshold,
			r.leasePolicy.MaxAttempts,
		)
	}
	if errors.Is(err, pgx.ErrNoRows) {
		return agentdomain.Run{}, agentapplication.ErrNoQueuedRun
	}
	if err != nil {
		return agentdomain.Run{}, fmt.Errorf("select next agent owner: %w", err)
	}

	var runID int64
	if err := tx.QueryRow(
		ctx,
		`SELECT id
		 FROM agent_runs
		 WHERE owner_user_id = $1
		   AND status = 'queued'
		   AND attempt_count < $2
		   AND next_attempt_at <= CURRENT_TIMESTAMP
		 ORDER BY next_attempt_at ASC, created_at ASC, id ASC
		 FOR UPDATE SKIP LOCKED
		 LIMIT 1`,
		ownerID,
		r.leasePolicy.MaxAttempts,
	).Scan(&runID); errors.Is(err, pgx.ErrNoRows) {
		return agentdomain.Run{}, agentapplication.ErrNoQueuedRun
	} else if err != nil {
		return agentdomain.Run{}, fmt.Errorf("select queued agent run: %w", err)
	}

	claimed, err := scanAgentRun(tx.QueryRow(
		ctx,
		`UPDATE agent_runs
		 SET status = 'processing',
		     attempt_count = attempt_count + 1,
		     error_code = NULL,
		     error_message = NULL,
		     updated_at = CURRENT_TIMESTAMP,
		     started_at = COALESCE(started_at, CURRENT_TIMESTAMP),
		     completed_at = NULL,
		     worker_id = $2,
		     lease_token = $3,
		     lease_expires_at = CURRENT_TIMESTAMP + ($4::BIGINT * INTERVAL '1 millisecond'),
		     heartbeat_at = CURRENT_TIMESTAMP
		 WHERE id = $1
		   AND status = 'queued'
		   AND attempt_count < $5
		 RETURNING `+agentRunSelectColumns,
		runID,
		r.leasePolicy.WorkerID,
		leaseToken,
		r.leasePolicy.LeaseDuration.Milliseconds(),
		r.leasePolicy.MaxAttempts,
	))
	if errors.Is(err, pgx.ErrNoRows) {
		return agentdomain.Run{}, agentapplication.ErrNoQueuedRun
	}
	if err != nil {
		return agentdomain.Run{}, fmt.Errorf("mark agent run processing: %w", err)
	}
	commandTag, err := tx.Exec(
		ctx,
		`UPDATE agent_owner_schedules
		 SET last_dispatched_at = CURRENT_TIMESTAMP,
		     updated_at = CURRENT_TIMESTAMP
		 WHERE owner_user_id = $1`,
		ownerID,
	)
	if err != nil {
		return agentdomain.Run{}, fmt.Errorf("update agent owner schedule: %w", err)
	}
	if commandTag.RowsAffected() != 1 {
		return agentdomain.Run{}, fmt.Errorf("update agent owner schedule: expected 1 row, got %d", commandTag.RowsAffected())
	}
	if err := tx.Commit(ctx); err != nil {
		return agentdomain.Run{}, fmt.Errorf("commit agent run claim: %w", err)
	}
	return claimed, nil
}

func selectNextAgentOwner(
	ctx context.Context,
	tx pgx.Tx,
	maxInFlight int,
	starvationThreshold time.Duration,
	maxAttempts int,
) (int64, error) {
	const query = `
		SELECT schedule.owner_user_id
		FROM agent_owner_schedules AS schedule
		JOIN LATERAL (
			SELECT MIN(GREATEST(run.created_at, run.next_attempt_at)) AS oldest_eligible_at
			FROM agent_runs AS run
			WHERE run.owner_user_id = schedule.owner_user_id
			  AND run.status = 'queued'
			  AND run.attempt_count < $3
			  AND run.next_attempt_at <= CURRENT_TIMESTAMP
		) AS queued_work ON queued_work.oldest_eligible_at IS NOT NULL
		JOIN LATERAL (
			SELECT COUNT(*) AS in_flight_count
			FROM agent_runs AS run
			WHERE run.owner_user_id = schedule.owner_user_id
			  AND run.status = 'processing'
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
		maxAttempts,
	).Scan(&ownerID)
	return ownerID, err
}

func (r *AgentRepository) GetQueueStats(ctx context.Context) (agentdomain.QueueStats, error) {
	if !r.leasePolicy.IsValid() {
		return agentdomain.QueueStats{}, agentapplication.ErrInvalidLeasePolicy
	}
	const query = `
		SELECT
			COUNT(*) FILTER (WHERE status = 'queued'),
			COUNT(*) FILTER (
				WHERE status = 'queued'
				  AND attempt_count < $1
				  AND next_attempt_at <= CURRENT_TIMESTAMP
			),
			COUNT(*) FILTER (WHERE status = 'processing'),
			COALESCE((
				SELECT MAX(owner_processing_count)
				FROM (
					SELECT COUNT(*) AS owner_processing_count
					FROM agent_runs
					WHERE status = 'processing'
					GROUP BY owner_user_id
				) AS processing_by_owner
			), 0),
			COALESCE((
				EXTRACT(EPOCH FROM (
					CURRENT_TIMESTAMP -
					MIN(GREATEST(created_at, next_attempt_at)) FILTER (
						WHERE status = 'queued'
						  AND attempt_count < $1
						  AND next_attempt_at <= CURRENT_TIMESTAMP
					)
				)) * 1000
			)::BIGINT, 0)
		FROM agent_runs
	`
	var stats agentdomain.QueueStats
	var oldestReadyWaitMilliseconds int64
	if err := r.pool.QueryRow(ctx, query, r.leasePolicy.MaxAttempts).Scan(
		&stats.QueuedCount,
		&stats.ReadyQueuedCount,
		&stats.ProcessingCount,
		&stats.MaxOwnerProcessingCount,
		&oldestReadyWaitMilliseconds,
	); err != nil {
		return agentdomain.QueueStats{}, fmt.Errorf("get agent queue stats: %w", err)
	}
	stats.OldestReadyWait = time.Duration(oldestReadyWaitMilliseconds) * time.Millisecond
	if !stats.IsValid() {
		return agentdomain.QueueStats{}, errors.New("get agent queue stats: database returned invalid counts")
	}
	return stats, nil
}

func (r *AgentRepository) RenewRunLease(ctx context.Context, runID int64, leaseToken string) error {
	if !r.leasePolicy.IsValid() {
		return agentapplication.ErrInvalidLeasePolicy
	}
	commandTag, err := r.pool.Exec(
		ctx,
		`UPDATE agent_runs
		 SET lease_expires_at = CURRENT_TIMESTAMP + ($3::BIGINT * INTERVAL '1 millisecond'),
		     heartbeat_at = CURRENT_TIMESTAMP,
		     updated_at = CURRENT_TIMESTAMP
		 WHERE id = $1
		   AND status = 'processing'
		   AND lease_token = $2
		   AND lease_expires_at > CURRENT_TIMESTAMP`,
		runID,
		leaseToken,
		r.leasePolicy.LeaseDuration.Milliseconds(),
	)
	if err != nil {
		return fmt.Errorf("renew agent run lease: %w", err)
	}
	if commandTag.RowsAffected() != 1 {
		return agentapplication.ErrRunLeaseLost
	}
	return nil
}

// RequeueRun applies the attempt bound and resets the current running Step.
func (r *AgentRepository) RequeueRun(
	ctx context.Context,
	runID int64,
	leaseToken string,
	nextAttemptAt time.Time,
	errorCode agentdomain.ErrorCode,
	errorMessage string,
) error {
	if !r.leasePolicy.IsValid() {
		return agentapplication.ErrInvalidLeasePolicy
	}
	if nextAttemptAt.IsZero() || validateAgentError(errorCode, errorMessage) != nil {
		return errors.New("agent run requeue input is invalid")
	}
	tx, err := r.pool.Begin(ctx)
	if err != nil {
		return fmt.Errorf("begin agent run requeue: %w", err)
	}
	defer func() { _ = tx.Rollback(context.Background()) }()
	run, err := r.lockFencedRun(ctx, tx, runID, leaseToken)
	if err != nil {
		return err
	}
	if run.AttemptCount >= r.leasePolicy.MaxAttempts {
		if err := r.failLockedRun(ctx, tx, run, errorCode, errorMessage); err != nil {
			return err
		}
	} else {
		if err := resetRunningAgentSteps(ctx, tx, run.ID); err != nil {
			return err
		}
		commandTag, err := tx.Exec(
			ctx,
			`UPDATE agent_runs
			 SET status = 'queued',
			     error_code = $3,
			     error_message = $4,
			     next_attempt_at = $2,
			     updated_at = CURRENT_TIMESTAMP,
			     completed_at = NULL,
			     worker_id = NULL,
			     lease_token = NULL,
			     lease_expires_at = NULL,
			     heartbeat_at = NULL
			 WHERE id = $1 AND status = 'processing' AND lease_token = $5`,
			run.ID,
			nextAttemptAt,
			errorCode,
			errorMessage,
			leaseToken,
		)
		if err != nil {
			return fmt.Errorf("requeue agent run: %w", err)
		}
		if commandTag.RowsAffected() != 1 {
			return agentapplication.ErrRunLeaseLost
		}
	}
	if err := tx.Commit(ctx); err != nil {
		return fmt.Errorf("commit agent run requeue: %w", err)
	}
	return nil
}

// CommitFinal atomically persists the final decision, assistant message and Run terminal state.
func (r *AgentRepository) CommitFinal(
	ctx context.Context,
	runID int64,
	leaseToken string,
	input agentapplication.CommitFinalInput,
) (agentdomain.Step, error) {
	if strings.TrimSpace(input.Result.Answer) == "" ||
		!input.Result.ResponseLanguage.IsResolved() ||
		(len(input.Result.Payload) > 0 && !json.Valid(input.Result.Payload)) ||
		!agentdomain.TokenUsageIsValid(input.PromptTokens, input.CompletionTokens, input.TotalTokens) {
		return agentdomain.Step{}, agentapplication.ErrInvalidStep
	}
	persistedJSON, err := json.Marshal(input.Result)
	if err != nil {
		return agentdomain.Step{}, fmt.Errorf("encode agent final decision: %w", err)
	}
	if len(persistedJSON) > maxAgentRunResultBytes {
		return agentdomain.Step{}, agentapplication.ErrInvalidStep
	}
	tx, err := r.pool.Begin(ctx)
	if err != nil {
		return agentdomain.Step{}, fmt.Errorf("begin agent final commit: %w", err)
	}
	defer func() { _ = tx.Rollback(context.Background()) }()
	withinLimit, err := jsonbSizeWithin(ctx, tx, persistedJSON, maxAgentRunResultBytes)
	if err != nil {
		return agentdomain.Step{}, err
	}
	if !withinLimit {
		return agentdomain.Step{}, agentapplication.ErrInvalidStep
	}
	run, err := r.lockFencedRun(ctx, tx, runID, leaseToken)
	if err != nil {
		return agentdomain.Step{}, err
	}
	if run.StepCount >= run.Budget.MaxSteps {
		return agentdomain.Step{}, agentapplication.ErrStepLimitExceeded
	}
	if input.TotalTokens > run.Budget.MaxTotalTokens-run.TotalTokens {
		return agentdomain.Step{}, agentapplication.ErrTokenBudgetExceeded
	}
	deadlineOpen, err := agentDeadlineOpen(ctx, tx, run.Budget.DeadlineAt)
	if err != nil {
		return agentdomain.Step{}, err
	}
	if !deadlineOpen {
		return agentdomain.Step{}, agentapplication.ErrRunDeadlineExceeded
	}
	var finalExists bool
	var activeStep bool
	var failedStep bool
	if err := tx.QueryRow(
		ctx,
		`SELECT
			EXISTS (SELECT 1 FROM agent_steps WHERE agent_run_id = $1 AND kind = 'final'),
			EXISTS (
				SELECT 1 FROM agent_steps
				WHERE agent_run_id = $1 AND status IN ('pending', 'running')
			),
			EXISTS (
				SELECT 1 FROM agent_steps
				WHERE agent_run_id = $1 AND status = 'failed'
			)`,
		run.ID,
	).Scan(&finalExists, &activeStep, &failedStep); err != nil {
		return agentdomain.Step{}, fmt.Errorf("validate final agent boundary: %w", err)
	}
	if finalExists {
		return agentdomain.Step{}, agentapplication.ErrFinalStepAlreadyPersisted
	}
	if activeStep || failedStep {
		return agentdomain.Step{}, agentapplication.ErrInvalidStep
	}
	sequenceNo := run.StepCount + 1
	step, err := scanAgentStep(tx.QueryRow(
		ctx,
		`INSERT INTO agent_steps AS step (
			agent_run_id, sequence_no, kind, status, result_json,
			prompt_tokens, completion_tokens, total_tokens,
			started_at, completed_at
		 )
		 VALUES ($1, $2, 'final', 'succeeded', $3::JSONB, $4, $5, $6,
		         CURRENT_TIMESTAMP, CURRENT_TIMESTAMP)
		 RETURNING `+agentStepReturningColumns,
		run.ID,
		sequenceNo,
		persistedJSON,
		input.PromptTokens,
		input.CompletionTokens,
		input.TotalTokens,
	))
	if err != nil {
		return agentdomain.Step{}, fmt.Errorf("insert final agent step: %w", err)
	}
	if err := updateAssistantMessage(
		ctx,
		tx,
		run.AssistantMessageID,
		input.Result.Answer,
		conversationdomain.MessageStatusCompleted,
	); err != nil {
		return agentdomain.Step{}, fmt.Errorf("complete agent assistant message: %w", err)
	}
	promptTokens := run.PromptTokens + input.PromptTokens
	completionTokens := run.CompletionTokens + input.CompletionTokens
	totalTokens := run.TotalTokens + input.TotalTokens
	commandTag, err := tx.Exec(
		ctx,
		`UPDATE agent_runs
		 SET status = 'succeeded',
		     error_code = NULL,
		     error_message = NULL,
		     result_json = $2::JSONB,
		     step_count = step_count + 1,
		     prompt_tokens = $3,
		     completion_tokens = $4,
		     total_tokens = $5,
		     updated_at = CURRENT_TIMESTAMP,
		     completed_at = CURRENT_TIMESTAMP,
		     worker_id = NULL,
		     lease_token = NULL,
		     lease_expires_at = NULL,
		     heartbeat_at = NULL
		 WHERE id = $1
		   AND status = 'processing'
		   AND lease_token = $6
		   AND lease_expires_at > CURRENT_TIMESTAMP
		   AND step_count = $7
		   AND COALESCE(total_tokens, 0) + $8 <= max_total_tokens`,
		run.ID,
		persistedJSON,
		promptTokens,
		completionTokens,
		totalTokens,
		leaseToken,
		run.StepCount,
		input.TotalTokens,
	)
	if err != nil {
		return agentdomain.Step{}, fmt.Errorf("commit final agent run state: %w", err)
	}
	if commandTag.RowsAffected() != 1 {
		return agentdomain.Step{}, agentapplication.ErrRunLeaseLost
	}
	if err := tx.Commit(ctx); err != nil {
		return agentdomain.Step{}, fmt.Errorf("commit agent final transaction: %w", err)
	}
	return step, nil
}

func (r *AgentRepository) MarkRunFailed(
	ctx context.Context,
	runID int64,
	leaseToken string,
	errorCode agentdomain.ErrorCode,
	errorMessage string,
) error {
	if err := validateAgentError(errorCode, errorMessage); err != nil {
		return err
	}
	tx, err := r.pool.Begin(ctx)
	if err != nil {
		return fmt.Errorf("begin agent run failure: %w", err)
	}
	defer func() { _ = tx.Rollback(context.Background()) }()
	run, err := r.lockFencedRun(ctx, tx, runID, leaseToken)
	if err != nil {
		return err
	}
	if err := r.failLockedRun(ctx, tx, run, errorCode, errorMessage); err != nil {
		return err
	}
	if err := tx.Commit(ctx); err != nil {
		return fmt.Errorf("commit agent run failure: %w", err)
	}
	return nil
}

// RequeueExpiredRuns recovers expired leases and terminally fails attempt two.
func (r *AgentRepository) RequeueExpiredRuns(
	ctx context.Context,
	limit int,
	errorCode agentdomain.ErrorCode,
	errorMessage string,
) (int64, error) {
	if limit <= 0 || limit > agentapplication.MaxRecoveryBatchSize {
		return 0, agentapplication.ErrInvalidRecoveryBatch
	}
	if err := validateAgentError(errorCode, errorMessage); err != nil {
		return 0, err
	}
	if !r.leasePolicy.IsValid() {
		return 0, agentapplication.ErrInvalidLeasePolicy
	}
	tx, err := r.pool.Begin(ctx)
	if err != nil {
		return 0, fmt.Errorf("begin expired agent run recovery: %w", err)
	}
	defer func() { _ = tx.Rollback(context.Background()) }()
	rows, err := tx.Query(
		ctx,
		`SELECT `+agentRunSelectColumns+`
		 FROM agent_runs
		 WHERE status = 'processing'
		   AND lease_expires_at <= CURRENT_TIMESTAMP
		 ORDER BY id
		 FOR UPDATE SKIP LOCKED
		 LIMIT $1`,
		limit,
	)
	if err != nil {
		return 0, fmt.Errorf("select expired agent runs: %w", err)
	}
	expired := make([]agentdomain.Run, 0)
	for rows.Next() {
		run, scanErr := scanAgentRun(rows)
		if scanErr != nil {
			rows.Close()
			return 0, fmt.Errorf("scan expired agent run: %w", scanErr)
		}
		expired = append(expired, run)
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return 0, fmt.Errorf("iterate expired agent runs: %w", err)
	}
	for _, run := range expired {
		if run.AttemptCount >= r.leasePolicy.MaxAttempts {
			if err := r.failLockedRun(ctx, tx, run, errorCode, errorMessage); err != nil {
				return 0, err
			}
			continue
		}
		if err := resetRunningAgentSteps(ctx, tx, run.ID); err != nil {
			return 0, err
		}
		commandTag, err := tx.Exec(
			ctx,
			`UPDATE agent_runs
			 SET status = 'queued',
			     error_code = $2,
			     error_message = $3,
			     next_attempt_at = CURRENT_TIMESTAMP,
			     updated_at = CURRENT_TIMESTAMP,
			     completed_at = NULL,
			     worker_id = NULL,
			     lease_token = NULL,
			     lease_expires_at = NULL,
			     heartbeat_at = NULL
			 WHERE id = $1 AND status = 'processing'`,
			run.ID,
			errorCode,
			errorMessage,
		)
		if err != nil {
			return 0, fmt.Errorf("requeue expired agent run: %w", err)
		}
		if commandTag.RowsAffected() != 1 {
			return 0, agentapplication.ErrRunLeaseLost
		}
	}
	if err := tx.Commit(ctx); err != nil {
		return 0, fmt.Errorf("commit expired agent run recovery: %w", err)
	}
	return int64(len(expired)), nil
}

func (r *AgentRepository) lockFencedRun(
	ctx context.Context,
	tx pgx.Tx,
	runID int64,
	leaseToken string,
) (agentdomain.Run, error) {
	// Lease validity is decided when this transaction acquires the Run row lock.
	// Recovery cannot install a newer token until the lock is released, so a
	// transaction that began while valid may finish without racing a takeover.
	run, err := scanAgentRun(tx.QueryRow(
		ctx,
		`SELECT `+agentRunSelectColumns+`
		 FROM agent_runs
		 WHERE id = $1
		   AND status = 'processing'
		   AND lease_token = $2
		   AND lease_expires_at > CURRENT_TIMESTAMP
		 FOR UPDATE`,
		runID,
		leaseToken,
	))
	if errors.Is(err, pgx.ErrNoRows) {
		return agentdomain.Run{}, agentapplication.ErrRunLeaseLost
	}
	if err != nil {
		return agentdomain.Run{}, fmt.Errorf("lock fenced agent run: %w", err)
	}
	return run, nil
}

func resetRunningAgentSteps(ctx context.Context, tx pgx.Tx, runID int64) error {
	if _, err := tx.Exec(
		ctx,
		`UPDATE agent_steps
		 SET status = 'pending',
		     error_code = NULL,
		     error_message = NULL,
		     result_json = NULL,
		     updated_at = CURRENT_TIMESTAMP,
		     started_at = NULL,
		     completed_at = NULL
		 WHERE agent_run_id = $1 AND status = 'running'`,
		runID,
	); err != nil {
		return fmt.Errorf("reset running agent steps: %w", err)
	}
	return nil
}

func (r *AgentRepository) failLockedRun(
	ctx context.Context,
	tx pgx.Tx,
	run agentdomain.Run,
	errorCode agentdomain.ErrorCode,
	errorMessage string,
) error {
	if err := failActiveAgentSteps(ctx, tx, run.ID, errorCode, errorMessage); err != nil {
		return err
	}
	if err := updateAssistantMessage(
		ctx,
		tx,
		run.AssistantMessageID,
		agentFailedMessage,
		conversationdomain.MessageStatusFailed,
	); err != nil {
		return fmt.Errorf("fail agent assistant message: %w", err)
	}
	commandTag, err := tx.Exec(
		ctx,
		`UPDATE agent_runs
		 SET status = 'failed',
		     error_code = $2,
		     error_message = $3,
		     updated_at = CURRENT_TIMESTAMP,
		     completed_at = CURRENT_TIMESTAMP,
		     worker_id = NULL,
		     lease_token = NULL,
		     lease_expires_at = NULL,
		     heartbeat_at = NULL
		 WHERE id = $1 AND status = 'processing'`,
		run.ID,
		errorCode,
		errorMessage,
	)
	if err != nil {
		return fmt.Errorf("mark agent run failed: %w", err)
	}
	if commandTag.RowsAffected() != 1 {
		return agentapplication.ErrRunLeaseLost
	}
	return nil
}

func failActiveAgentSteps(
	ctx context.Context,
	tx pgx.Tx,
	runID int64,
	errorCode agentdomain.ErrorCode,
	errorMessage string,
) error {
	if _, err := tx.Exec(
		ctx,
		`UPDATE agent_steps
		 SET status = 'failed',
		     result_json = NULL,
		     error_code = $2,
		     error_message = $3,
		     updated_at = CURRENT_TIMESTAMP,
		     started_at = COALESCE(started_at, CURRENT_TIMESTAMP),
		     completed_at = CURRENT_TIMESTAMP
		 WHERE agent_run_id = $1 AND status IN ('pending', 'running')`,
		runID,
		errorCode,
		errorMessage,
	); err != nil {
		return fmt.Errorf("fail active agent steps: %w", err)
	}
	return nil
}
