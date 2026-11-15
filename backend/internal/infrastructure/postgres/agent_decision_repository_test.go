package postgres_test

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"sync"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"

	agentapplication "rag-reasoning-platform/backend/internal/application/agent"
	agentdomain "rag-reasoning-platform/backend/internal/domain/agent"
	conversationdomain "rag-reasoning-platform/backend/internal/domain/conversation"
)

func testCommitFinalInput(answer string) agentapplication.CommitFinalInput {
	return agentapplication.CommitFinalInput{
		Result: agentapplication.FinalStepResult{
			Answer: answer, ResponseLanguage: agentdomain.ResponseLanguageEnglish,
			Payload: json.RawMessage(`{"sources":[]}`),
		},
		PromptTokens: 2, CompletionTokens: 3, TotalTokens: 5,
	}
}

func TestAgentCommitFinalRollsBackEveryWriteAndFencesReplay(t *testing.T) {
	if os.Getenv("RUN_DATABASE_TESTS") != "1" {
		t.Skip("set RUN_DATABASE_TESTS=1 to run PostgreSQL integration tests")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	pool := openIsolatedDocumentTestPool(t, ctx)
	owner := answerJobTestOwnerScope(t, ctx, pool, "agent-final-atomic")
	repository := newAgentTestRepository(pool, "agent-worker-final-atomic")
	conversation := createAgentTestConversation(t, ctx, pool, owner, "atomic final")
	queued := queueAgentTestRun(t, ctx, repository, owner, conversation.ID, "atomic-final")
	claimed, err := repository.ClaimNextRun(ctx)
	if err != nil || claimed.ID != queued.Run.ID {
		t.Fatalf("claim atomic final Run = %+v, %v", claimed, err)
	}
	if _, err := pool.Exec(
		ctx,
		`CREATE FUNCTION reject_agent_final_for_test()
		 RETURNS TRIGGER
		 LANGUAGE plpgsql
		 AS $$
		 BEGIN
		   IF NEW.status = 'succeeded' AND OLD.status = 'processing' THEN
		     RAISE EXCEPTION 'injected final commit failure';
		   END IF;
		   RETURN NEW;
		 END
		 $$;
		 CREATE TRIGGER reject_agent_final_for_test
		 BEFORE UPDATE OF status ON agent_runs
		 FOR EACH ROW EXECUTE FUNCTION reject_agent_final_for_test();`,
	); err != nil {
		t.Fatalf("install final rollback trigger: %v", err)
	}
	if _, err := repository.CommitFinal(ctx, claimed.ID, claimed.LeaseToken, testCommitFinalInput("Atomic answer")); err == nil {
		t.Fatal("CommitFinal succeeded despite injected final Run failure")
	}
	var status string
	var stepCount int
	var totalTokens *int
	var leaseToken string
	if err := pool.QueryRow(
		ctx,
		`SELECT status, step_count, total_tokens, lease_token
		 FROM agent_runs WHERE id = $1`,
		claimed.ID,
	).Scan(&status, &stepCount, &totalTokens, &leaseToken); err != nil {
		t.Fatalf("read rolled-back final Run: %v", err)
	}
	if status != "processing" || stepCount != 0 || totalTokens != nil || leaseToken != claimed.LeaseToken {
		t.Fatalf("rolled-back final Run status=%s steps=%d tokens=%v lease=%q", status, stepCount, totalTokens, leaseToken)
	}
	var persistedSteps int
	if err := pool.QueryRow(ctx, `SELECT COUNT(*) FROM agent_steps WHERE agent_run_id = $1`, claimed.ID).Scan(&persistedSteps); err != nil || persistedSteps != 0 {
		t.Fatalf("rolled-back final Step count = %d, %v", persistedSteps, err)
	}
	var assistantStatus string
	var assistantContent string
	if err := pool.QueryRow(
		ctx,
		`SELECT status, content FROM conversation_messages WHERE id = $1`,
		claimed.AssistantMessageID,
	).Scan(&assistantStatus, &assistantContent); err != nil {
		t.Fatalf("read rolled-back assistant: %v", err)
	}
	if assistantStatus != string(conversationdomain.MessageStatusGenerating) ||
		assistantContent != conversationdomain.AssistantGeneratingPlaceholder {
		t.Fatalf("rolled-back assistant status=%s content=%q", assistantStatus, assistantContent)
	}
	if _, err := pool.Exec(
		ctx,
		`DROP TRIGGER reject_agent_final_for_test ON agent_runs;
		 DROP FUNCTION reject_agent_final_for_test();`,
	); err != nil {
		t.Fatalf("remove final rollback trigger: %v", err)
	}
	finalStep, err := repository.CommitFinal(ctx, claimed.ID, claimed.LeaseToken, testCommitFinalInput("Atomic answer"))
	if err != nil || finalStep.Kind != agentdomain.StepKindFinal || finalStep.SequenceNo != 1 ||
		finalStep.TotalTokens != 5 {
		t.Fatalf("successful atomic final = %+v, %v", finalStep, err)
	}
	if _, err := repository.CommitFinal(ctx, claimed.ID, claimed.LeaseToken, testCommitFinalInput("Duplicate")); !errors.Is(err, agentapplication.ErrRunLeaseLost) {
		t.Fatalf("replayed final error = %v", err)
	}
}

func TestAgentConcurrentCommitFinalHasOneWinner(t *testing.T) {
	if os.Getenv("RUN_DATABASE_TESTS") != "1" {
		t.Skip("set RUN_DATABASE_TESTS=1 to run PostgreSQL integration tests")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	pool := openIsolatedDocumentTestPool(t, ctx)
	owner := answerJobTestOwnerScope(t, ctx, pool, "agent-final-race")
	repository := newAgentTestRepository(pool, "agent-worker-final-race")
	conversation := createAgentTestConversation(t, ctx, pool, owner, "final race")
	queueAgentTestRun(t, ctx, repository, owner, conversation.ID, "final-race")
	claimed, err := repository.ClaimNextRun(ctx)
	if err != nil {
		t.Fatalf("claim final-race Run: %v", err)
	}
	start := make(chan struct{})
	errorsCh := make(chan error, 2)
	var done sync.WaitGroup
	done.Add(2)
	for range 2 {
		go func() {
			defer done.Done()
			<-start
			_, commitErr := repository.CommitFinal(ctx, claimed.ID, claimed.LeaseToken, testCommitFinalInput("Race answer"))
			errorsCh <- commitErr
		}()
	}
	close(start)
	done.Wait()
	close(errorsCh)
	successes := 0
	leaseLosses := 0
	for commitErr := range errorsCh {
		switch {
		case commitErr == nil:
			successes++
		case errors.Is(commitErr, agentapplication.ErrRunLeaseLost):
			leaseLosses++
		default:
			t.Fatalf("unexpected concurrent final error: %v", commitErr)
		}
	}
	if successes != 1 || leaseLosses != 1 {
		t.Fatalf("concurrent final successes=%d lease_losses=%d", successes, leaseLosses)
	}
	var finalCount int
	if err := pool.QueryRow(
		ctx,
		`SELECT COUNT(*) FROM agent_steps WHERE agent_run_id = $1 AND kind = 'final'`,
		claimed.ID,
	).Scan(&finalCount); err != nil || finalCount != 1 {
		t.Fatalf("concurrent final Step count = %d, %v", finalCount, err)
	}
}

func TestAgentConcurrentPersistDecisionAccountsOnce(t *testing.T) {
	if os.Getenv("RUN_DATABASE_TESTS") != "1" {
		t.Skip("set RUN_DATABASE_TESTS=1 to run PostgreSQL integration tests")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	pool := openIsolatedDocumentTestPool(t, ctx)
	owner := answerJobTestOwnerScope(t, ctx, pool, "agent-decision-race")
	repository := newAgentTestRepository(pool, "agent-worker-decision-race")
	conversation := createAgentTestConversation(t, ctx, pool, owner, "decision race")
	queueAgentTestRun(t, ctx, repository, owner, conversation.ID, "decision-race")
	claimed, err := repository.ClaimNextRun(ctx)
	if err != nil {
		t.Fatalf("claim decision-race Run: %v", err)
	}
	start := make(chan struct{})
	errorsCh := make(chan error, 2)
	var done sync.WaitGroup
	done.Add(2)
	for range 2 {
		go func() {
			defer done.Done()
			<-start
			_, persistErr := repository.PersistDecision(ctx, claimed.ID, claimed.LeaseToken, agentapplication.PersistDecisionInput{
				ToolName: "search_documents", Arguments: json.RawMessage(`{"query":"race"}`),
				PromptTokens: 2, CompletionTokens: 1, TotalTokens: 3,
			})
			errorsCh <- persistErr
		}()
	}
	close(start)
	done.Wait()
	close(errorsCh)
	successes := 0
	conflicts := 0
	for persistErr := range errorsCh {
		switch {
		case persistErr == nil:
			successes++
		case errors.Is(persistErr, agentapplication.ErrInvalidStep):
			conflicts++
		default:
			t.Fatalf("unexpected concurrent decision error: %v", persistErr)
		}
	}
	if successes != 1 || conflicts != 1 {
		t.Fatalf("concurrent decisions successes=%d conflicts=%d", successes, conflicts)
	}
	run, err := repository.GetRunByID(ctx, owner, claimed.ID)
	if err != nil || run.StepCount != 1 || run.TotalTokens != 3 {
		t.Fatalf("concurrent decision Run = %+v, %v", run, err)
	}
	steps, err := repository.ListSteps(ctx, owner, claimed.ID)
	if err != nil || len(steps) != 1 || steps[0].TotalTokens != 3 {
		t.Fatalf("concurrent decision Steps = %+v, %v", steps, err)
	}
}

func TestAgentPersistDecisionEnforcesStepTokenAndDeadlineBeforeTool(t *testing.T) {
	if os.Getenv("RUN_DATABASE_TESTS") != "1" {
		t.Skip("set RUN_DATABASE_TESTS=1 to run PostgreSQL integration tests")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	pool := openIsolatedDocumentTestPool(t, ctx)
	owner := answerJobTestOwnerScope(t, ctx, pool, "agent-decision-budget")
	repository := newAgentTestRepository(pool, "agent-worker-decision-budget")

	stepConversation := createAgentTestConversation(t, ctx, pool, owner, "reserve final")
	queueAgentTestRun(t, ctx, repository, owner, stepConversation.ID, "reserve-final")
	stepClaim, err := repository.ClaimNextRun(ctx)
	if err != nil {
		t.Fatalf("claim reserve-final Run: %v", err)
	}
	for sequence := 1; sequence <= 4; sequence++ {
		step, err := repository.PersistDecision(ctx, stepClaim.ID, stepClaim.LeaseToken, agentapplication.PersistDecisionInput{
			ToolName: "search_documents", Arguments: json.RawMessage(`{"query":"bounded"}`),
			PromptTokens: 1, CompletionTokens: 0, TotalTokens: 1,
		})
		if err != nil || step.SequenceNo != sequence {
			t.Fatalf("persist bounded decision %d = %+v, %v", sequence, step, err)
		}
		if _, err := repository.MarkStepSucceeded(
			ctx,
			stepClaim.ID,
			step.ID,
			stepClaim.LeaseToken,
			agentapplication.CompleteStepInput{Result: json.RawMessage(`{"items":[]}`)},
		); err != nil {
			t.Fatalf("complete bounded decision %d: %v", sequence, err)
		}
	}
	if _, err := repository.PersistDecision(ctx, stepClaim.ID, stepClaim.LeaseToken, agentapplication.PersistDecisionInput{
		ToolName: "search_documents", Arguments: json.RawMessage(`{"query":"would consume final"}`),
		PromptTokens: 1, CompletionTokens: 0, TotalTokens: 1,
	}); !errors.Is(err, agentapplication.ErrStepLimitExceeded) {
		t.Fatalf("fifth Tool decision error = %v", err)
	}
	finalStep, err := repository.CommitFinal(ctx, stepClaim.ID, stepClaim.LeaseToken, agentapplication.CommitFinalInput{
		Result:       agentapplication.FinalStepResult{Answer: "bounded", ResponseLanguage: agentdomain.ResponseLanguageEnglish},
		PromptTokens: 1, CompletionTokens: 0, TotalTokens: 1,
	})
	if err != nil || finalStep.SequenceNo != 5 {
		t.Fatalf("reserved final Step = %+v, %v", finalStep, err)
	}

	budgetConversation := createAgentTestConversation(t, ctx, pool, owner, "token budget")
	budgetQueued, err := repository.CreateRun(ctx, owner, agentapplication.CreateRunInput{
		ConversationID: budgetConversation.ID, Content: "budget", ClientRunID: "token-budget",
		ResponseLanguage: agentdomain.ResponseLanguageEnglish,
		Budget:           agentdomain.Budget{MaxSteps: 5, MaxTotalTokens: 3, DeadlineAt: time.Now().Add(time.Minute)},
	})
	if err != nil {
		t.Fatalf("queue token-budget Run: %v", err)
	}
	budgetClaim, err := repository.ClaimNextRun(ctx)
	if err != nil || budgetClaim.ID != budgetQueued.Run.ID {
		t.Fatalf("claim token-budget Run = %+v, %v", budgetClaim, err)
	}
	if _, err := repository.PersistDecision(ctx, budgetClaim.ID, budgetClaim.LeaseToken, agentapplication.PersistDecisionInput{
		ToolName: "search_documents", Arguments: json.RawMessage(`{"query":"too expensive"}`),
		PromptTokens: 2, CompletionTokens: 2, TotalTokens: 4,
	}); !errors.Is(err, agentapplication.ErrTokenBudgetExceeded) {
		t.Fatalf("over-budget decision error = %v", err)
	}
	assertEmptyDecisionState(t, ctx, pool, budgetClaim.ID)
	if _, err := repository.CommitFinal(ctx, budgetClaim.ID, budgetClaim.LeaseToken, agentapplication.CommitFinalInput{
		Result:       agentapplication.FinalStepResult{Answer: "too expensive", ResponseLanguage: agentdomain.ResponseLanguageEnglish},
		PromptTokens: 2, CompletionTokens: 2, TotalTokens: 4,
	}); !errors.Is(err, agentapplication.ErrTokenBudgetExceeded) {
		t.Fatalf("over-budget final error = %v", err)
	}
	assertEmptyDecisionState(t, ctx, pool, budgetClaim.ID)
	if err := repository.MarkRunFailed(ctx, budgetClaim.ID, budgetClaim.LeaseToken, agentdomain.ErrorCodeTokenBudgetExceeded, "token budget exceeded"); err != nil {
		t.Fatalf("close token-budget fixture: %v", err)
	}

	deadlineConversation := createAgentTestConversation(t, ctx, pool, owner, "deadline")
	deadlineQueued := queueAgentTestRun(t, ctx, repository, owner, deadlineConversation.ID, "deadline")
	deadlineClaim, err := repository.ClaimNextRun(ctx)
	if err != nil || deadlineClaim.ID != deadlineQueued.Run.ID {
		t.Fatalf("claim deadline Run = %+v, %v", deadlineClaim, err)
	}
	if _, err := pool.Exec(
		ctx,
		`UPDATE agent_runs SET deadline_at = created_at + INTERVAL '1 microsecond' WHERE id = $1`,
		deadlineClaim.ID,
	); err != nil {
		t.Fatalf("expire decision deadline: %v", err)
	}
	if _, err := repository.PersistDecision(ctx, deadlineClaim.ID, deadlineClaim.LeaseToken, agentapplication.PersistDecisionInput{
		ToolName: "search_documents", Arguments: json.RawMessage(`{"query":"too late"}`),
		PromptTokens: 1, CompletionTokens: 1, TotalTokens: 2,
	}); !errors.Is(err, agentapplication.ErrRunDeadlineExceeded) {
		t.Fatalf("expired decision error = %v", err)
	}
	assertEmptyDecisionState(t, ctx, pool, deadlineClaim.ID)
	if _, err := repository.CommitFinal(ctx, deadlineClaim.ID, deadlineClaim.LeaseToken, testCommitFinalInput("too late")); !errors.Is(err, agentapplication.ErrRunDeadlineExceeded) {
		t.Fatalf("expired final error = %v", err)
	}
	assertEmptyDecisionState(t, ctx, pool, deadlineClaim.ID)
}

func assertEmptyDecisionState(t *testing.T, ctx context.Context, pool *pgxpool.Pool, runID int64) {
	t.Helper()
	var stepCount int
	var promptTokens *int
	var completionTokens *int
	var totalTokens *int
	var persistedSteps int
	if err := pool.QueryRow(
		ctx,
		`SELECT step_count, prompt_tokens, completion_tokens, total_tokens,
		        (SELECT COUNT(*) FROM agent_steps WHERE agent_run_id = agent_runs.id)
		 FROM agent_runs WHERE id = $1`,
		runID,
	).Scan(&stepCount, &promptTokens, &completionTokens, &totalTokens, &persistedSteps); err != nil {
		t.Fatalf("read empty decision state: %v", err)
	}
	if stepCount != 0 || promptTokens != nil || completionTokens != nil || totalTokens != nil || persistedSteps != 0 {
		t.Fatalf("decision state changed: steps=%d prompt=%v completion=%v total=%v rows=%d", stepCount, promptTokens, completionTokens, totalTokens, persistedSteps)
	}
}
