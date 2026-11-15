package postgres_test

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"testing"
	"time"

	agentapplication "rag-reasoning-platform/backend/internal/application/agent"
	agentdomain "rag-reasoning-platform/backend/internal/domain/agent"
	conversationdomain "rag-reasoning-platform/backend/internal/domain/conversation"
	postgresrepository "rag-reasoning-platform/backend/internal/infrastructure/postgres"
)

func TestAgentLeaseRecoveryPreservesStepsAndFencesStaleWorker(t *testing.T) {
	if os.Getenv("RUN_DATABASE_TESTS") != "1" {
		t.Skip("set RUN_DATABASE_TESTS=1 to run PostgreSQL integration tests")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 45*time.Second)
	defer cancel()
	pool := openIsolatedDocumentTestPool(t, ctx)
	owner := answerJobTestOwnerScope(t, ctx, pool, "agent-lease")
	repository := newAgentTestRepository(pool, "agent-worker-lease")
	conversation := createAgentTestConversation(t, ctx, pool, owner, "lease recovery")
	queued := queueAgentTestRun(t, ctx, repository, owner, conversation.ID, "lease-run")

	claimed, err := repository.ClaimNextRun(ctx)
	if err != nil || claimed.ID != queued.Run.ID || claimed.AttemptCount != 1 || claimed.LeaseToken == "" {
		t.Fatalf("first agent claim = %+v, %v", claimed, err)
	}
	scopedClaimed, err := repository.GetRunByID(ctx, owner, claimed.ID)
	if err != nil || scopedClaimed.LeaseToken != "" || scopedClaimed.WorkerID != "" ||
		scopedClaimed.LeaseExpiresAt != nil || scopedClaimed.HeartbeatAt != nil {
		t.Fatalf("scoped processing run leaked lease capability: %+v, %v", scopedClaimed, err)
	}
	if _, err := pool.Exec(
		ctx,
		`UPDATE agent_runs SET lease_expires_at = CURRENT_TIMESTAMP + INTERVAL '1 second' WHERE id = $1`,
		claimed.ID,
	); err != nil {
		t.Fatalf("shorten renewable agent lease: %v", err)
	}
	if err := repository.RenewRunLease(ctx, claimed.ID, claimed.LeaseToken); err != nil {
		t.Fatalf("renew active agent lease: %v", err)
	}
	var renewedForMoreThanThirtySeconds bool
	if err := pool.QueryRow(
		ctx,
		`SELECT lease_expires_at > CURRENT_TIMESTAMP + INTERVAL '30 seconds'
		 FROM agent_runs WHERE id = $1`,
		claimed.ID,
	).Scan(&renewedForMoreThanThirtySeconds); err != nil || !renewedForMoreThanThirtySeconds {
		t.Fatalf("renewed agent lease valid=%v, error=%v", renewedForMoreThanThirtySeconds, err)
	}
	if _, err := repository.CancelRun(ctx, owner, claimed.ID); !errors.Is(err, agentapplication.ErrRunProcessingCannotCancel) {
		t.Fatalf("processing cancel error = %v", err)
	}
	toolName := "search_documents"
	firstStep, err := repository.StartStep(ctx, claimed.ID, claimed.LeaseToken, agentapplication.StartStepInput{
		Kind: agentdomain.StepKindToolCall, ToolName: &toolName, Arguments: json.RawMessage(`{"query":"first"}`),
	})
	if err != nil || firstStep.SequenceNo != 1 || firstStep.Status != agentdomain.StepStatusRunning {
		t.Fatalf("start first agent step = %+v, %v", firstStep, err)
	}
	if _, err := repository.StartStep(ctx, claimed.ID, claimed.LeaseToken, agentapplication.StartStepInput{
		Kind: agentdomain.StepKindToolCall, ToolName: &toolName, Arguments: json.RawMessage(`{"query":"blocked"}`),
	}); !errors.Is(err, agentapplication.ErrInvalidStep) {
		t.Fatalf("parallel active step error = %v", err)
	}
	firstStep, err = repository.MarkStepSucceeded(ctx, claimed.ID, firstStep.ID, claimed.LeaseToken, agentapplication.CompleteStepInput{
		Result: json.RawMessage(`{"items":["one"]}`), PromptTokens: 2, CompletionTokens: 1, TotalTokens: 3,
	})
	if err != nil || firstStep.Status != agentdomain.StepStatusSucceeded {
		t.Fatalf("complete first agent step = %+v, %v", firstStep, err)
	}
	secondStep, err := repository.StartStep(ctx, claimed.ID, claimed.LeaseToken, agentapplication.StartStepInput{
		Kind: agentdomain.StepKindToolCall, ToolName: &toolName, Arguments: json.RawMessage(`{"query":"second"}`),
	})
	if err != nil || secondStep.SequenceNo != 2 {
		t.Fatalf("start second agent step = %+v, %v", secondStep, err)
	}

	if _, err := pool.Exec(
		ctx,
		`UPDATE agent_runs SET lease_expires_at = CURRENT_TIMESTAMP - INTERVAL '1 second' WHERE id = $1`,
		claimed.ID,
	); err != nil {
		t.Fatalf("expire first agent lease: %v", err)
	}
	if err := repository.RenewRunLease(ctx, claimed.ID, claimed.LeaseToken); !errors.Is(err, agentapplication.ErrRunLeaseLost) {
		t.Fatalf("renew expired lease error = %v", err)
	}
	if _, err := repository.ListStepsForWorker(ctx, claimed.ID, claimed.LeaseToken); !errors.Is(err, agentapplication.ErrRunLeaseLost) {
		t.Fatalf("list with expired lease error = %v", err)
	}
	recovered, err := repository.RequeueExpiredRuns(
		ctx,
		agentdomain.ErrorCodeWorkerInterrupted,
		"agent worker lease expired",
	)
	if err != nil || recovered != 1 {
		t.Fatalf("recover first expired run = %d, %v", recovered, err)
	}
	recoveredRun, err := repository.GetRunByID(ctx, owner, claimed.ID)
	if err != nil || recoveredRun.Status != agentdomain.RunStatusQueued || recoveredRun.AttemptCount != 1 ||
		recoveredRun.LeaseToken != "" {
		t.Fatalf("recovered agent run = %+v, %v", recoveredRun, err)
	}
	steps, err := repository.ListSteps(ctx, owner, claimed.ID)
	if err != nil || len(steps) != 2 || steps[0].Status != agentdomain.StepStatusSucceeded ||
		steps[1].Status != agentdomain.StepStatusPending {
		t.Fatalf("recovered agent steps = %+v, %v", steps, err)
	}
	if _, err := repository.MarkStepFailed(
		ctx,
		claimed.ID,
		secondStep.ID,
		claimed.LeaseToken,
		agentapplication.FailStepInput{
			ErrorCode: agentdomain.ErrorCodeWorkerInterrupted, ErrorMessage: "stale worker",
		},
	); !errors.Is(err, agentapplication.ErrRunLeaseLost) {
		t.Fatalf("stale step failure error = %v", err)
	}

	secondClaim, err := repository.ClaimNextRun(ctx)
	if err != nil || secondClaim.ID != claimed.ID || secondClaim.AttemptCount != 2 ||
		secondClaim.LeaseToken == claimed.LeaseToken {
		t.Fatalf("second agent claim = %+v, %v", secondClaim, err)
	}
	resumed, err := repository.ClaimPendingStep(ctx, secondClaim.ID, secondClaim.LeaseToken)
	if err != nil || resumed.ID != secondStep.ID || resumed.SequenceNo != secondStep.SequenceNo {
		t.Fatalf("resume pending agent step = %+v, %v", resumed, err)
	}
	if _, err := repository.MarkStepSucceeded(ctx, secondClaim.ID, resumed.ID, secondClaim.LeaseToken, agentapplication.CompleteStepInput{
		Result: json.RawMessage(`{"items":["two"]}`), PromptTokens: 1, CompletionTokens: 1, TotalTokens: 2,
	}); err != nil {
		t.Fatalf("complete resumed agent step: %v", err)
	}
	finalStep, err := repository.StartStep(ctx, secondClaim.ID, secondClaim.LeaseToken, agentapplication.StartStepInput{
		Kind: agentdomain.StepKindFinal,
	})
	if err != nil || finalStep.SequenceNo != 3 {
		t.Fatalf("start final agent step = %+v, %v", finalStep, err)
	}
	if _, err := repository.MarkStepSucceeded(ctx, secondClaim.ID, finalStep.ID, secondClaim.LeaseToken, agentapplication.CompleteStepInput{
		Result:       json.RawMessage(`{"answer":"Done.","response_language":"en","payload":{"sources":[]}}`),
		PromptTokens: 1, CompletionTokens: 2, TotalTokens: 3,
	}); err != nil {
		t.Fatalf("complete final agent step: %v", err)
	}
	if _, err := repository.StartStep(ctx, secondClaim.ID, secondClaim.LeaseToken, agentapplication.StartStepInput{
		Kind: agentdomain.StepKindToolCall, ToolName: &toolName, Arguments: json.RawMessage(`{"query":"after final"}`),
	}); !errors.Is(err, agentapplication.ErrFinalStepAlreadyPersisted) {
		t.Fatalf("append after final error = %v", err)
	}
	if err := repository.MarkRunSucceeded(ctx, secondClaim.ID, secondClaim.LeaseToken, agentdomain.Result{
		Answer: "Divergent answer.", ResponseLanguage: agentdomain.ResponseLanguageEnglish,
		Payload: json.RawMessage(`{"sources":[]}`), PromptTokens: 4, CompletionTokens: 4, TotalTokens: 8,
	}); err == nil {
		t.Fatal("run success accepted result that diverged from final step audit")
	}
	if err := repository.MarkRunSucceeded(ctx, secondClaim.ID, secondClaim.LeaseToken, agentdomain.Result{
		Answer: "Done.", ResponseLanguage: agentdomain.ResponseLanguageEnglish,
		Payload: json.RawMessage(`{"sources":[]}`), PromptTokens: 4, CompletionTokens: 4, TotalTokens: 8,
	}); err != nil {
		t.Fatalf("mark agent run succeeded: %v", err)
	}
	succeeded, err := repository.GetRunByID(ctx, owner, secondClaim.ID)
	if err != nil || succeeded.Status != agentdomain.RunStatusSucceeded || succeeded.Result == nil ||
		succeeded.LeaseToken != "" || succeeded.TotalTokens != 8 {
		t.Fatalf("succeeded agent run = %+v, %v", succeeded, err)
	}
	messages, err := postgresrepository.NewConversationRepository(pool).ListMessages(
		ctx,
		owner,
		conversation.ID,
		conversationdomain.ListOptions{Limit: 10},
	)
	if err != nil || len(messages.Messages) != 2 ||
		messages.Messages[1].Status != conversationdomain.MessageStatusCompleted ||
		messages.Messages[1].Content != "Done." {
		t.Fatalf("succeeded agent messages = %+v, %v", messages.Messages, err)
	}
	if err := repository.MarkRunFailed(
		ctx,
		secondClaim.ID,
		secondClaim.LeaseToken,
		agentdomain.ErrorCodeExecutionFailed,
		"stale terminal write",
	); !errors.Is(err, agentapplication.ErrRunLeaseLost) {
		t.Fatalf("stale terminal write error = %v", err)
	}
}

func TestAgentExplicitRequeueHonorsAttemptBound(t *testing.T) {
	if os.Getenv("RUN_DATABASE_TESTS") != "1" {
		t.Skip("set RUN_DATABASE_TESTS=1 to run PostgreSQL integration tests")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	pool := openIsolatedDocumentTestPool(t, ctx)
	owner := answerJobTestOwnerScope(t, ctx, pool, "agent-explicit-requeue")
	repository := newAgentTestRepository(pool, "agent-worker-explicit-requeue")
	conversation := createAgentTestConversation(t, ctx, pool, owner, "explicit requeue")
	queued := queueAgentTestRun(t, ctx, repository, owner, conversation.ID, "explicit-requeue-run")
	toolName := "search_documents"

	first, err := repository.ClaimNextRun(ctx)
	if err != nil {
		t.Fatalf("claim explicit requeue first attempt: %v", err)
	}
	step, err := repository.StartStep(ctx, first.ID, first.LeaseToken, agentapplication.StartStepInput{
		Kind: agentdomain.StepKindToolCall, ToolName: &toolName, Arguments: json.RawMessage(`{"query":"retry"}`),
	})
	if err != nil {
		t.Fatalf("start explicit requeue step: %v", err)
	}
	if err := repository.RequeueRun(
		ctx,
		first.ID,
		first.LeaseToken,
		time.Now().Add(-time.Second),
		agentdomain.ErrorCodeTemporarilyUnavailable,
		"capacity coordination unavailable",
	); err != nil {
		t.Fatalf("explicitly requeue first attempt: %v", err)
	}
	requeued, err := repository.GetRunByID(ctx, owner, first.ID)
	if err != nil || requeued.Status != agentdomain.RunStatusQueued || requeued.AttemptCount != 1 {
		t.Fatalf("explicitly requeued run = %+v, %v", requeued, err)
	}
	steps, err := repository.ListSteps(ctx, owner, first.ID)
	if err != nil || len(steps) != 1 || steps[0].ID != step.ID || steps[0].Status != agentdomain.StepStatusPending {
		t.Fatalf("explicitly requeued steps = %+v, %v", steps, err)
	}
	second, err := repository.ClaimNextRun(ctx)
	if err != nil || second.ID != queued.Run.ID || second.AttemptCount != 2 {
		t.Fatalf("claim explicit requeue second attempt = %+v, %v", second, err)
	}
	if err := repository.RequeueRun(
		ctx,
		second.ID,
		second.LeaseToken,
		time.Now(),
		agentdomain.ErrorCodeTemporarilyUnavailable,
		"capacity coordination unavailable",
	); err != nil {
		t.Fatalf("terminally requeue second attempt: %v", err)
	}
	failed, err := repository.GetRunByID(ctx, owner, second.ID)
	if err != nil || failed.Status != agentdomain.RunStatusFailed || failed.CompletedAt == nil {
		t.Fatalf("explicit requeue terminal run = %+v, %v", failed, err)
	}
	steps, err = repository.ListSteps(ctx, owner, second.ID)
	if err != nil || len(steps) != 1 || steps[0].Status != agentdomain.StepStatusFailed {
		t.Fatalf("explicit requeue terminal steps = %+v, %v", steps, err)
	}
}

func TestAgentFailureFinalizesActiveStepAndFailedStepStopsExecution(t *testing.T) {
	if os.Getenv("RUN_DATABASE_TESTS") != "1" {
		t.Skip("set RUN_DATABASE_TESTS=1 to run PostgreSQL integration tests")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	pool := openIsolatedDocumentTestPool(t, ctx)
	owner := answerJobTestOwnerScope(t, ctx, pool, "agent-terminal-step")
	repository := newAgentTestRepository(pool, "agent-worker-terminal-step")
	toolName := "search_documents"

	directConversation := createAgentTestConversation(t, ctx, pool, owner, "direct failure")
	queueAgentTestRun(t, ctx, repository, owner, directConversation.ID, "direct-failure")
	directClaim, err := repository.ClaimNextRun(ctx)
	if err != nil {
		t.Fatalf("claim direct-failure run: %v", err)
	}
	directStep, err := repository.StartStep(ctx, directClaim.ID, directClaim.LeaseToken, agentapplication.StartStepInput{
		Kind: agentdomain.StepKindToolCall, ToolName: &toolName, Arguments: json.RawMessage(`{"query":"fail"}`),
	})
	if err != nil {
		t.Fatalf("start direct-failure step: %v", err)
	}
	if err := repository.MarkRunFailed(
		ctx,
		directClaim.ID,
		directClaim.LeaseToken,
		agentdomain.ErrorCodeExecutionFailed,
		"safe terminal failure",
	); err != nil {
		t.Fatalf("directly fail run with active step: %v", err)
	}
	directSteps, err := repository.ListSteps(ctx, owner, directClaim.ID)
	if err != nil || len(directSteps) != 1 || directSteps[0].ID != directStep.ID ||
		directSteps[0].Status != agentdomain.StepStatusFailed {
		t.Fatalf("direct-failure steps = %+v, %v", directSteps, err)
	}

	stepFailureConversation := createAgentTestConversation(t, ctx, pool, owner, "step failure")
	queueAgentTestRun(t, ctx, repository, owner, stepFailureConversation.ID, "step-failure")
	stepFailureClaim, err := repository.ClaimNextRun(ctx)
	if err != nil {
		t.Fatalf("claim step-failure run: %v", err)
	}
	failedStep, err := repository.StartStep(ctx, stepFailureClaim.ID, stepFailureClaim.LeaseToken, agentapplication.StartStepInput{
		Kind: agentdomain.StepKindToolCall, ToolName: &toolName, Arguments: json.RawMessage(`{"query":"tool failure"}`),
	})
	if err != nil {
		t.Fatalf("start failed tool step: %v", err)
	}
	failedStep, err = repository.MarkStepFailed(
		ctx,
		stepFailureClaim.ID,
		failedStep.ID,
		stepFailureClaim.LeaseToken,
		agentapplication.FailStepInput{
			ErrorCode: agentdomain.ErrorCodeToolFailed, ErrorMessage: "safe tool failure",
			PromptTokens: 2, CompletionTokens: 1, TotalTokens: 3,
		},
	)
	if err != nil || failedStep.Status != agentdomain.StepStatusFailed || failedStep.TotalTokens != 3 {
		t.Fatalf("failed tool step = %+v, %v", failedStep, err)
	}
	if _, err := repository.StartStep(ctx, stepFailureClaim.ID, stepFailureClaim.LeaseToken, agentapplication.StartStepInput{
		Kind: agentdomain.StepKindFinal,
	}); !errors.Is(err, agentapplication.ErrInvalidStep) {
		t.Fatalf("append after failed step error = %v", err)
	}
	stepFailureRun, err := repository.GetRunByID(ctx, owner, stepFailureClaim.ID)
	if err != nil || stepFailureRun.TotalTokens != 3 {
		t.Fatalf("failed-step token aggregate = %+v, %v", stepFailureRun, err)
	}
}

func TestAgentSecondExpiredAttemptFailsRunAndAssistant(t *testing.T) {
	if os.Getenv("RUN_DATABASE_TESTS") != "1" {
		t.Skip("set RUN_DATABASE_TESTS=1 to run PostgreSQL integration tests")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	pool := openIsolatedDocumentTestPool(t, ctx)
	owner := answerJobTestOwnerScope(t, ctx, pool, "agent-max-attempt")
	repository := newAgentTestRepository(pool, "agent-worker-max-attempt")
	conversation := createAgentTestConversation(t, ctx, pool, owner, "max attempts")
	queued := queueAgentTestRun(t, ctx, repository, owner, conversation.ID, "max-attempt-run")
	toolName := "search_documents"

	first, err := repository.ClaimNextRun(ctx)
	if err != nil {
		t.Fatalf("claim max-attempt run first time: %v", err)
	}
	step, err := repository.StartStep(ctx, first.ID, first.LeaseToken, agentapplication.StartStepInput{
		Kind: agentdomain.StepKindToolCall, ToolName: &toolName, Arguments: json.RawMessage(`{"query":"resume"}`),
	})
	if err != nil {
		t.Fatalf("start max-attempt step: %v", err)
	}
	if _, err := pool.Exec(ctx, `UPDATE agent_runs SET lease_expires_at = CURRENT_TIMESTAMP - INTERVAL '1 second' WHERE id = $1`, first.ID); err != nil {
		t.Fatalf("expire first max-attempt lease: %v", err)
	}
	if recovered, err := repository.RequeueExpiredRuns(ctx, agentdomain.ErrorCodeWorkerInterrupted, "worker interrupted"); err != nil || recovered != 1 {
		t.Fatalf("recover first max-attempt lease = %d, %v", recovered, err)
	}
	second, err := repository.ClaimNextRun(ctx)
	if err != nil || second.ID != queued.Run.ID || second.AttemptCount != 2 {
		t.Fatalf("claim max-attempt run second time = %+v, %v", second, err)
	}
	resumed, err := repository.ClaimPendingStep(ctx, second.ID, second.LeaseToken)
	if err != nil || resumed.ID != step.ID {
		t.Fatalf("resume max-attempt step = %+v, %v", resumed, err)
	}
	if _, err := pool.Exec(ctx, `UPDATE agent_runs SET lease_expires_at = CURRENT_TIMESTAMP - INTERVAL '1 second' WHERE id = $1`, second.ID); err != nil {
		t.Fatalf("expire second max-attempt lease: %v", err)
	}
	if recovered, err := repository.RequeueExpiredRuns(ctx, agentdomain.ErrorCodeWorkerInterrupted, "worker interrupted"); err != nil || recovered != 1 {
		t.Fatalf("recover second max-attempt lease = %d, %v", recovered, err)
	}
	failed, err := repository.GetRunByID(ctx, owner, second.ID)
	if err != nil || failed.Status != agentdomain.RunStatusFailed ||
		failed.ErrorCode != agentdomain.ErrorCodeWorkerInterrupted || failed.CompletedAt == nil || failed.LeaseToken != "" {
		t.Fatalf("max-attempt failed run = %+v, %v", failed, err)
	}
	steps, err := repository.ListSteps(ctx, owner, second.ID)
	if err != nil || len(steps) != 1 || steps[0].Status != agentdomain.StepStatusFailed {
		t.Fatalf("max-attempt recovered steps = %+v, %v", steps, err)
	}
	messages, err := postgresrepository.NewConversationRepository(pool).ListMessages(
		ctx,
		owner,
		conversation.ID,
		conversationdomain.ListOptions{Limit: 10},
	)
	if err != nil || len(messages.Messages) != 2 ||
		messages.Messages[1].Status != conversationdomain.MessageStatusFailed {
		t.Fatalf("max-attempt failed messages = %+v, %v", messages.Messages, err)
	}
}
