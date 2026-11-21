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
	accessdomain "rag-reasoning-platform/backend/internal/domain/access"
	agentdomain "rag-reasoning-platform/backend/internal/domain/agent"
)

const (
	maxAgentStepArgumentsBytes = 8192
	maxAgentStepResultBytes    = 32768
)

func (r *AgentRepository) ListSteps(
	ctx context.Context,
	scope accessdomain.OwnerScope,
	runID int64,
) ([]agentdomain.Step, error) {
	if !scope.IsValid() {
		return nil, accessdomain.ErrInvalidOwnerScope
	}
	rows, err := r.pool.Query(
		ctx,
		`SELECT `+agentStepSelectColumns+`
		 FROM agent_steps AS step
		 JOIN agent_runs AS run ON run.id = step.agent_run_id
		 WHERE step.agent_run_id = $1 AND run.owner_user_id = $2
		 ORDER BY step.sequence_no ASC, step.id ASC`,
		runID,
		scope.OwnerUserID(),
	)
	if err != nil {
		return nil, fmt.Errorf("list scoped agent steps: %w", err)
	}
	defer rows.Close()
	steps, err := scanAgentSteps(rows)
	if err != nil {
		return nil, err
	}
	if len(steps) == 0 {
		if _, err := r.GetRunByID(ctx, scope, runID); err != nil {
			return nil, err
		}
	}
	return steps, nil
}

func (r *AgentRepository) ListStepsForWorker(
	ctx context.Context,
	runID int64,
	leaseToken string,
) ([]agentdomain.Step, error) {
	tx, err := r.pool.Begin(ctx)
	if err != nil {
		return nil, fmt.Errorf("begin worker agent step list: %w", err)
	}
	defer func() { _ = tx.Rollback(context.Background()) }()
	if _, err := r.lockFencedRun(ctx, tx, runID, leaseToken); err != nil {
		return nil, err
	}
	rows, err := tx.Query(
		ctx,
		`SELECT `+agentStepSelectColumns+`
		 FROM agent_steps AS step
		 WHERE step.agent_run_id = $1
		 ORDER BY step.sequence_no ASC, step.id ASC`,
		runID,
	)
	if err != nil {
		return nil, fmt.Errorf("list worker agent steps: %w", err)
	}
	steps, err := scanAgentSteps(rows)
	rows.Close()
	if err != nil {
		return nil, err
	}
	if err := tx.Commit(ctx); err != nil {
		return nil, fmt.Errorf("commit worker agent step list: %w", err)
	}
	return steps, nil
}

// ClaimPendingStep resumes the one Step reset by lease recovery.
func (r *AgentRepository) ClaimPendingStep(
	ctx context.Context,
	runID int64,
	leaseToken string,
) (agentdomain.Step, error) {
	tx, err := r.pool.Begin(ctx)
	if err != nil {
		return agentdomain.Step{}, fmt.Errorf("begin pending agent step claim: %w", err)
	}
	defer func() { _ = tx.Rollback(context.Background()) }()
	run, err := r.lockFencedRun(ctx, tx, runID, leaseToken)
	if err != nil {
		return agentdomain.Step{}, err
	}
	deadlineOpen, err := agentDeadlineOpen(ctx, tx, run.Budget.DeadlineAt)
	if err != nil {
		return agentdomain.Step{}, err
	}
	if !deadlineOpen {
		return agentdomain.Step{}, agentapplication.ErrRunDeadlineExceeded
	}
	var stepID int64
	if err := tx.QueryRow(
		ctx,
		`SELECT id
		 FROM agent_steps
		 WHERE agent_run_id = $1 AND kind = 'tool_call' AND status = 'pending'
		 ORDER BY sequence_no ASC
		 FOR UPDATE
		 LIMIT 1`,
		runID,
	).Scan(&stepID); errors.Is(err, pgx.ErrNoRows) {
		return agentdomain.Step{}, agentapplication.ErrNoPendingStep
	} else if err != nil {
		return agentdomain.Step{}, fmt.Errorf("lock pending agent step: %w", err)
	}
	step, err := scanAgentStep(tx.QueryRow(
		ctx,
		`UPDATE agent_steps AS step
		 SET status = 'running',
		     updated_at = CURRENT_TIMESTAMP,
		     started_at = CURRENT_TIMESTAMP,
		     completed_at = NULL
		 WHERE id = $1 AND status = 'pending'
		 RETURNING `+agentStepReturningColumns,
		stepID,
	))
	if errors.Is(err, pgx.ErrNoRows) {
		return agentdomain.Step{}, agentapplication.ErrNoPendingStep
	}
	if err != nil {
		return agentdomain.Step{}, fmt.Errorf("claim pending agent step: %w", err)
	}
	if err := tx.Commit(ctx); err != nil {
		return agentdomain.Step{}, fmt.Errorf("commit pending agent step claim: %w", err)
	}
	return step, nil
}

// PersistDecision durably records one Tool decision before the Tool may run.
func (r *AgentRepository) PersistDecision(
	ctx context.Context,
	runID int64,
	leaseToken string,
	input agentapplication.PersistDecisionInput,
) (agentdomain.Step, error) {
	if err := validatePersistDecision(input); err != nil {
		return agentdomain.Step{}, err
	}
	tx, err := r.pool.Begin(ctx)
	if err != nil {
		return agentdomain.Step{}, fmt.Errorf("begin agent step start: %w", err)
	}
	defer func() { _ = tx.Rollback(context.Background()) }()
	run, err := r.lockFencedRun(ctx, tx, runID, leaseToken)
	if err != nil {
		return agentdomain.Step{}, err
	}
	// One Step is always reserved for the final decision.
	if run.StepCount >= run.Budget.MaxSteps-1 {
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
	var activeStepExists bool
	var failedStepExists bool
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
	).Scan(&finalExists, &activeStepExists, &failedStepExists); err != nil {
		return agentdomain.Step{}, fmt.Errorf("check agent step append boundary: %w", err)
	}
	if finalExists {
		return agentdomain.Step{}, agentapplication.ErrFinalStepAlreadyPersisted
	}
	if activeStepExists {
		return agentdomain.Step{}, agentapplication.ErrInvalidStep
	}
	if failedStepExists {
		return agentdomain.Step{}, agentapplication.ErrInvalidStep
	}
	withinLimit, err := jsonbSizeWithin(ctx, tx, input.Arguments, maxAgentStepArgumentsBytes)
	if err != nil {
		return agentdomain.Step{}, err
	}
	if !withinLimit {
		return agentdomain.Step{}, agentapplication.ErrInvalidStep
	}
	sequenceNo := run.StepCount + 1
	toolName := input.ToolName
	step, err := scanAgentStep(tx.QueryRow(
		ctx,
		`INSERT INTO agent_steps AS step (
			agent_run_id, sequence_no, kind, status, tool_name, arguments_json,
			prompt_tokens, completion_tokens, total_tokens, started_at
		 )
		 VALUES ($1, $2, 'tool_call', 'running', $3, $4::JSONB, $5, $6, $7, CURRENT_TIMESTAMP)
		 RETURNING `+agentStepReturningColumns,
		run.ID,
		sequenceNo,
		toolName,
		input.Arguments,
		input.PromptTokens,
		input.CompletionTokens,
		input.TotalTokens,
	))
	if err != nil {
		return agentdomain.Step{}, fmt.Errorf("insert running agent step: %w", err)
	}
	commandTag, err := tx.Exec(
		ctx,
		`UPDATE agent_runs
		 SET step_count = step_count + 1,
		     prompt_tokens = COALESCE(prompt_tokens, 0) + $4,
		     completion_tokens = COALESCE(completion_tokens, 0) + $5,
		     total_tokens = COALESCE(total_tokens, 0) + $6,
		     updated_at = CURRENT_TIMESTAMP
		 WHERE id = $1
		   AND status = 'processing'
		   AND lease_token = $2
		   AND lease_expires_at > CURRENT_TIMESTAMP
		   AND step_count = $3
		   AND COALESCE(total_tokens, 0) + $6 <= max_total_tokens`,
		run.ID,
		leaseToken,
		run.StepCount,
		input.PromptTokens,
		input.CompletionTokens,
		input.TotalTokens,
	)
	if err != nil {
		return agentdomain.Step{}, fmt.Errorf("persist agent decision accounting: %w", err)
	}
	if commandTag.RowsAffected() != 1 {
		return agentdomain.Step{}, agentapplication.ErrRunLeaseLost
	}
	if err := tx.Commit(ctx); err != nil {
		return agentdomain.Step{}, fmt.Errorf("commit agent decision: %w", err)
	}
	return step, nil
}

func (r *AgentRepository) MarkStepSucceeded(
	ctx context.Context,
	runID int64,
	stepID int64,
	leaseToken string,
	input agentapplication.CompleteStepInput,
) (agentdomain.Step, error) {
	if len(input.Result) == 0 || len(input.Result) > maxAgentStepResultBytes ||
		!json.Valid(input.Result) {
		return agentdomain.Step{}, agentapplication.ErrInvalidStep
	}
	tx, err := r.pool.Begin(ctx)
	if err != nil {
		return agentdomain.Step{}, fmt.Errorf("begin agent step success: %w", err)
	}
	defer func() { _ = tx.Rollback(context.Background()) }()
	withinLimit, err := jsonbSizeWithin(ctx, tx, input.Result, maxAgentStepResultBytes)
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
	var lockedStepID int64
	if err := tx.QueryRow(
		ctx,
		`SELECT id
		 FROM agent_steps
		 WHERE id = $1 AND agent_run_id = $2 AND kind = 'tool_call' AND status = 'running'
		 FOR UPDATE`,
		stepID,
		run.ID,
	).Scan(&lockedStepID); errors.Is(err, pgx.ErrNoRows) {
		return agentdomain.Step{}, agentapplication.ErrInvalidStep
	} else if err != nil {
		return agentdomain.Step{}, fmt.Errorf("lock running agent step for success: %w", err)
	}
	step, err := scanAgentStep(tx.QueryRow(
		ctx,
		`UPDATE agent_steps AS step
		 SET status = 'succeeded',
		     result_json = $2::JSONB,
		     error_code = NULL,
		     error_message = NULL,
		     updated_at = CURRENT_TIMESTAMP,
		     completed_at = CURRENT_TIMESTAMP
		 WHERE id = $1 AND status = 'running'
		 RETURNING `+agentStepReturningColumns,
		lockedStepID,
		input.Result,
	))
	if err != nil {
		return agentdomain.Step{}, fmt.Errorf("mark agent step succeeded: %w", err)
	}
	if err := tx.Commit(ctx); err != nil {
		return agentdomain.Step{}, fmt.Errorf("commit agent step success: %w", err)
	}
	return step, nil
}

func (r *AgentRepository) MarkStepFailed(
	ctx context.Context,
	runID int64,
	stepID int64,
	leaseToken string,
	input agentapplication.FailStepInput,
) (agentdomain.Step, error) {
	if err := validateAgentError(input.ErrorCode, input.ErrorMessage); err != nil {
		return agentdomain.Step{}, err
	}
	tx, err := r.pool.Begin(ctx)
	if err != nil {
		return agentdomain.Step{}, fmt.Errorf("begin agent step failure: %w", err)
	}
	defer func() { _ = tx.Rollback(context.Background()) }()
	if _, err := r.lockFencedRun(ctx, tx, runID, leaseToken); err != nil {
		return agentdomain.Step{}, err
	}
	var lockedStepID int64
	if err := tx.QueryRow(
		ctx,
		`SELECT id
		 FROM agent_steps
		 WHERE id = $1 AND agent_run_id = $2 AND kind = 'tool_call' AND status = 'running'
		 FOR UPDATE`,
		stepID,
		runID,
	).Scan(&lockedStepID); errors.Is(err, pgx.ErrNoRows) {
		return agentdomain.Step{}, agentapplication.ErrInvalidStep
	} else if err != nil {
		return agentdomain.Step{}, fmt.Errorf("lock running agent step for failure: %w", err)
	}
	step, err := scanAgentStep(tx.QueryRow(
		ctx,
		`UPDATE agent_steps AS step
		 SET status = 'failed',
		     result_json = NULL,
		     error_code = $2,
		     error_message = $3,
		     updated_at = CURRENT_TIMESTAMP,
		     completed_at = CURRENT_TIMESTAMP
		 WHERE id = $1 AND status = 'running'
		 RETURNING `+agentStepReturningColumns,
		lockedStepID,
		input.ErrorCode,
		input.ErrorMessage,
	))
	if err != nil {
		return agentdomain.Step{}, fmt.Errorf("mark agent step failed: %w", err)
	}
	if err := tx.Commit(ctx); err != nil {
		return agentdomain.Step{}, fmt.Errorf("commit agent step failure: %w", err)
	}
	return step, nil
}

const agentStepSelectColumns = `
	step.id, step.agent_run_id, step.sequence_no, step.kind, step.status, step.tool_name,
	step.arguments_json, step.result_json, step.error_code, step.error_message,
	step.prompt_tokens, step.completion_tokens, step.total_tokens,
	step.created_at, step.started_at, step.completed_at
`

const agentStepReturningColumns = `
	step.id, step.agent_run_id, step.sequence_no, step.kind, step.status, step.tool_name,
	step.arguments_json, step.result_json, step.error_code, step.error_message,
	step.prompt_tokens, step.completion_tokens, step.total_tokens,
	step.created_at, step.started_at, step.completed_at
`

func scanAgentStep(row pgx.Row) (agentdomain.Step, error) {
	var step agentdomain.Step
	var kind string
	var status string
	var argumentsJSON []byte
	var resultJSON []byte
	var errorCode *string
	var promptTokens *int
	var completionTokens *int
	var totalTokens *int
	err := row.Scan(
		&step.ID,
		&step.RunID,
		&step.SequenceNo,
		&kind,
		&status,
		&step.ToolName,
		&argumentsJSON,
		&resultJSON,
		&errorCode,
		&step.ErrorMessage,
		&promptTokens,
		&completionTokens,
		&totalTokens,
		&step.CreatedAt,
		&step.StartedAt,
		&step.CompletedAt,
	)
	if err != nil {
		return agentdomain.Step{}, err
	}
	step.Kind = agentdomain.StepKind(kind)
	step.Status = agentdomain.StepStatus(status)
	step.Arguments = argumentsJSON
	step.Result = resultJSON
	if errorCode != nil {
		step.ErrorCode = agentdomain.ErrorCode(*errorCode)
	}
	if promptTokens != nil {
		step.PromptTokens = *promptTokens
		step.CompletionTokens = *completionTokens
		step.TotalTokens = *totalTokens
	}
	if !step.Kind.IsValid() || !step.Status.IsValid() || !step.ErrorCode.IsValid() {
		return agentdomain.Step{}, errors.New("database returned invalid agent step")
	}
	return step, nil
}

func scanAgentSteps(rows pgx.Rows) ([]agentdomain.Step, error) {
	steps := make([]agentdomain.Step, 0)
	for rows.Next() {
		step, err := scanAgentStep(rows)
		if err != nil {
			return nil, fmt.Errorf("scan agent step: %w", err)
		}
		steps = append(steps, step)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("iterate agent steps: %w", err)
	}
	return steps, nil
}

func validatePersistDecision(input agentapplication.PersistDecisionInput) error {
	if strings.TrimSpace(input.ToolName) == "" ||
		strings.TrimSpace(input.ToolName) != input.ToolName || len(input.ToolName) > 100 ||
		len(input.Arguments) == 0 || len(input.Arguments) > maxAgentStepArgumentsBytes ||
		!json.Valid(input.Arguments) ||
		!agentdomain.TokenUsageIsValid(input.PromptTokens, input.CompletionTokens, input.TotalTokens) {
		return agentapplication.ErrInvalidStep
	}
	return nil
}

func agentDeadlineOpen(ctx context.Context, tx pgx.Tx, deadline time.Time) (bool, error) {
	var open bool
	if err := tx.QueryRow(ctx, `SELECT CURRENT_TIMESTAMP < $1`, deadline).Scan(&open); err != nil {
		return false, fmt.Errorf("check agent run deadline: %w", err)
	}
	return open, nil
}

func jsonbSizeWithin(ctx context.Context, tx pgx.Tx, value json.RawMessage, limit int) (bool, error) {
	var size int
	if err := tx.QueryRow(
		ctx,
		`SELECT OCTET_LENGTH(($1::JSONB)::TEXT)`,
		value,
	).Scan(&size); err != nil {
		return false, fmt.Errorf("measure bounded agent JSON: %w", err)
	}
	return size <= limit, nil
}
