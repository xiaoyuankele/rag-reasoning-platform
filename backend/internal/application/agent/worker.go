package agent

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"time"

	accessdomain "rag-reasoning-platform/backend/internal/domain/access"
	agentdomain "rag-reasoning-platform/backend/internal/domain/agent"
)

const (
	safeRecoveryMessage    = "agent run lease expired"
	safeTemporaryMessage   = "agent dependency temporarily unavailable"
	safeExecutionMessage   = "agent execution failed"
	safeInvalidToolMessage = "agent model returned an invalid action"
	safeToolFailureMessage = "agent tool execution failed"
	safeStepLimitMessage   = "agent step limit exceeded"
	safeTokenBudgetMessage = "agent token budget exceeded"
	safeDeadlineMessage    = "agent deadline exceeded"
)

var (
	ErrWorkerDependencies  = errors.New("agent worker dependencies must be provided")
	ErrInvalidWorkerPolicy = errors.New("agent worker policy is invalid")
)

type ExecutionRepository interface {
	ClaimNextRun(ctx context.Context) (agentdomain.Run, error)
	RenewRunLease(ctx context.Context, runID int64, leaseToken string) error
	RequeueRun(
		ctx context.Context,
		runID int64,
		leaseToken string,
		nextAttemptAt time.Time,
		errorCode agentdomain.ErrorCode,
		errorMessage string,
	) error
	MarkRunFailed(
		ctx context.Context,
		runID int64,
		leaseToken string,
		errorCode agentdomain.ErrorCode,
		errorMessage string,
	) error
	CommitFinal(
		ctx context.Context,
		runID int64,
		leaseToken string,
		input CommitFinalInput,
	) (agentdomain.Step, error)
	RequeueExpiredRuns(
		ctx context.Context,
		limit int,
		errorCode agentdomain.ErrorCode,
		errorMessage string,
	) (int64, error)
	ListStepsForWorker(ctx context.Context, runID int64, leaseToken string) ([]agentdomain.Step, error)
	ClaimPendingStep(ctx context.Context, runID int64, leaseToken string) (agentdomain.Step, error)
	PersistDecision(
		ctx context.Context,
		runID int64,
		leaseToken string,
		input PersistDecisionInput,
	) (agentdomain.Step, error)
	MarkStepSucceeded(
		ctx context.Context,
		runID int64,
		stepID int64,
		leaseToken string,
		input CompleteStepInput,
	) (agentdomain.Step, error)
	MarkStepFailed(
		ctx context.Context,
		runID int64,
		stepID int64,
		leaseToken string,
		input FailStepInput,
	) (agentdomain.Step, error)
}

type WorkerPolicy struct {
	ModelTimeout      time.Duration
	ToolTimeout       time.Duration
	HeartbeatInterval time.Duration
	RetryDelay        time.Duration
	RecoveryBatchSize int
}

func (p WorkerPolicy) IsValid() bool {
	return p.ModelTimeout > 0 && p.ToolTimeout > 0 && p.HeartbeatInterval > 0 &&
		p.RetryDelay > 0 && p.RecoveryBatchSize > 0 && p.RecoveryBatchSize <= MaxRecoveryBatchSize
}

type Worker struct {
	repository ExecutionRepository
	contexts   ContextProvider
	model      AgentModel
	tools      ToolSet
	policy     WorkerPolicy
	now        func() time.Time
}

func NewWorker(
	repository ExecutionRepository,
	contexts ContextProvider,
	model AgentModel,
	tools ToolSet,
	policy WorkerPolicy,
) (*Worker, error) {
	if repository == nil || contexts == nil || model == nil || tools == nil {
		return nil, ErrWorkerDependencies
	}
	if !policy.IsValid() {
		return nil, ErrInvalidWorkerPolicy
	}
	return &Worker{
		repository: repository,
		contexts:   contexts,
		model:      model,
		tools:      tools,
		policy:     policy,
		now:        time.Now,
	}, nil
}

// RunOnce recovers a bounded expired batch, claims one Run and executes it.
func (w *Worker) RunOnce(ctx context.Context) (bool, error) {
	if _, err := w.repository.RequeueExpiredRuns(
		ctx,
		w.policy.RecoveryBatchSize,
		agentdomain.ErrorCodeWorkerInterrupted,
		safeRecoveryMessage,
	); err != nil {
		return false, fmt.Errorf("recover expired agent runs: %w", err)
	}
	run, err := w.repository.ClaimNextRun(ctx)
	if errors.Is(err, ErrNoQueuedRun) {
		return false, nil
	}
	if err != nil {
		return false, fmt.Errorf("claim next agent run: %w", err)
	}

	workContext, cancelWork := context.WithCancel(ctx)
	heartbeatContext, stopHeartbeat := context.WithCancel(ctx)
	heartbeatErrors := make(chan error, 1)
	heartbeatDone := make(chan struct{})
	go func() {
		defer close(heartbeatDone)
		ticker := time.NewTicker(w.policy.HeartbeatInterval)
		defer ticker.Stop()
		for {
			select {
			case <-heartbeatContext.Done():
				return
			case <-ticker.C:
				if renewErr := w.repository.RenewRunLease(
					heartbeatContext,
					run.ID,
					run.LeaseToken,
				); renewErr != nil {
					heartbeatErrors <- renewErr
					cancelWork()
					return
				}
			}
		}
	}()

	executionErr := w.executeRun(workContext, run)
	stopHeartbeat()
	cancelWork()
	<-heartbeatDone
	select {
	case heartbeatErr := <-heartbeatErrors:
		// A terminal transaction clears its lease before the heartbeat goroutine
		// is stopped. A renewal racing after a successful terminal write may
		// therefore observe lease loss; the committed terminal state is authoritative.
		if executionErr == nil && ctx.Err() == nil {
			return true, nil
		}
		return true, fmt.Errorf("renew agent run lease: %w", heartbeatErr)
	default:
	}
	if ctx.Err() != nil {
		return true, fmt.Errorf("agent run %d interrupted: %w", run.ID, ctx.Err())
	}
	return true, executionErr
}

func (w *Worker) executeRun(ctx context.Context, run agentdomain.Run) error {
	scope, err := accessdomain.NewOwnerScope(run.OwnerUserID)
	if err != nil {
		return w.failRun(ctx, run, agentdomain.ErrorCodeExecutionFailed, safeExecutionMessage)
	}
	steps, err := w.repository.ListStepsForWorker(ctx, run.ID, run.LeaseToken)
	if err != nil {
		return err
	}
	if failed := firstFailedStep(steps); failed != nil {
		code := failed.ErrorCode
		if code == agentdomain.ErrorCodeNone || !code.IsValid() {
			code = agentdomain.ErrorCodeExecutionFailed
		}
		return w.failRun(ctx, run, code, safeMessageForCode(code))
	}
	if hasUnexpectedRunningStep(steps) {
		return w.failRun(ctx, run, agentdomain.ErrorCodeExecutionFailed, safeExecutionMessage)
	}
	if pending := firstPendingStep(steps); pending != nil {
		if code := preflightTool(run, w.now()); code != agentdomain.ErrorCodeNone {
			return w.failRun(ctx, run, code, safeMessageForCode(code))
		}
		claimedStep, err := w.repository.ClaimPendingStep(ctx, run.ID, run.LeaseToken)
		if err != nil {
			return err
		}
		replaceStep(&steps, claimedStep)
		completed, err := w.executeTool(ctx, scope, run, &steps, claimedStep)
		if err != nil || !completed {
			return err
		}
	}

	contextSnapshot, err := w.contexts.LoadAgentContext(ctx, scope, run)
	if err != nil {
		if ctx.Err() != nil {
			return ctx.Err()
		}
		if errors.Is(err, ErrAgentContextUnavailable) || errors.Is(err, ErrModelTemporarilyUnavailable) {
			return w.requeueRun(ctx, run)
		}
		return w.failRun(ctx, run, agentdomain.ErrorCodeExecutionFailed, safeExecutionMessage)
	}
	if !contextSnapshot.IsValid() || !contextSnapshot.ResponseLanguage.IsResolved() ||
		(run.ResponseLanguage.IsResolved() && run.ResponseLanguage != contextSnapshot.ResponseLanguage) {
		return w.failRun(ctx, run, agentdomain.ErrorCodeExecutionFailed, safeExecutionMessage)
	}

	for {
		if ctx.Err() != nil {
			return ctx.Err()
		}
		if code := preflightModel(run, w.now()); code != agentdomain.ErrorCodeNone {
			return w.failRun(ctx, run, code, safeMessageForCode(code))
		}
		request := ModelRequest{
			RunID:               run.ID,
			RequestedDocumentID: copyOptionalInt64(run.RequestedDocumentID),
			ResponseLanguage:    contextSnapshot.ResponseLanguage,
			Context:             cloneAgentContext(contextSnapshot),
			Tools:               cloneToolDefinitions(w.tools.Definitions()),
			Observations:        modelObservations(steps),
			Remaining: RemainingBudget{
				Steps:      run.Budget.MaxSteps - run.StepCount,
				Tokens:     run.Budget.MaxTotalTokens - run.TotalTokens,
				DeadlineAt: run.Budget.DeadlineAt,
			},
		}
		callContext, cancel := boundedCallContext(ctx, w.policy.ModelTimeout, run.Budget.DeadlineAt)
		decision, decisionErr := w.model.Decide(callContext, request)
		callErr := callContext.Err()
		cancel()
		if ctx.Err() != nil {
			return ctx.Err()
		}
		if decisionErr != nil || callErr != nil {
			if !w.now().Before(run.Budget.DeadlineAt) {
				return w.failRun(ctx, run, agentdomain.ErrorCodeDeadlineExceeded, safeDeadlineMessage)
			}
			if errors.Is(decisionErr, ErrInvalidModelDecision) {
				return w.failRun(ctx, run, agentdomain.ErrorCodeInvalidToolCall, safeInvalidToolMessage)
			}
			if errors.Is(decisionErr, ErrModelTemporarilyUnavailable) ||
				errors.Is(decisionErr, context.DeadlineExceeded) || errors.Is(callErr, context.DeadlineExceeded) {
				return w.requeueRun(ctx, run)
			}
			return w.failRun(ctx, run, agentdomain.ErrorCodeExecutionFailed, safeExecutionMessage)
		}
		if !decision.IsValid() {
			return w.failRun(ctx, run, agentdomain.ErrorCodeInvalidToolCall, safeInvalidToolMessage)
		}
		if decision.Usage.TotalTokens > run.Budget.MaxTotalTokens-run.TotalTokens {
			return w.failRun(ctx, run, agentdomain.ErrorCodeTokenBudgetExceeded, safeTokenBudgetMessage)
		}
		if decision.ToolCall != nil {
			// A Tool decision must leave Token budget for the final Model decision.
			if decision.Usage.TotalTokens >= run.Budget.MaxTotalTokens-run.TotalTokens {
				return w.failRun(ctx, run, agentdomain.ErrorCodeTokenBudgetExceeded, safeTokenBudgetMessage)
			}
			canonical, err := w.tools.ValidateCall(run, steps, *decision.ToolCall)
			if err != nil {
				return w.failRun(ctx, run, agentdomain.ErrorCodeInvalidToolCall, safeInvalidToolMessage)
			}
			persisted, err := w.repository.PersistDecision(ctx, run.ID, run.LeaseToken, PersistDecisionInput{
				ToolName:         decision.ToolCall.Name,
				Arguments:        canonical,
				PromptTokens:     decision.Usage.PromptTokens,
				CompletionTokens: decision.Usage.CompletionTokens,
				TotalTokens:      decision.Usage.TotalTokens,
			})
			if err != nil {
				return w.handlePersistenceBoundary(ctx, run, err)
			}
			applyDecisionAccounting(&run, persisted)
			steps = append(steps, persisted)
			if code := preflightTool(run, w.now()); code != agentdomain.ErrorCodeNone {
				return w.failRun(ctx, run, code, safeMessageForCode(code))
			}
			completed, err := w.executeTool(ctx, scope, run, &steps, persisted)
			if err != nil || !completed {
				return err
			}
			continue
		}

		sources, err := w.tools.ResolveFinalSources(run, steps, decision.Final.EvidenceRefs)
		if err != nil {
			return w.failRun(ctx, run, agentdomain.ErrorCodeInvalidToolCall, safeInvalidToolMessage)
		}
		payload, err := json.Marshal(struct {
			Sources []FinalSource `json:"sources"`
		}{Sources: sources})
		if err != nil {
			return w.failRun(ctx, run, agentdomain.ErrorCodeExecutionFailed, safeExecutionMessage)
		}
		_, err = w.repository.CommitFinal(ctx, run.ID, run.LeaseToken, CommitFinalInput{
			Result: FinalStepResult{
				Answer:           decision.Final.Text,
				ResponseLanguage: contextSnapshot.ResponseLanguage,
				Payload:          payload,
			},
			PromptTokens:     decision.Usage.PromptTokens,
			CompletionTokens: decision.Usage.CompletionTokens,
			TotalTokens:      decision.Usage.TotalTokens,
		})
		if err != nil {
			return w.handlePersistenceBoundary(ctx, run, err)
		}
		return nil
	}
}

func (w *Worker) executeTool(
	ctx context.Context,
	scope accessdomain.OwnerScope,
	run agentdomain.Run,
	steps *[]agentdomain.Step,
	step agentdomain.Step,
) (bool, error) {
	if code := preflightTool(run, w.now()); code != agentdomain.ErrorCodeNone {
		return false, w.failRun(ctx, run, code, safeMessageForCode(code))
	}
	callContext, cancel := boundedCallContext(ctx, w.policy.ToolTimeout, run.Budget.DeadlineAt)
	result, toolErr := w.tools.Execute(callContext, scope, run, *steps, step)
	callErr := callContext.Err()
	cancel()
	if ctx.Err() != nil {
		return false, ctx.Err()
	}
	if toolErr != nil || callErr != nil {
		if !w.now().Before(run.Budget.DeadlineAt) {
			return false, w.failRun(ctx, run, agentdomain.ErrorCodeDeadlineExceeded, safeDeadlineMessage)
		}
		if errors.Is(toolErr, ErrToolTemporarilyUnavailable) ||
			errors.Is(toolErr, context.DeadlineExceeded) || errors.Is(callErr, context.DeadlineExceeded) {
			return false, w.requeueRun(ctx, run)
		}
		code := agentdomain.ErrorCodeToolFailed
		message := safeToolFailureMessage
		if errors.Is(toolErr, ErrInvalidToolCall) || errors.Is(toolErr, ErrEvidenceRefNotFound) {
			code = agentdomain.ErrorCodeInvalidToolCall
			message = safeInvalidToolMessage
		}
		if _, err := w.repository.MarkStepFailed(ctx, run.ID, step.ID, run.LeaseToken, FailStepInput{
			ErrorCode: code, ErrorMessage: message,
		}); err != nil {
			return false, err
		}
		return false, w.failRun(ctx, run, code, message)
	}
	completed, err := w.repository.MarkStepSucceeded(ctx, run.ID, step.ID, run.LeaseToken, CompleteStepInput{
		Result: result,
	})
	if err != nil {
		return false, err
	}
	replaceStep(steps, completed)
	return true, nil
}

func (w *Worker) handlePersistenceBoundary(ctx context.Context, run agentdomain.Run, err error) error {
	switch {
	case errors.Is(err, ErrRunLeaseLost):
		return err
	case errors.Is(err, ErrStepLimitExceeded):
		return w.failRun(ctx, run, agentdomain.ErrorCodeStepLimitExceeded, safeStepLimitMessage)
	case errors.Is(err, ErrTokenBudgetExceeded):
		return w.failRun(ctx, run, agentdomain.ErrorCodeTokenBudgetExceeded, safeTokenBudgetMessage)
	case errors.Is(err, ErrRunDeadlineExceeded):
		return w.failRun(ctx, run, agentdomain.ErrorCodeDeadlineExceeded, safeDeadlineMessage)
	case errors.Is(err, ErrInvalidStep), errors.Is(err, ErrFinalStepAlreadyPersisted):
		return w.failRun(ctx, run, agentdomain.ErrorCodeInvalidToolCall, safeInvalidToolMessage)
	default:
		return err
	}
}

func (w *Worker) requeueRun(ctx context.Context, run agentdomain.Run) error {
	if ctx.Err() != nil {
		return ctx.Err()
	}
	return w.repository.RequeueRun(
		ctx,
		run.ID,
		run.LeaseToken,
		w.now().Add(w.policy.RetryDelay),
		agentdomain.ErrorCodeTemporarilyUnavailable,
		safeTemporaryMessage,
	)
}

func (w *Worker) failRun(
	ctx context.Context,
	run agentdomain.Run,
	code agentdomain.ErrorCode,
	message string,
) error {
	if ctx.Err() != nil {
		return ctx.Err()
	}
	return w.repository.MarkRunFailed(ctx, run.ID, run.LeaseToken, code, message)
}

func preflightModel(run agentdomain.Run, now time.Time) agentdomain.ErrorCode {
	if !now.Before(run.Budget.DeadlineAt) {
		return agentdomain.ErrorCodeDeadlineExceeded
	}
	if run.StepCount >= run.Budget.MaxSteps {
		return agentdomain.ErrorCodeStepLimitExceeded
	}
	if run.TotalTokens >= run.Budget.MaxTotalTokens {
		return agentdomain.ErrorCodeTokenBudgetExceeded
	}
	return agentdomain.ErrorCodeNone
}

func preflightTool(run agentdomain.Run, now time.Time) agentdomain.ErrorCode {
	if !now.Before(run.Budget.DeadlineAt) {
		return agentdomain.ErrorCodeDeadlineExceeded
	}
	if run.TotalTokens >= run.Budget.MaxTotalTokens {
		return agentdomain.ErrorCodeTokenBudgetExceeded
	}
	return agentdomain.ErrorCodeNone
}

func safeMessageForCode(code agentdomain.ErrorCode) string {
	switch code {
	case agentdomain.ErrorCodeStepLimitExceeded:
		return safeStepLimitMessage
	case agentdomain.ErrorCodeTokenBudgetExceeded:
		return safeTokenBudgetMessage
	case agentdomain.ErrorCodeDeadlineExceeded:
		return safeDeadlineMessage
	case agentdomain.ErrorCodeInvalidToolCall:
		return safeInvalidToolMessage
	case agentdomain.ErrorCodeToolFailed:
		return safeToolFailureMessage
	default:
		return safeExecutionMessage
	}
}

func modelObservations(steps []agentdomain.Step) []ModelObservation {
	observations := make([]ModelObservation, 0, len(steps))
	for _, step := range steps {
		if step.Kind != agentdomain.StepKindToolCall || step.Status != agentdomain.StepStatusSucceeded || step.ToolName == nil {
			continue
		}
		observations = append(observations, ModelObservation{
			SequenceNo: step.SequenceNo,
			ToolName:   *step.ToolName,
			Arguments:  append(json.RawMessage(nil), step.Arguments...),
			Result:     append(json.RawMessage(nil), step.Result...),
		})
	}
	return observations
}

func applyDecisionAccounting(run *agentdomain.Run, step agentdomain.Step) {
	run.StepCount++
	run.PromptTokens += step.PromptTokens
	run.CompletionTokens += step.CompletionTokens
	run.TotalTokens += step.TotalTokens
}

func replaceStep(steps *[]agentdomain.Step, replacement agentdomain.Step) {
	for index := range *steps {
		if (*steps)[index].ID == replacement.ID {
			(*steps)[index] = replacement
			return
		}
	}
	*steps = append(*steps, replacement)
}

func firstPendingStep(steps []agentdomain.Step) *agentdomain.Step {
	for index := range steps {
		if steps[index].Status == agentdomain.StepStatusPending {
			return &steps[index]
		}
	}
	return nil
}

func firstFailedStep(steps []agentdomain.Step) *agentdomain.Step {
	for index := range steps {
		if steps[index].Status == agentdomain.StepStatusFailed {
			return &steps[index]
		}
	}
	return nil
}

func hasUnexpectedRunningStep(steps []agentdomain.Step) bool {
	for _, step := range steps {
		if step.Status == agentdomain.StepStatusRunning {
			return true
		}
	}
	return false
}

func cloneAgentContext(input AgentContext) AgentContext {
	cloned := input
	cloned.History = append([]ModelMessage(nil), input.History...)
	return cloned
}

func cloneToolDefinitions(input []ToolDefinition) []ToolDefinition {
	cloned := make([]ToolDefinition, len(input))
	for index, definition := range input {
		cloned[index] = definition
		cloned[index].Parameters = append(json.RawMessage(nil), definition.Parameters...)
	}
	return cloned
}

func copyOptionalInt64(value *int64) *int64 {
	if value == nil {
		return nil
	}
	copy := *value
	return &copy
}

func boundedCallContext(
	parent context.Context,
	timeout time.Duration,
	runDeadline time.Time,
) (context.Context, context.CancelFunc) {
	deadline := time.Now().Add(timeout)
	if runDeadline.Before(deadline) {
		deadline = runDeadline
	}
	return context.WithDeadline(parent, deadline)
}
