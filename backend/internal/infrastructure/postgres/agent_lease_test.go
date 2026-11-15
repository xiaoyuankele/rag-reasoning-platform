package postgres_test

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"sort"
	"testing"
	"time"

	agentapplication "rag-reasoning-platform/backend/internal/application/agent"
	accessdomain "rag-reasoning-platform/backend/internal/domain/access"
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
	firstStep, err := repository.PersistDecision(ctx, claimed.ID, claimed.LeaseToken, agentapplication.PersistDecisionInput{
		ToolName: toolName, Arguments: json.RawMessage(`{"query":"first"}`),
		PromptTokens: 2, CompletionTokens: 1, TotalTokens: 3,
	})
	if err != nil || firstStep.SequenceNo != 1 || firstStep.Status != agentdomain.StepStatusRunning {
		t.Fatalf("persist first agent decision = %+v, %v", firstStep, err)
	}
	if firstStep.TotalTokens != 3 {
		t.Fatalf("first decision tokens = %+v", firstStep)
	}
	if _, err := repository.CommitFinal(ctx, claimed.ID, claimed.LeaseToken, testCommitFinalInput("blocked by active Tool")); !errors.Is(err, agentapplication.ErrInvalidStep) {
		t.Fatalf("final with active Tool error = %v", err)
	}
	if _, err := repository.PersistDecision(ctx, claimed.ID, claimed.LeaseToken, agentapplication.PersistDecisionInput{
		ToolName: toolName, Arguments: json.RawMessage(`{"query":"blocked"}`),
		PromptTokens: 1, CompletionTokens: 1, TotalTokens: 2,
	}); !errors.Is(err, agentapplication.ErrInvalidStep) {
		t.Fatalf("parallel active step error = %v", err)
	}
	firstStep, err = repository.MarkStepSucceeded(ctx, claimed.ID, firstStep.ID, claimed.LeaseToken, agentapplication.CompleteStepInput{
		Result: json.RawMessage(`{"items":["one"]}`),
	})
	if err != nil || firstStep.Status != agentdomain.StepStatusSucceeded || firstStep.TotalTokens != 3 {
		t.Fatalf("complete first agent step = %+v, %v", firstStep, err)
	}
	secondStep, err := repository.PersistDecision(ctx, claimed.ID, claimed.LeaseToken, agentapplication.PersistDecisionInput{
		ToolName: toolName, Arguments: json.RawMessage(`{"query":"second"}`),
		PromptTokens: 1, CompletionTokens: 1, TotalTokens: 2,
	})
	if err != nil || secondStep.SequenceNo != 2 || secondStep.TotalTokens != 2 {
		t.Fatalf("persist second agent decision = %+v, %v", secondStep, err)
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
		agentapplication.MaxRecoveryBatchSize,
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
		steps[1].Status != agentdomain.StepStatusPending || steps[1].TotalTokens != 2 ||
		recoveredRun.StepCount != 2 || recoveredRun.TotalTokens != 5 {
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
	if _, err := repository.PersistDecision(ctx, claimed.ID, claimed.LeaseToken, agentapplication.PersistDecisionInput{
		ToolName: toolName, Arguments: json.RawMessage(`{"query":"stale"}`),
		PromptTokens: 1, CompletionTokens: 1, TotalTokens: 2,
	}); !errors.Is(err, agentapplication.ErrRunLeaseLost) {
		t.Fatalf("stale decision error = %v", err)
	}
	if _, err := repository.ClaimPendingStep(ctx, claimed.ID, claimed.LeaseToken); !errors.Is(err, agentapplication.ErrRunLeaseLost) {
		t.Fatalf("stale pending claim error = %v", err)
	}
	if _, err := repository.MarkStepSucceeded(
		ctx,
		claimed.ID,
		secondStep.ID,
		claimed.LeaseToken,
		agentapplication.CompleteStepInput{Result: json.RawMessage(`{"stale":true}`)},
	); !errors.Is(err, agentapplication.ErrRunLeaseLost) {
		t.Fatalf("stale Step success error = %v", err)
	}
	if err := repository.RequeueRun(
		ctx,
		claimed.ID,
		claimed.LeaseToken,
		time.Now(),
		agentdomain.ErrorCodeWorkerInterrupted,
		"stale worker",
	); !errors.Is(err, agentapplication.ErrRunLeaseLost) {
		t.Fatalf("stale requeue error = %v", err)
	}
	if _, err := repository.CommitFinal(ctx, claimed.ID, claimed.LeaseToken, testCommitFinalInput("stale final")); !errors.Is(err, agentapplication.ErrRunLeaseLost) {
		t.Fatalf("stale final error = %v", err)
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
		Result: json.RawMessage(`{"items":["two"]}`),
	}); err != nil {
		t.Fatalf("complete resumed agent step: %v", err)
	}
	finalStep, err := repository.CommitFinal(ctx, secondClaim.ID, secondClaim.LeaseToken, agentapplication.CommitFinalInput{
		Result: agentapplication.FinalStepResult{
			Answer: "Done.", ResponseLanguage: agentdomain.ResponseLanguageEnglish,
			Payload: json.RawMessage(`{"sources":[]}`),
		},
		PromptTokens: 1, CompletionTokens: 2, TotalTokens: 3,
	})
	if err != nil || finalStep.SequenceNo != 3 || finalStep.Status != agentdomain.StepStatusSucceeded {
		t.Fatalf("commit final agent decision = %+v, %v", finalStep, err)
	}
	if _, err := repository.CommitFinal(ctx, secondClaim.ID, secondClaim.LeaseToken, agentapplication.CommitFinalInput{
		Result:       agentapplication.FinalStepResult{Answer: "duplicate", ResponseLanguage: agentdomain.ResponseLanguageEnglish},
		PromptTokens: 1, CompletionTokens: 1, TotalTokens: 2,
	}); !errors.Is(err, agentapplication.ErrRunLeaseLost) {
		t.Fatalf("second final commit error = %v", err)
	}
	if _, err := repository.PersistDecision(ctx, secondClaim.ID, secondClaim.LeaseToken, agentapplication.PersistDecisionInput{
		ToolName: toolName, Arguments: json.RawMessage(`{"query":"after final"}`),
		PromptTokens: 1, CompletionTokens: 1, TotalTokens: 2,
	}); !errors.Is(err, agentapplication.ErrRunLeaseLost) {
		t.Fatalf("decision after final error = %v", err)
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
	step, err := repository.PersistDecision(ctx, first.ID, first.LeaseToken, agentapplication.PersistDecisionInput{
		ToolName: toolName, Arguments: json.RawMessage(`{"query":"retry"}`),
		PromptTokens: 2, CompletionTokens: 1, TotalTokens: 3,
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
	if err != nil || len(steps) != 1 || steps[0].ID != step.ID ||
		steps[0].Status != agentdomain.StepStatusPending || steps[0].TotalTokens != 3 || requeued.TotalTokens != 3 {
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
	directStep, err := repository.PersistDecision(ctx, directClaim.ID, directClaim.LeaseToken, agentapplication.PersistDecisionInput{
		ToolName: toolName, Arguments: json.RawMessage(`{"query":"fail"}`),
		PromptTokens: 1, CompletionTokens: 1, TotalTokens: 2,
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
	failedStep, err := repository.PersistDecision(ctx, stepFailureClaim.ID, stepFailureClaim.LeaseToken, agentapplication.PersistDecisionInput{
		ToolName: toolName, Arguments: json.RawMessage(`{"query":"tool failure"}`),
		PromptTokens: 2, CompletionTokens: 1, TotalTokens: 3,
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
		},
	)
	if err != nil || failedStep.Status != agentdomain.StepStatusFailed || failedStep.TotalTokens != 3 {
		t.Fatalf("failed tool step = %+v, %v", failedStep, err)
	}
	if _, err := repository.CommitFinal(ctx, stepFailureClaim.ID, stepFailureClaim.LeaseToken, testCommitFinalInput("blocked by failed Tool")); !errors.Is(err, agentapplication.ErrInvalidStep) {
		t.Fatalf("final with failed Tool error = %v", err)
	}
	if _, err := repository.PersistDecision(ctx, stepFailureClaim.ID, stepFailureClaim.LeaseToken, agentapplication.PersistDecisionInput{
		ToolName: toolName, Arguments: json.RawMessage(`{"query":"after failure"}`),
		PromptTokens: 1, CompletionTokens: 1, TotalTokens: 2,
	}); !errors.Is(err, agentapplication.ErrInvalidStep) {
		t.Fatalf("append after failed step error = %v", err)
	}
	stepFailureRun, err := repository.GetRunByID(ctx, owner, stepFailureClaim.ID)
	if err != nil || stepFailureRun.TotalTokens != 3 {
		t.Fatalf("failed-step token aggregate = %+v, %v", stepFailureRun, err)
	}
}

func TestAgentCancelRecoveredRunClosesPendingDecision(t *testing.T) {
	if os.Getenv("RUN_DATABASE_TESTS") != "1" {
		t.Skip("set RUN_DATABASE_TESTS=1 to run PostgreSQL integration tests")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	pool := openIsolatedDocumentTestPool(t, ctx)
	owner := answerJobTestOwnerScope(t, ctx, pool, "agent-cancel-recovered")
	repository := newAgentTestRepository(pool, "agent-worker-cancel-recovered")
	conversation := createAgentTestConversation(t, ctx, pool, owner, "cancel recovered")
	queueAgentTestRun(t, ctx, repository, owner, conversation.ID, "cancel-recovered")
	claimed, err := repository.ClaimNextRun(ctx)
	if err != nil {
		t.Fatalf("claim cancel-recovered Run: %v", err)
	}
	step, err := repository.PersistDecision(ctx, claimed.ID, claimed.LeaseToken, agentapplication.PersistDecisionInput{
		ToolName: "search_documents", Arguments: json.RawMessage(`{"query":"cancel"}`),
		PromptTokens: 2, CompletionTokens: 1, TotalTokens: 3,
	})
	if err != nil {
		t.Fatalf("persist cancel-recovered decision: %v", err)
	}
	if err := repository.RequeueRun(
		ctx,
		claimed.ID,
		claimed.LeaseToken,
		time.Now().Add(-time.Second),
		agentdomain.ErrorCodeTemporarilyUnavailable,
		"temporary capacity failure",
	); err != nil {
		t.Fatalf("requeue cancel-recovered Run: %v", err)
	}
	canceled, err := repository.CancelRun(ctx, owner, claimed.ID)
	if err != nil || canceled.Status != agentdomain.RunStatusCanceled || canceled.TotalTokens != 3 {
		t.Fatalf("cancel recovered Run = %+v, %v", canceled, err)
	}
	steps, err := repository.ListSteps(ctx, owner, claimed.ID)
	if err != nil || len(steps) != 1 || steps[0].ID != step.ID ||
		steps[0].Status != agentdomain.StepStatusFailed || steps[0].TotalTokens != 3 {
		t.Fatalf("canceled recovered decision = %+v, %v", steps, err)
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
	step, err := repository.PersistDecision(ctx, first.ID, first.LeaseToken, agentapplication.PersistDecisionInput{
		ToolName: toolName, Arguments: json.RawMessage(`{"query":"resume"}`),
		PromptTokens: 2, CompletionTokens: 1, TotalTokens: 3,
	})
	if err != nil {
		t.Fatalf("start max-attempt step: %v", err)
	}
	if _, err := pool.Exec(ctx, `UPDATE agent_runs SET lease_expires_at = CURRENT_TIMESTAMP - INTERVAL '1 second' WHERE id = $1`, first.ID); err != nil {
		t.Fatalf("expire first max-attempt lease: %v", err)
	}
	if recovered, err := repository.RequeueExpiredRuns(ctx, agentapplication.MaxRecoveryBatchSize, agentdomain.ErrorCodeWorkerInterrupted, "worker interrupted"); err != nil || recovered != 1 {
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
	if recovered, err := repository.RequeueExpiredRuns(ctx, agentapplication.MaxRecoveryBatchSize, agentdomain.ErrorCodeWorkerInterrupted, "worker interrupted"); err != nil || recovered != 1 {
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

func TestAgentExpiredRecoveryIsBoundedAndPreservesDecisionAccounting(t *testing.T) {
	if os.Getenv("RUN_DATABASE_TESTS") != "1" {
		t.Skip("set RUN_DATABASE_TESTS=1 to run PostgreSQL integration tests")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	pool := openIsolatedDocumentTestPool(t, ctx)
	repository := newAgentTestRepository(pool, "agent-worker-batch-recovery")
	type fixture struct {
		owner accessdomain.OwnerScope
		run   agentdomain.Run
		step  agentdomain.Step
	}
	fixtures := make([]fixture, 0, 3)
	ownersByID := make(map[int64]accessdomain.OwnerScope, 3)
	for index := range 3 {
		owner := answerJobTestOwnerScope(t, ctx, pool, fmt.Sprintf("agent-batch-%d", index))
		ownersByID[owner.OwnerUserID()] = owner
		conversation := createAgentTestConversation(t, ctx, pool, owner, fmt.Sprintf("batch %d", index))
		queueAgentTestRun(t, ctx, repository, owner, conversation.ID, fmt.Sprintf("batch-run-%d", index))
		fixtures = append(fixtures, fixture{})
	}
	for index := range fixtures {
		claimed, err := repository.ClaimNextRun(ctx)
		if err != nil {
			t.Fatalf("claim batch recovery run %d: %v", index, err)
		}
		step, err := repository.PersistDecision(ctx, claimed.ID, claimed.LeaseToken, agentapplication.PersistDecisionInput{
			ToolName: "search_documents", Arguments: json.RawMessage(fmt.Sprintf(`{"query":"batch-%d"}`, index)),
			PromptTokens: index + 1, CompletionTokens: 1, TotalTokens: index + 2,
		})
		if err != nil {
			t.Fatalf("persist batch decision %d: %v", index, err)
		}
		fixtures[index] = fixture{owner: ownersByID[claimed.OwnerUserID], run: claimed, step: step}
	}
	sort.Slice(fixtures, func(left, right int) bool { return fixtures[left].run.ID < fixtures[right].run.ID })
	if _, err := repository.MarkStepSucceeded(
		ctx,
		fixtures[0].run.ID,
		fixtures[0].step.ID,
		fixtures[0].run.LeaseToken,
		agentapplication.CompleteStepInput{Result: json.RawMessage(`{"items":[]}`)},
	); err != nil {
		t.Fatalf("complete pre-crash batch Step: %v", err)
	}
	for _, item := range fixtures {
		if _, err := pool.Exec(
			ctx,
			`UPDATE agent_runs SET lease_expires_at = CURRENT_TIMESTAMP - INTERVAL '1 second' WHERE id = $1`,
			item.run.ID,
		); err != nil {
			t.Fatalf("expire batch run %d: %v", item.run.ID, err)
		}
	}
	if _, err := repository.RequeueExpiredRuns(ctx, 0, agentdomain.ErrorCodeWorkerInterrupted, "worker interrupted"); !errors.Is(err, agentapplication.ErrInvalidRecoveryBatch) {
		t.Fatalf("zero recovery batch error = %v", err)
	}
	if _, err := repository.RequeueExpiredRuns(ctx, agentapplication.MaxRecoveryBatchSize+1, agentdomain.ErrorCodeWorkerInterrupted, "worker interrupted"); !errors.Is(err, agentapplication.ErrInvalidRecoveryBatch) {
		t.Fatalf("oversized recovery batch error = %v", err)
	}
	if recovered, err := repository.RequeueExpiredRuns(ctx, 2, agentdomain.ErrorCodeWorkerInterrupted, "worker interrupted"); err != nil || recovered != 2 {
		t.Fatalf("first bounded recovery = %d, %v", recovered, err)
	}
	for index, item := range fixtures {
		run, err := repository.GetRunByID(ctx, item.owner, item.run.ID)
		if err != nil {
			t.Fatalf("get bounded recovery run %d: %v", item.run.ID, err)
		}
		wantStatus := agentdomain.RunStatusQueued
		if index == 2 {
			wantStatus = agentdomain.RunStatusProcessing
		}
		if run.Status != wantStatus || run.AttemptCount != 1 || run.StepCount != 1 ||
			run.TotalTokens != item.step.TotalTokens {
			t.Fatalf("bounded recovery run %d = %+v, want status %s", item.run.ID, run, wantStatus)
		}
		steps, err := repository.ListSteps(ctx, item.owner, item.run.ID)
		if err != nil || len(steps) != 1 || steps[0].TotalTokens != item.step.TotalTokens {
			t.Fatalf("bounded recovery steps %d = %+v, %v", item.run.ID, steps, err)
		}
		wantStepStatus := agentdomain.StepStatusPending
		if index == 0 {
			wantStepStatus = agentdomain.StepStatusSucceeded
		}
		if index == 2 {
			wantStepStatus = agentdomain.StepStatusRunning
		}
		if steps[0].Status != wantStepStatus {
			t.Fatalf("bounded recovery Step %d status = %s, want %s", item.run.ID, steps[0].Status, wantStepStatus)
		}
	}
	if recovered, err := repository.RequeueExpiredRuns(ctx, 2, agentdomain.ErrorCodeWorkerInterrupted, "worker interrupted"); err != nil || recovered != 1 {
		t.Fatalf("second bounded recovery = %d, %v", recovered, err)
	}
	var succeededClaim agentdomain.Run
	for range 3 {
		claimed, err := repository.ClaimNextRun(ctx)
		if err != nil {
			t.Fatalf("reclaim bounded recovery Run: %v", err)
		}
		if claimed.ID == fixtures[0].run.ID {
			succeededClaim = claimed
		}
	}
	if succeededClaim.ID == 0 {
		t.Fatal("did not reclaim Run with succeeded pre-crash Step")
	}
	nextStep, err := repository.PersistDecision(ctx, succeededClaim.ID, succeededClaim.LeaseToken, agentapplication.PersistDecisionInput{
		ToolName: "search_documents", Arguments: json.RawMessage(`{"query":"after succeeded crash"}`),
		PromptTokens: 1, CompletionTokens: 1, TotalTokens: 2,
	})
	if err != nil || nextStep.SequenceNo != 2 {
		t.Fatalf("next decision after succeeded crash = %+v, %v", nextStep, err)
	}
	reclaimedRun, err := repository.GetRunByID(ctx, fixtures[0].owner, succeededClaim.ID)
	if err != nil || reclaimedRun.StepCount != 2 ||
		reclaimedRun.TotalTokens != fixtures[0].step.TotalTokens+2 {
		t.Fatalf("reclaimed succeeded Run accounting = %+v, %v", reclaimedRun, err)
	}
}
