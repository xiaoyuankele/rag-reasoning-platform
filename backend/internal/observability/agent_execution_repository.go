package observability

import (
	"context"
	"errors"
	"log/slog"
	"time"

	agentapplication "rag-reasoning-platform/backend/internal/application/agent"
	agentdomain "rag-reasoning-platform/backend/internal/domain/agent"
)

type AgentQueueStatsReader interface {
	GetQueueStats(context.Context) (agentdomain.QueueStats, error)
}

// AgentExecutionRepositoryLogger decorates Agent persistence with payload-free
// lifecycle events. It never logs Owner identity, leases, messages, arguments,
// results, final answers, evidence or raw errors.
type AgentExecutionRepositoryLogger struct {
	next   agentapplication.ExecutionRepository
	stats  AgentQueueStatsReader
	logger *slog.Logger
	now    func() time.Time
}

var _ agentapplication.ExecutionRepository = (*AgentExecutionRepositoryLogger)(nil)

func NewAgentExecutionRepositoryLogger(
	next agentapplication.ExecutionRepository,
	stats AgentQueueStatsReader,
	logger *slog.Logger,
) *AgentExecutionRepositoryLogger {
	if next == nil || stats == nil {
		panic("NewAgentExecutionRepositoryLogger requires non-nil repositories")
	}
	if logger == nil {
		panic("NewAgentExecutionRepositoryLogger requires a non-nil logger")
	}
	return &AgentExecutionRepositoryLogger{next: next, stats: stats, logger: logger, now: time.Now}
}

func (r *AgentExecutionRepositoryLogger) ClaimNextRun(ctx context.Context) (agentdomain.Run, error) {
	startedAt := r.now()
	run, err := r.next.ClaimNextRun(ctx)
	attributes := []slog.Attr{
		slog.String("event", "agent_run_claim"),
		slog.Int64("duration_ms", r.now().Sub(startedAt).Milliseconds()),
	}
	if err == nil {
		eligibleAt := run.CreatedAt
		if run.NextAttemptAt.After(eligibleAt) {
			eligibleAt = run.NextAttemptAt
		}
		queueWait := r.now().Sub(eligibleAt)
		if queueWait < 0 {
			queueWait = 0
		}
		attributes = append(attributes,
			slog.Int64("agent_run_id", run.ID),
			slog.String("status", safeAgentRunStatus(run.Status)),
			slog.Int("attempt_count", run.AttemptCount),
			slog.Int64("queue_wait_ms", queueWait.Milliseconds()),
			slog.Int("step_count", run.StepCount),
			slog.Int("prompt_tokens", run.PromptTokens),
			slog.Int("completion_tokens", run.CompletionTokens),
			slog.Int("total_tokens", run.TotalTokens),
		)
	}
	r.logRepositoryEvent(ctx, err, attributes, true)
	return run, err
}

func (r *AgentExecutionRepositoryLogger) GetQueueStats(ctx context.Context) (agentdomain.QueueStats, error) {
	return r.stats.GetQueueStats(ctx)
}

func (r *AgentExecutionRepositoryLogger) RenewRunLease(ctx context.Context, runID int64, leaseToken string) error {
	err := r.next.RenewRunLease(ctx, runID, leaseToken)
	if err != nil {
		r.logRepositoryEvent(ctx, err, []slog.Attr{
			slog.String("event", "agent_run_heartbeat"), slog.Int64("agent_run_id", runID),
		}, false)
	}
	return err
}

func (r *AgentExecutionRepositoryLogger) RequeueRun(
	ctx context.Context,
	runID int64,
	leaseToken string,
	nextAttemptAt time.Time,
	errorCode agentdomain.ErrorCode,
	errorMessage string,
) error {
	startedAt := r.now()
	err := r.next.RequeueRun(ctx, runID, leaseToken, nextAttemptAt, errorCode, errorMessage)
	attributes := []slog.Attr{
		slog.String("event", "agent_run_requeue"),
		slog.Int64("agent_run_id", runID),
		slog.Int64("duration_ms", r.now().Sub(startedAt).Milliseconds()),
	}
	if err == nil {
		attributes = append(attributes, slog.String("status", safeAgentRunStatus(agentdomain.RunStatusQueued)))
	}
	attributes = appendSafeAgentErrorCode(attributes, errorCode)
	r.logRepositoryEvent(ctx, err, attributes, true)
	return err
}

func (r *AgentExecutionRepositoryLogger) MarkRunFailed(
	ctx context.Context,
	runID int64,
	leaseToken string,
	errorCode agentdomain.ErrorCode,
	errorMessage string,
) error {
	startedAt := r.now()
	err := r.next.MarkRunFailed(ctx, runID, leaseToken, errorCode, errorMessage)
	attributes := []slog.Attr{
		slog.String("event", "agent_run_terminal"),
		slog.Int64("agent_run_id", runID),
		slog.Int64("duration_ms", r.now().Sub(startedAt).Milliseconds()),
	}
	if err == nil {
		attributes = append(attributes, slog.String("status", safeAgentRunStatus(agentdomain.RunStatusFailed)))
	}
	attributes = appendSafeAgentErrorCode(attributes, errorCode)
	r.logRepositoryEvent(ctx, err, attributes, true)
	return err
}

func (r *AgentExecutionRepositoryLogger) CommitFinal(
	ctx context.Context,
	runID int64,
	leaseToken string,
	input agentapplication.CommitFinalInput,
) (agentdomain.Step, error) {
	startedAt := r.now()
	step, err := r.next.CommitFinal(ctx, runID, leaseToken, input)
	attributes := []slog.Attr{
		slog.String("event", "agent_run_terminal"),
		slog.Int64("agent_run_id", runID),
		slog.Int64("duration_ms", r.now().Sub(startedAt).Milliseconds()),
	}
	if err == nil {
		attributes = append(attributes, slog.String("status", safeAgentRunStatus(agentdomain.RunStatusSucceeded)))
		attributes = append(attributes, agentStepCountAttributes(step)...)
	}
	r.logRepositoryEvent(ctx, err, attributes, true)
	return step, err
}

func (r *AgentExecutionRepositoryLogger) RequeueExpiredRuns(
	ctx context.Context,
	limit int,
	errorCode agentdomain.ErrorCode,
	errorMessage string,
) (int64, error) {
	startedAt := r.now()
	recovered, err := r.next.RequeueExpiredRuns(ctx, limit, errorCode, errorMessage)
	attributes := []slog.Attr{
		slog.String("event", "agent_run_recovery"),
		slog.Int64("recovered_count", recovered),
		slog.Int("batch_size", limit),
		slog.Int64("duration_ms", r.now().Sub(startedAt).Milliseconds()),
	}
	attributes = appendSafeAgentErrorCode(attributes, errorCode)
	r.logRepositoryEvent(ctx, err, attributes, true)
	return recovered, err
}

func (r *AgentExecutionRepositoryLogger) ListStepsForWorker(
	ctx context.Context,
	runID int64,
	leaseToken string,
) ([]agentdomain.Step, error) {
	steps, err := r.next.ListStepsForWorker(ctx, runID, leaseToken)
	attributes := []slog.Attr{
		slog.String("event", "agent_steps_loaded"), slog.Int64("agent_run_id", runID),
	}
	if err == nil {
		attributes = append(attributes, aggregateAgentStepAttributes(steps)...)
	}
	r.logRepositoryEvent(ctx, err, attributes, false)
	return steps, err
}

func (r *AgentExecutionRepositoryLogger) ClaimPendingStep(
	ctx context.Context,
	runID int64,
	leaseToken string,
) (agentdomain.Step, error) {
	step, err := r.next.ClaimPendingStep(ctx, runID, leaseToken)
	attributes := []slog.Attr{
		slog.String("event", "agent_step_claim"), slog.Int64("agent_run_id", runID),
	}
	if err == nil {
		attributes = append(attributes, agentStepCountAttributes(step)...)
	}
	r.logRepositoryEvent(ctx, err, attributes, false)
	return step, err
}

func (r *AgentExecutionRepositoryLogger) PersistDecision(
	ctx context.Context,
	runID int64,
	leaseToken string,
	input agentapplication.PersistDecisionInput,
) (agentdomain.Step, error) {
	step, err := r.next.PersistDecision(ctx, runID, leaseToken, input)
	attributes := []slog.Attr{
		slog.String("event", "agent_step_persisted"), slog.Int64("agent_run_id", runID),
	}
	if err == nil {
		attributes = append(attributes, agentStepCountAttributes(step)...)
	}
	r.logRepositoryEvent(ctx, err, attributes, false)
	return step, err
}

func (r *AgentExecutionRepositoryLogger) MarkStepSucceeded(
	ctx context.Context,
	runID int64,
	stepID int64,
	leaseToken string,
	input agentapplication.CompleteStepInput,
) (agentdomain.Step, error) {
	step, err := r.next.MarkStepSucceeded(ctx, runID, stepID, leaseToken, input)
	attributes := []slog.Attr{
		slog.String("event", "agent_step_terminal"), slog.Int64("agent_run_id", runID),
	}
	if err == nil {
		attributes = append(attributes, agentStepCountAttributes(step)...)
	} else {
		attributes = append(attributes, slog.Int64("agent_step_id", stepID))
	}
	r.logRepositoryEvent(ctx, err, attributes, false)
	return step, err
}

func (r *AgentExecutionRepositoryLogger) MarkStepFailed(
	ctx context.Context,
	runID int64,
	stepID int64,
	leaseToken string,
	input agentapplication.FailStepInput,
) (agentdomain.Step, error) {
	step, err := r.next.MarkStepFailed(ctx, runID, stepID, leaseToken, input)
	attributes := []slog.Attr{
		slog.String("event", "agent_step_terminal"), slog.Int64("agent_run_id", runID),
	}
	if err == nil {
		attributes = append(attributes, agentStepCountAttributes(step)...)
		attributes = appendSafeAgentErrorCode(attributes, input.ErrorCode)
	} else {
		attributes = append(attributes, slog.Int64("agent_step_id", stepID))
	}
	r.logRepositoryEvent(ctx, err, attributes, false)
	return step, err
}

func (r *AgentExecutionRepositoryLogger) logRepositoryEvent(
	ctx context.Context,
	err error,
	attributes []slog.Attr,
	includeQueueStats bool,
) {
	if err != nil {
		attributes = append(attributes, slog.String("error_category", safeAgentErrorCategory(err)))
	}
	outcome := "succeeded"
	if err != nil {
		outcome = "failed"
	}
	attributes = append(attributes, slog.String("outcome", outcome))
	if includeQueueStats {
		stats, statsErr := r.stats.GetQueueStats(ctx)
		if statsErr != nil {
			attributes = append(attributes, slog.String("queue_stats_error_category", safeAgentErrorCategory(statsErr)))
		} else {
			attributes = append(attributes,
				slog.Int64("queued_count", stats.QueuedCount),
				slog.Int64("ready_queued_count", stats.ReadyQueuedCount),
				slog.Int64("processing_count", stats.ProcessingCount),
				slog.Int64("max_owner_processing_count", stats.MaxOwnerProcessingCount),
				slog.Int64("oldest_ready_wait_ms", stats.OldestReadyWait.Milliseconds()),
			)
		}
	}
	r.logger.LogAttrs(ctx, safeAgentLogLevel(err), "Agent execution repository event", attributes...)
}

func agentStepCountAttributes(step agentdomain.Step) []slog.Attr {
	return []slog.Attr{
		slog.Int64("agent_step_id", step.ID),
		slog.Int("sequence_no", step.SequenceNo),
		slog.String("kind", safeAgentStepKind(step.Kind)),
		slog.String("status", safeAgentStepStatus(step.Status)),
		slog.Int("prompt_tokens", step.PromptTokens),
		slog.Int("completion_tokens", step.CompletionTokens),
		slog.Int("total_tokens", step.TotalTokens),
	}
}

func aggregateAgentStepAttributes(steps []agentdomain.Step) []slog.Attr {
	promptTokens := 0
	completionTokens := 0
	totalTokens := 0
	for _, step := range steps {
		promptTokens += step.PromptTokens
		completionTokens += step.CompletionTokens
		totalTokens += step.TotalTokens
	}
	return []slog.Attr{
		slog.Int("step_count", len(steps)),
		slog.Int("prompt_tokens", promptTokens),
		slog.Int("completion_tokens", completionTokens),
		slog.Int("total_tokens", totalTokens),
	}
}

func appendSafeAgentErrorCode(attributes []slog.Attr, code agentdomain.ErrorCode) []slog.Attr {
	if code != agentdomain.ErrorCodeNone && code.IsValid() {
		return append(attributes, slog.String("error_code", string(code)))
	}
	return attributes
}

func safeAgentRunStatus(status agentdomain.RunStatus) string {
	if status.IsValid() {
		return string(status)
	}
	return "invalid"
}

func safeAgentStepKind(kind agentdomain.StepKind) string {
	if kind.IsValid() {
		return string(kind)
	}
	return "invalid"
}

func safeAgentStepStatus(status agentdomain.StepStatus) string {
	if status.IsValid() {
		return string(status)
	}
	return "invalid"
}

func safeAgentErrorCategory(err error) string {
	switch {
	case err == nil:
		return ""
	case errors.Is(err, context.Canceled):
		return "canceled"
	case errors.Is(err, context.DeadlineExceeded):
		return "timeout"
	case errors.Is(err, agentapplication.ErrNoQueuedRun):
		return "empty_queue"
	case errors.Is(err, agentapplication.ErrRunLeaseLost):
		return "lease_lost"
	case errors.Is(err, agentapplication.ErrStepLimitExceeded),
		errors.Is(err, agentapplication.ErrTokenBudgetExceeded),
		errors.Is(err, agentapplication.ErrRunDeadlineExceeded):
		return "budget"
	case errors.Is(err, agentapplication.ErrInvalidStep),
		errors.Is(err, agentapplication.ErrFinalStepAlreadyPersisted),
		errors.Is(err, agentapplication.ErrInvalidRecoveryBatch):
		return "invalid"
	default:
		return "internal"
	}
}

func safeAgentLogLevel(err error) slog.Level {
	if err == nil || errors.Is(err, agentapplication.ErrNoQueuedRun) {
		return slog.LevelInfo
	}
	return slog.LevelWarn
}
