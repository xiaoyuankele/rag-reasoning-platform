package observability

import (
	"context"
	"encoding/json"
	"errors"
	"log/slog"
	"strings"
	"testing"
	"time"

	agentapplication "rag-reasoning-platform/backend/internal/application/agent"
	agentdomain "rag-reasoning-platform/backend/internal/domain/agent"
)

type fakeObservedAgentRepository struct {
	run              agentdomain.Run
	step             agentdomain.Step
	steps            []agentdomain.Step
	stats            agentdomain.QueueStats
	statsErr         error
	renewErr         error
	requeueErr       error
	calls            map[string]int
	lastLeaseToken   string
	lastErrorMessage string
	lastArguments    string
	lastResult       string
	lastAnswer       string
}

func (f *fakeObservedAgentRepository) record(name string) {
	if f.calls == nil {
		f.calls = make(map[string]int)
	}
	f.calls[name]++
}

func (f *fakeObservedAgentRepository) ClaimNextRun(context.Context) (agentdomain.Run, error) {
	f.record("claim")
	return f.run, nil
}

func (f *fakeObservedAgentRepository) GetQueueStats(context.Context) (agentdomain.QueueStats, error) {
	f.record("stats")
	return f.stats, f.statsErr
}

func (f *fakeObservedAgentRepository) RenewRunLease(_ context.Context, _ int64, leaseToken string) error {
	f.record("renew")
	f.lastLeaseToken = leaseToken
	return f.renewErr
}

func (f *fakeObservedAgentRepository) RequeueRun(_ context.Context, _ int64, leaseToken string, _ time.Time, _ agentdomain.ErrorCode, message string) error {
	f.record("requeue")
	f.lastLeaseToken = leaseToken
	f.lastErrorMessage = message
	return f.requeueErr
}

func (f *fakeObservedAgentRepository) MarkRunFailed(_ context.Context, _ int64, leaseToken string, _ agentdomain.ErrorCode, message string) error {
	f.record("run_failed")
	f.lastLeaseToken = leaseToken
	f.lastErrorMessage = message
	return nil
}

func (f *fakeObservedAgentRepository) CommitFinal(_ context.Context, _ int64, leaseToken string, input agentapplication.CommitFinalInput) (agentdomain.Step, error) {
	f.record("final")
	f.lastLeaseToken = leaseToken
	f.lastAnswer = input.Result.Answer
	return f.step, nil
}

func (f *fakeObservedAgentRepository) RequeueExpiredRuns(context.Context, int, agentdomain.ErrorCode, string) (int64, error) {
	f.record("recovery")
	return 2, nil
}

func (f *fakeObservedAgentRepository) ListStepsForWorker(context.Context, int64, string) ([]agentdomain.Step, error) {
	f.record("list_steps")
	return append([]agentdomain.Step(nil), f.steps...), nil
}

func (f *fakeObservedAgentRepository) ClaimPendingStep(context.Context, int64, string) (agentdomain.Step, error) {
	f.record("claim_step")
	return f.step, nil
}

func (f *fakeObservedAgentRepository) PersistDecision(_ context.Context, _ int64, leaseToken string, input agentapplication.PersistDecisionInput) (agentdomain.Step, error) {
	f.record("persist")
	f.lastLeaseToken = leaseToken
	f.lastArguments = string(input.Arguments)
	return f.step, nil
}

func (f *fakeObservedAgentRepository) MarkStepSucceeded(_ context.Context, _ int64, _ int64, leaseToken string, input agentapplication.CompleteStepInput) (agentdomain.Step, error) {
	f.record("step_succeeded")
	f.lastLeaseToken = leaseToken
	f.lastResult = string(input.Result)
	return f.step, nil
}

func (f *fakeObservedAgentRepository) MarkStepFailed(_ context.Context, _ int64, _ int64, leaseToken string, input agentapplication.FailStepInput) (agentdomain.Step, error) {
	f.record("step_failed")
	f.lastLeaseToken = leaseToken
	f.lastErrorMessage = input.ErrorMessage
	step := f.step
	step.Status = agentdomain.StepStatusFailed
	step.ErrorCode = input.ErrorCode
	return step, nil
}

func TestAgentExecutionRepositoryLoggerRecordsCountsWithoutPayloadsOrIdentity(t *testing.T) {
	var output lockedAgentLogBuffer
	now := time.Date(2026, 11, 16, 12, 0, 10, 0, time.UTC)
	toolName := "search_documents"
	secretMessage := "SQL-secret provider-body-secret"
	step := agentdomain.Step{
		ID: 9, RunID: 7, SequenceNo: 1, Kind: agentdomain.StepKindToolCall,
		Status: agentdomain.StepStatusSucceeded, ToolName: &toolName,
		Arguments:    json.RawMessage(`{"query":"prompt-secret arguments-secret"}`),
		Result:       json.RawMessage(`{"excerpt":"evidence-secret result-secret"}`),
		PromptTokens: 2, CompletionTokens: 1, TotalTokens: 3,
	}
	repository := &fakeObservedAgentRepository{
		run: agentdomain.Run{
			ID: 7, OwnerUserID: 987654321, WorkerID: "worker-secret", LeaseToken: "lease-secret",
			Status: agentdomain.RunStatusProcessing, AttemptCount: 1, StepCount: 1,
			PromptTokens: 2, CompletionTokens: 1, TotalTokens: 3,
			CreatedAt: now.Add(-20 * time.Second), NextAttemptAt: now.Add(-5 * time.Second),
			ErrorMessage: &secretMessage,
		},
		step: step, steps: []agentdomain.Step{step},
		stats: agentdomain.QueueStats{
			QueuedCount: 4, ReadyQueuedCount: 3, ProcessingCount: 2,
			MaxOwnerProcessingCount: 1, OldestReadyWait: 9 * time.Second,
		},
	}
	logger := NewAgentExecutionRepositoryLogger(repository, repository, slog.New(slog.NewJSONHandler(&output, nil)))
	logger.now = func() time.Time { return now }

	if _, err := logger.ClaimNextRun(t.Context()); err != nil {
		t.Fatal(err)
	}
	if _, err := logger.ListStepsForWorker(t.Context(), 7, "lease-secret"); err != nil {
		t.Fatal(err)
	}
	if err := logger.RenewRunLease(t.Context(), 7, "lease-secret"); err != nil {
		t.Fatal(err)
	}
	if _, err := logger.ClaimPendingStep(t.Context(), 7, "lease-secret"); err != nil {
		t.Fatal(err)
	}
	if _, err := logger.PersistDecision(t.Context(), 7, "lease-secret", agentapplication.PersistDecisionInput{
		ToolName: toolName, Arguments: step.Arguments, PromptTokens: 2, CompletionTokens: 1, TotalTokens: 3,
	}); err != nil {
		t.Fatal(err)
	}
	if _, err := logger.MarkStepSucceeded(t.Context(), 7, 9, "lease-secret", agentapplication.CompleteStepInput{Result: step.Result}); err != nil {
		t.Fatal(err)
	}
	if _, err := logger.MarkStepFailed(t.Context(), 7, 9, "lease-secret", agentapplication.FailStepInput{
		ErrorCode: agentdomain.ErrorCodeToolFailed, ErrorMessage: secretMessage,
	}); err != nil {
		t.Fatal(err)
	}
	if err := logger.RequeueRun(t.Context(), 7, "lease-secret", now, agentdomain.ErrorCodeTemporarilyUnavailable, secretMessage); err != nil {
		t.Fatal(err)
	}
	if _, err := logger.RequeueExpiredRuns(t.Context(), 100, agentdomain.ErrorCodeWorkerInterrupted, secretMessage); err != nil {
		t.Fatal(err)
	}
	if _, err := logger.CommitFinal(t.Context(), 7, "lease-secret", agentapplication.CommitFinalInput{
		Result: agentapplication.FinalStepResult{Answer: "answer-secret", Payload: json.RawMessage(`{"sources":["evidence-secret"]}`)},
	}); err != nil {
		t.Fatal(err)
	}
	if err := logger.MarkRunFailed(t.Context(), 7, "lease-secret", agentdomain.ErrorCodeToolFailed, secretMessage); err != nil {
		t.Fatal(err)
	}
	for _, call := range []string{
		"claim", "list_steps", "renew", "claim_step", "persist", "step_succeeded", "step_failed",
		"requeue", "recovery", "final", "run_failed",
	} {
		if repository.calls[call] != 1 {
			t.Fatalf("delegate call %q = %d, want 1", call, repository.calls[call])
		}
	}
	if repository.lastLeaseToken != "lease-secret" || repository.lastErrorMessage != secretMessage ||
		repository.lastArguments != string(step.Arguments) || repository.lastResult != string(step.Result) ||
		repository.lastAnswer != "answer-secret" {
		t.Fatalf("delegate inputs changed: %+v", repository)
	}

	logs := output.String()
	for _, required := range []string{
		`"event":"agent_run_claim"`, `"queue_wait_ms":5000`, `"queued_count":4`,
		`"event":"agent_run_recovery"`, `"recovered_count":2`,
		`"event":"agent_run_terminal"`, `"step_count":1`, `"total_tokens":3`,
		`"agent_run_id":7`, `"agent_step_id":9`, `"error_code":"agent_tool_failed"`,
	} {
		if !strings.Contains(logs, required) {
			t.Fatalf("logs missing %q: %s", required, logs)
		}
	}
	for _, forbidden := range []string{
		"987654321", "worker-secret", "lease-secret", "SQL-secret", "provider-body-secret",
		"prompt-secret", "arguments-secret", "result-secret", "evidence-secret", "answer-secret",
		"owner_user_id", "arguments", "result_json", "error_message",
	} {
		if strings.Contains(logs, forbidden) {
			t.Fatalf("logs leaked %q: %s", forbidden, logs)
		}
	}
}

func TestAgentExecutionRepositoryLoggerUsesOnlySafeErrorCategories(t *testing.T) {
	var output lockedAgentLogBuffer
	repository := &fakeObservedAgentRepository{
		renewErr:   errors.New("redis-sql-secret"),
		requeueErr: errors.New("requeue-write-secret"),
		statsErr:   errors.New("queue-stats-secret"),
	}
	logger := NewAgentExecutionRepositoryLogger(repository, repository, slog.New(slog.NewJSONHandler(&output, nil)))
	if err := logger.RenewRunLease(t.Context(), 7, "lease-secret"); err == nil {
		t.Fatal("RenewRunLease() error = nil")
	}
	if err := logger.RequeueRun(t.Context(), 7, "lease-secret", time.Now(), agentdomain.ErrorCodeTemporarilyUnavailable, "message-secret"); err == nil {
		t.Fatal("RequeueRun() error = nil")
	}
	if _, err := logger.RequeueExpiredRuns(t.Context(), 100, agentdomain.ErrorCodeWorkerInterrupted, "message-secret"); err != nil {
		t.Fatal(err)
	}
	logs := output.String()
	if !strings.Contains(logs, `"error_category":"internal"`) ||
		!strings.Contains(logs, `"queue_stats_error_category":"internal"`) ||
		!strings.Contains(logs, `"outcome":"failed"`) || strings.Contains(logs, `"status":"queued"`) {
		t.Fatalf("safe categories missing: %s", logs)
	}
	for _, forbidden := range []string{"redis-sql-secret", "requeue-write-secret", "queue-stats-secret", "lease-secret", "message-secret"} {
		if strings.Contains(logs, forbidden) {
			t.Fatalf("logs leaked %q: %s", forbidden, logs)
		}
	}
}
