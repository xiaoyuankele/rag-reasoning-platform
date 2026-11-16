package postgres

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"math"
	"reflect"
	"strings"

	"github.com/jackc/pgx/v5"

	agentapplication "rag-reasoning-platform/backend/internal/application/agent"
	accessdomain "rag-reasoning-platform/backend/internal/domain/access"
	agentdomain "rag-reasoning-platform/backend/internal/domain/agent"
	conversationdomain "rag-reasoning-platform/backend/internal/domain/conversation"
)

var errInvalidAgentRunSnapshot = errors.New("database returned invalid agent run snapshot")

func (r *AgentRepository) getRunSnapshotByID(
	ctx context.Context,
	scope accessdomain.OwnerScope,
	runID int64,
) (agentapplication.RunSnapshot, error) {
	if !scope.IsValid() {
		return agentapplication.RunSnapshot{}, accessdomain.ErrInvalidOwnerScope
	}
	tx, err := r.pool.BeginTx(ctx, pgx.TxOptions{
		IsoLevel:   pgx.RepeatableRead,
		AccessMode: pgx.ReadOnly,
	})
	if err != nil {
		return agentapplication.RunSnapshot{}, fmt.Errorf("begin scoped agent snapshot: %w", err)
	}
	defer func() { _ = tx.Rollback(context.Background()) }()

	run, err := scanAgentRun(tx.QueryRow(
		ctx,
		`SELECT `+agentRunSelectColumns+`
		 FROM agent_runs
		 WHERE id = $1 AND owner_user_id = $2`,
		runID,
		scope.OwnerUserID(),
	))
	if errors.Is(err, pgx.ErrNoRows) {
		return agentapplication.RunSnapshot{}, agentapplication.ErrRunNotFound
	}
	if err != nil {
		return agentapplication.RunSnapshot{}, fmt.Errorf("get scoped agent run for snapshot: %w", err)
	}
	snapshot, err := loadAgentRunSnapshot(ctx, tx, scope, run)
	if err != nil {
		return agentapplication.RunSnapshot{}, err
	}
	if err := tx.Commit(ctx); err != nil {
		return agentapplication.RunSnapshot{}, fmt.Errorf("commit scoped agent snapshot: %w", err)
	}
	return snapshot, nil
}

func (r *AgentRepository) getConversationRunSnapshot(
	ctx context.Context,
	scope accessdomain.OwnerScope,
	conversationID int64,
	activeOnly bool,
) (agentapplication.RunSnapshot, error) {
	if !scope.IsValid() {
		return agentapplication.RunSnapshot{}, accessdomain.ErrInvalidOwnerScope
	}
	tx, err := r.pool.BeginTx(ctx, pgx.TxOptions{
		IsoLevel:   pgx.RepeatableRead,
		AccessMode: pgx.ReadOnly,
	})
	if err != nil {
		return agentapplication.RunSnapshot{}, fmt.Errorf("begin conversation agent snapshot: %w", err)
	}
	defer func() { _ = tx.Rollback(context.Background()) }()

	var scopedConversationID int64
	if err := tx.QueryRow(
		ctx,
		`SELECT id
		 FROM conversations
		 WHERE id = $1 AND owner_user_id = $2`,
		conversationID,
		scope.OwnerUserID(),
	).Scan(&scopedConversationID); errors.Is(err, pgx.ErrNoRows) {
		return agentapplication.RunSnapshot{}, conversationdomain.ErrNotFound
	} else if err != nil {
		return agentapplication.RunSnapshot{}, fmt.Errorf("get scoped conversation for agent snapshot: %w", err)
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
		scopedConversationID,
		scope.OwnerUserID(),
	))
	if errors.Is(err, pgx.ErrNoRows) {
		return agentapplication.RunSnapshot{}, agentapplication.ErrRunNotFound
	}
	if err != nil {
		return agentapplication.RunSnapshot{}, fmt.Errorf("get conversation agent run for snapshot: %w", err)
	}
	snapshot, err := loadAgentRunSnapshot(ctx, tx, scope, run)
	if err != nil {
		return agentapplication.RunSnapshot{}, err
	}
	if err := tx.Commit(ctx); err != nil {
		return agentapplication.RunSnapshot{}, fmt.Errorf("commit conversation agent snapshot: %w", err)
	}
	return snapshot, nil
}

func loadAgentRunSnapshot(
	ctx context.Context,
	tx pgx.Tx,
	scope accessdomain.OwnerScope,
	run agentdomain.Run,
) (agentapplication.RunSnapshot, error) {
	if !scope.IsValid() {
		return agentapplication.RunSnapshot{}, accessdomain.ErrInvalidOwnerScope
	}
	if run.OwnerUserID != scope.OwnerUserID() {
		return agentapplication.RunSnapshot{}, agentapplication.ErrRunNotFound
	}
	rows, err := tx.Query(
		ctx,
		`SELECT `+agentStepSelectColumns+`
		 FROM agent_steps AS step
		 WHERE step.agent_run_id = $1
		 ORDER BY step.sequence_no ASC, step.id ASC`,
		run.ID,
	)
	if err != nil {
		return agentapplication.RunSnapshot{}, fmt.Errorf("list agent steps for snapshot: %w", err)
	}
	steps, err := scanAgentSteps(rows)
	rows.Close()
	if err != nil {
		return agentapplication.RunSnapshot{}, err
	}
	if err := validateAgentRunSnapshot(run, steps); err != nil {
		return agentapplication.RunSnapshot{}, err
	}
	return agentapplication.RunSnapshot{Run: scopedAgentRun(run), Steps: steps}, nil
}

func validateAgentRunSnapshot(run agentdomain.Run, steps []agentdomain.Step) error {
	invalid := func(reason string) error {
		return fmt.Errorf("%w: %s", errInvalidAgentRunSnapshot, reason)
	}
	if run.ID <= 0 || run.OwnerUserID <= 0 || run.ConversationID <= 0 ||
		run.UserMessageID <= 0 || run.AssistantMessageID <= 0 ||
		!run.Status.IsValid() || !run.ResponseLanguage.IsValid() || !run.Budget.IsValid() {
		return invalid("invalid run identity or state")
	}
	if run.StepCount != len(steps) || run.StepCount < 0 || run.StepCount > run.Budget.MaxSteps {
		return invalid("run step count does not match ordered steps")
	}
	if !agentdomain.TokenUsageIsValid(run.PromptTokens, run.CompletionTokens, run.TotalTokens) ||
		run.TotalTokens > run.Budget.MaxTotalTokens {
		return invalid("invalid run token usage")
	}
	if run.Status.IsTerminal() != (run.CompletedAt != nil) {
		return invalid("run terminal timestamp is inconsistent")
	}
	if (run.Status == agentdomain.RunStatusProcessing || run.Status == agentdomain.RunStatusSucceeded ||
		run.Status == agentdomain.RunStatusFailed || run.AttemptCount > 0) && run.StartedAt == nil {
		return invalid("started run is missing its first processing timestamp")
	}

	promptTokens := 0
	completionTokens := 0
	totalTokens := 0
	finalSeen := false
	var persistedFinal agentapplication.FinalStepResult
	for index, step := range steps {
		if step.ID <= 0 || step.RunID != run.ID || step.SequenceNo != index+1 ||
			!step.Kind.IsValid() || !step.Status.IsValid() || !step.ErrorCode.IsValid() {
			return invalid("step identity, order, or state is inconsistent")
		}
		if !agentdomain.TokenUsageIsValid(step.PromptTokens, step.CompletionTokens, step.TotalTokens) {
			return invalid("step token usage is invalid")
		}
		if promptTokens > math.MaxInt-step.PromptTokens ||
			completionTokens > math.MaxInt-step.CompletionTokens ||
			totalTokens > math.MaxInt-step.TotalTokens {
			return invalid("step token aggregate overflows")
		}
		promptTokens += step.PromptTokens
		completionTokens += step.CompletionTokens
		totalTokens += step.TotalTokens

		switch step.Status {
		case agentdomain.StepStatusPending:
			if step.StartedAt != nil || step.CompletedAt != nil {
				return invalid("pending step timestamps are inconsistent")
			}
		case agentdomain.StepStatusRunning:
			if step.StartedAt == nil || step.CompletedAt != nil {
				return invalid("running step timestamps are inconsistent")
			}
		case agentdomain.StepStatusSucceeded, agentdomain.StepStatusFailed:
			if step.StartedAt == nil || step.CompletedAt == nil {
				return invalid("terminal step timestamps are inconsistent")
			}
		}
		if run.Status.IsTerminal() && !step.Status.IsTerminal() {
			return invalid("terminal run contains an active step")
		}

		switch step.Kind {
		case agentdomain.StepKindToolCall:
			if step.ToolName == nil ||
				(*step.ToolName != agentapplication.ToolSearchDocuments &&
					*step.ToolName != agentapplication.ToolReadDocumentEvidence) ||
				len(step.Arguments) == 0 || !json.Valid(step.Arguments) {
				return invalid("tool step shape is invalid")
			}
			if step.Status == agentdomain.StepStatusSucceeded &&
				(len(step.Result) == 0 || !json.Valid(step.Result)) {
				return invalid("successful tool step result is invalid")
			}
		case agentdomain.StepKindFinal:
			if finalSeen || index != len(steps)-1 || step.Status != agentdomain.StepStatusSucceeded ||
				step.ToolName != nil || len(step.Arguments) != 0 || len(step.Result) == 0 ||
				!json.Valid(step.Result) {
				return invalid("final step is not the last successful step")
			}
			if err := json.Unmarshal(step.Result, &persistedFinal); err != nil ||
				strings.TrimSpace(persistedFinal.Answer) == "" ||
				!persistedFinal.ResponseLanguage.IsResolved() ||
				(len(persistedFinal.Payload) > 0 && !json.Valid(persistedFinal.Payload)) {
				return invalid("final step result is invalid")
			}
			finalSeen = true
		}
		if step.Status == agentdomain.StepStatusFailed {
			if step.ErrorCode == agentdomain.ErrorCodeNone || step.ErrorMessage == nil ||
				strings.TrimSpace(*step.ErrorMessage) == "" || len(step.Result) != 0 {
				return invalid("failed step error is invalid")
			}
		} else if step.ErrorCode != agentdomain.ErrorCodeNone || step.ErrorMessage != nil {
			return invalid("non-failed step contains an error")
		}
	}
	if promptTokens != run.PromptTokens || completionTokens != run.CompletionTokens || totalTokens != run.TotalTokens {
		return invalid("run token usage does not equal the step aggregate")
	}
	if run.Status == agentdomain.RunStatusSucceeded {
		if !finalSeen || run.Result == nil || !run.Result.IsValid() ||
			persistedFinal.Answer != run.Result.Answer ||
			persistedFinal.ResponseLanguage != run.Result.ResponseLanguage ||
			!agentSnapshotJSONEqual(persistedFinal.Payload, run.Result.Payload) ||
			run.Result.PromptTokens != run.PromptTokens ||
			run.Result.CompletionTokens != run.CompletionTokens ||
			run.Result.TotalTokens != run.TotalTokens ||
			(len(run.Result.Payload) > 0 && !json.Valid(run.Result.Payload)) {
			return invalid("succeeded run result is inconsistent")
		}
	} else if finalSeen || run.Result != nil {
		return invalid("non-succeeded run contains a final result")
	}
	return nil
}

func agentSnapshotJSONEqual(left, right json.RawMessage) bool {
	if len(left) == 0 || len(right) == 0 {
		return len(left) == 0 && len(right) == 0
	}
	var leftValue any
	var rightValue any
	if json.Unmarshal(left, &leftValue) != nil || json.Unmarshal(right, &rightValue) != nil {
		return false
	}
	return reflect.DeepEqual(leftValue, rightValue)
}
