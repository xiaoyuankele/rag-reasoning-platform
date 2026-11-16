package postgres_test

import (
	"context"
	"errors"
	"os"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"

	agentapplication "rag-reasoning-platform/backend/internal/application/agent"
	agentdomain "rag-reasoning-platform/backend/internal/domain/agent"
	conversationdomain "rag-reasoning-platform/backend/internal/domain/conversation"
	postgresrepository "rag-reasoning-platform/backend/internal/infrastructure/postgres"
)

func TestAgentRunSnapshotsCoverCreateReplayQueriesAndCancel(t *testing.T) {
	if os.Getenv("RUN_DATABASE_TESTS") != "1" {
		t.Skip("set RUN_DATABASE_TESTS=1 to run PostgreSQL integration tests")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 45*time.Second)
	defer cancel()
	pool := openIsolatedDocumentTestPool(t, ctx)
	owner := answerJobTestOwnerScope(t, ctx, pool, "agent-snapshot-lifecycle")
	repository := newAgentTestRepository(pool, "agent-worker-snapshot-lifecycle")
	conversation := createAgentTestConversation(t, ctx, pool, owner, "snapshot lifecycle")

	created := queueAgentTestRun(t, ctx, repository, owner, conversation.ID, "snapshot-run")
	if !created.Created || created.Steps == nil || len(created.Steps) != 0 ||
		created.Run.UserMessageID != created.UserMessage.ID ||
		created.Run.AssistantMessageID != created.AssistantMessage.ID ||
		created.UserMessage.ConversationID != created.Run.ConversationID ||
		created.AssistantMessage.ConversationID != created.Run.ConversationID {
		t.Fatalf("fresh create snapshot/messages = %+v", created)
	}
	assertAgentRunSnapshot(t, created.RunSnapshot)

	claimed, err := repository.ClaimNextRun(ctx)
	if err != nil || claimed.ID != created.Run.ID || claimed.StartedAt == nil {
		t.Fatalf("ClaimNextRun() = %+v, %v", claimed, err)
	}
	firstStartedAt := *claimed.StartedAt
	processing, err := repository.GetRunByID(ctx, owner, claimed.ID)
	if err != nil {
		t.Fatalf("GetRunByID(processing) error = %v", err)
	}
	if processing.WorkerID != "" || processing.LeaseToken != "" ||
		processing.LeaseExpiresAt != nil || processing.HeartbeatAt != nil {
		t.Fatalf("scoped processing snapshot leaked worker state: %+v", processing.Run)
	}
	assertAgentRunSnapshot(t, processing)

	step, err := repository.PersistDecision(ctx, claimed.ID, claimed.LeaseToken, agentapplication.PersistDecisionInput{
		ToolName:     agentapplication.ToolSearchDocuments,
		Arguments:    []byte(`{"query":"recovered","strategy":"keyword","top_k":1}`),
		PromptTokens: 2, CompletionTokens: 1, TotalTokens: 3,
	})
	if err != nil || step.SequenceNo != 1 || step.Status != agentdomain.StepStatusRunning {
		t.Fatalf("PersistDecision() = %+v, %v", step, err)
	}
	if err := repository.RequeueRun(
		ctx,
		claimed.ID,
		claimed.LeaseToken,
		time.Now().Add(time.Second),
		agentdomain.ErrorCodeTemporarilyUnavailable,
		"temporary model failure",
	); err != nil {
		t.Fatalf("RequeueRun() error = %v", err)
	}

	replayed, err := repository.CreateRun(ctx, owner, agentapplication.CreateRunInput{
		ConversationID:   conversation.ID,
		Content:          created.UserMessage.Content,
		ClientRunID:      created.Run.ClientRunID,
		ResponseLanguage: agentdomain.ResponseLanguageEnglish,
		Budget: agentdomain.Budget{
			MaxSteps: 1, MaxTotalTokens: 1, DeadlineAt: time.Now().Add(time.Hour),
		},
	})
	if err != nil || replayed.Created || replayed.Run.ID != created.Run.ID ||
		replayed.UserMessage.ID != created.UserMessage.ID ||
		replayed.AssistantMessage.ID != created.AssistantMessage.ID {
		t.Fatalf("idempotent replay snapshot = %+v, %v", replayed, err)
	}
	if len(replayed.Steps) != 1 || replayed.Steps[0].Status != agentdomain.StepStatusPending ||
		replayed.Steps[0].TotalTokens != 3 || replayed.StartedAt == nil ||
		!replayed.StartedAt.Equal(firstStartedAt) {
		t.Fatalf("replayed recovered snapshot = %+v", replayed.RunSnapshot)
	}
	assertAgentRunSnapshot(t, replayed.RunSnapshot)

	for name, load := range map[string]func() (agentapplication.RunSnapshot, error){
		"get": func() (agentapplication.RunSnapshot, error) {
			return repository.GetRunByID(ctx, owner, created.Run.ID)
		},
		"active": func() (agentapplication.RunSnapshot, error) {
			return repository.GetActiveRunByConversationID(ctx, owner, conversation.ID)
		},
		"latest": func() (agentapplication.RunSnapshot, error) {
			return repository.GetLatestRunByConversationID(ctx, owner, conversation.ID)
		},
	} {
		t.Run(name, func(t *testing.T) {
			snapshot, err := load()
			if err != nil || snapshot.ID != created.Run.ID || len(snapshot.Steps) != 1 ||
				snapshot.Steps[0].Status != agentdomain.StepStatusPending {
				t.Fatalf("snapshot = %+v, %v", snapshot, err)
			}
			assertAgentRunSnapshot(t, snapshot)
		})
	}

	canceled, err := repository.CancelRun(ctx, owner, created.Run.ID)
	if err != nil || canceled.Status != agentdomain.RunStatusCanceled || len(canceled.Steps) != 1 ||
		canceled.Steps[0].Status != agentdomain.StepStatusFailed ||
		canceled.Steps[0].ErrorCode != agentdomain.ErrorCodeWorkerInterrupted ||
		canceled.Steps[0].TotalTokens != 3 || canceled.StartedAt == nil ||
		!canceled.StartedAt.Equal(firstStartedAt) {
		t.Fatalf("CancelRun() snapshot = %+v, %v", canceled, err)
	}
	assertAgentRunSnapshot(t, canceled)
	if _, err := repository.GetActiveRunByConversationID(ctx, owner, conversation.ID); !errors.Is(err, agentapplication.ErrRunNotFound) {
		t.Fatalf("active after cancel error = %v", err)
	}
	latest, err := repository.GetLatestRunByConversationID(ctx, owner, conversation.ID)
	if err != nil || latest.Status != agentdomain.RunStatusCanceled {
		t.Fatalf("latest canceled snapshot = %+v, %v", latest, err)
	}
	assertAgentRunSnapshot(t, latest)

	archived := conversationdomain.StatusArchived
	if _, err := postgresrepository.NewConversationRepository(pool).UpdateConversation(
		ctx,
		owner,
		conversationdomain.UpdateInput{ConversationID: conversation.ID, Status: &archived},
	); err != nil {
		t.Fatalf("archive snapshot conversation: %v", err)
	}
	archivedLatest, err := repository.GetLatestRunByConversationID(ctx, owner, conversation.ID)
	if err != nil || archivedLatest.ID != created.Run.ID {
		t.Fatalf("latest archived conversation snapshot = %+v, %v", archivedLatest, err)
	}
}

func TestAgentConversationSnapshotLookupDistinguishesConversationAndRunAbsence(t *testing.T) {
	if os.Getenv("RUN_DATABASE_TESTS") != "1" {
		t.Skip("set RUN_DATABASE_TESTS=1 to run PostgreSQL integration tests")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	pool := openIsolatedDocumentTestPool(t, ctx)
	ownerA := answerJobTestOwnerScope(t, ctx, pool, "agent-snapshot-errors-a")
	ownerB := answerJobTestOwnerScope(t, ctx, pool, "agent-snapshot-errors-b")
	repository := newAgentTestRepository(pool, "agent-worker-snapshot-errors")
	empty := createAgentTestConversation(t, ctx, pool, ownerA, "empty agent conversation")

	for _, lookup := range []func() (agentapplication.RunSnapshot, error){
		func() (agentapplication.RunSnapshot, error) {
			return repository.GetActiveRunByConversationID(ctx, ownerA, empty.ID+1_000_000)
		},
		func() (agentapplication.RunSnapshot, error) {
			return repository.GetLatestRunByConversationID(ctx, ownerA, empty.ID+1_000_000)
		},
		func() (agentapplication.RunSnapshot, error) {
			return repository.GetActiveRunByConversationID(ctx, ownerB, empty.ID)
		},
		func() (agentapplication.RunSnapshot, error) {
			return repository.GetLatestRunByConversationID(ctx, ownerB, empty.ID)
		},
	} {
		if _, err := lookup(); !errors.Is(err, conversationdomain.ErrNotFound) {
			t.Fatalf("missing/cross-owner conversation error = %v", err)
		}
	}
	for _, lookup := range []func() (agentapplication.RunSnapshot, error){
		func() (agentapplication.RunSnapshot, error) {
			return repository.GetActiveRunByConversationID(ctx, ownerA, empty.ID)
		},
		func() (agentapplication.RunSnapshot, error) {
			return repository.GetLatestRunByConversationID(ctx, ownerA, empty.ID)
		},
	} {
		if _, err := lookup(); !errors.Is(err, agentapplication.ErrRunNotFound) {
			t.Fatalf("existing conversation without Run error = %v", err)
		}
	}

	queued := queueAgentTestRun(t, ctx, repository, ownerA, empty.ID, "owner-scoped-snapshot")
	if _, err := repository.GetRunByID(ctx, ownerB, queued.Run.ID); !errors.Is(err, agentapplication.ErrRunNotFound) {
		t.Fatalf("cross-owner GetRunByID() error = %v", err)
	}
	if _, err := repository.CancelRun(ctx, ownerB, queued.Run.ID); !errors.Is(err, agentapplication.ErrRunNotFound) {
		t.Fatalf("cross-owner CancelRun() error = %v", err)
	}
}

func TestAgentSnapshotReadUsesOneRepeatableView(t *testing.T) {
	if os.Getenv("RUN_DATABASE_TESTS") != "1" {
		t.Skip("set RUN_DATABASE_TESTS=1 to run PostgreSQL integration tests")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	pool := openIsolatedDocumentTestPool(t, ctx)
	owner := answerJobTestOwnerScope(t, ctx, pool, "agent-snapshot-repeatable-read")
	repository := newAgentTestRepository(pool, "agent-worker-snapshot-repeatable-read")
	conversation := createAgentTestConversation(t, ctx, pool, owner, "repeatable snapshot")
	queued := queueAgentTestRun(t, ctx, repository, owner, conversation.ID, "repeatable-view")

	writer, err := pool.BeginTx(ctx, pgx.TxOptions{})
	if err != nil {
		t.Fatalf("begin snapshot writer: %v", err)
	}
	defer func() { _ = writer.Rollback(context.Background()) }()
	if _, err := writer.Exec(ctx, `LOCK TABLE agent_steps IN ACCESS EXCLUSIVE MODE`); err != nil {
		t.Fatalf("lock agent_steps: %v", err)
	}

	type snapshotResult struct {
		snapshot agentapplication.RunSnapshot
		err      error
	}
	resultCh := make(chan snapshotResult, 1)
	go func() {
		snapshot, lookupErr := repository.GetRunByID(ctx, owner, queued.Run.ID)
		resultCh <- snapshotResult{snapshot: snapshot, err: lookupErr}
	}()

	waitDeadline := time.Now().Add(5 * time.Second)
	for {
		select {
		case result := <-resultCh:
			t.Fatalf("snapshot reader completed before the Step lock was released: %+v, %v", result.snapshot, result.err)
		default:
		}
		var blocked bool
		if err := writer.QueryRow(
			ctx,
			`SELECT EXISTS (
				SELECT 1
				FROM pg_locks
				WHERE pid <> pg_backend_pid()
				  AND relation = 'agent_steps'::REGCLASS
				  AND NOT granted
			)`,
		).Scan(&blocked); err != nil {
			t.Fatalf("observe blocked snapshot reader: %v", err)
		}
		if blocked {
			break
		}
		if time.Now().After(waitDeadline) {
			t.Fatal("snapshot reader did not block after reading the Run")
		}
		time.Sleep(10 * time.Millisecond)
	}

	if _, err := writer.Exec(
		ctx,
		`INSERT INTO agent_steps (
			agent_run_id, sequence_no, kind, status, tool_name, arguments_json,
			prompt_tokens, completion_tokens, total_tokens
		 ) VALUES ($1, 1, 'tool_call', 'pending', 'search_documents',
		           '{"query":"concurrent"}'::JSONB, 2, 1, 3)`,
		queued.Run.ID,
	); err != nil {
		t.Fatalf("insert concurrent pending step: %v", err)
	}
	if _, err := writer.Exec(
		ctx,
		`UPDATE agent_runs
		 SET step_count = 1,
		     prompt_tokens = 2,
		     completion_tokens = 1,
		     total_tokens = 3,
		     updated_at = CURRENT_TIMESTAMP
		 WHERE id = $1`,
		queued.Run.ID,
	); err != nil {
		t.Fatalf("update concurrent Run aggregate: %v", err)
	}
	if err := writer.Commit(ctx); err != nil {
		t.Fatalf("commit concurrent snapshot writer: %v", err)
	}

	result := <-resultCh
	if result.err != nil || result.snapshot.StepCount != 0 || len(result.snapshot.Steps) != 0 {
		t.Fatalf("repeatable reader returned torn snapshot = %+v, %v", result.snapshot, result.err)
	}
	assertAgentRunSnapshot(t, result.snapshot)
	updated, err := repository.GetRunByID(ctx, owner, queued.Run.ID)
	if err != nil || updated.StepCount != 1 || len(updated.Steps) != 1 || updated.TotalTokens != 3 {
		t.Fatalf("post-commit snapshot = %+v, %v", updated, err)
	}
	assertAgentRunSnapshot(t, updated)
}

func TestAgentSnapshotRejectsMismatchedRunAndFinalStepResults(t *testing.T) {
	if os.Getenv("RUN_DATABASE_TESTS") != "1" {
		t.Skip("set RUN_DATABASE_TESTS=1 to run PostgreSQL integration tests")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	pool := openIsolatedDocumentTestPool(t, ctx)
	owner := answerJobTestOwnerScope(t, ctx, pool, "agent-snapshot-final-audit")
	repository := newAgentTestRepository(pool, "agent-worker-snapshot-final-audit")
	conversation := createAgentTestConversation(t, ctx, pool, owner, "final audit")
	queued := queueAgentTestRun(t, ctx, repository, owner, conversation.ID, "final-audit")
	claimed, err := repository.ClaimNextRun(ctx)
	if err != nil || claimed.ID != queued.Run.ID {
		t.Fatalf("ClaimNextRun() = %+v, %v", claimed, err)
	}
	if _, err := repository.CommitFinal(ctx, claimed.ID, claimed.LeaseToken, agentapplication.CommitFinalInput{
		Result: agentapplication.FinalStepResult{
			Answer: "trusted answer", ResponseLanguage: agentdomain.ResponseLanguageEnglish,
			Payload: []byte(`{"sources":[]}`),
		},
		PromptTokens: 2, CompletionTokens: 1, TotalTokens: 3,
	}); err != nil {
		t.Fatalf("CommitFinal() error = %v", err)
	}
	if _, err := pool.Exec(
		ctx,
		`UPDATE agent_runs
		 SET result_json = JSONB_SET(result_json, '{answer}', TO_JSONB('tampered answer'::TEXT)),
		     updated_at = CURRENT_TIMESTAMP
		 WHERE id = $1`,
		claimed.ID,
	); err != nil {
		t.Fatalf("tamper Run final result: %v", err)
	}
	if snapshot, err := repository.GetRunByID(ctx, owner, claimed.ID); err == nil {
		t.Fatalf("GetRunByID() accepted mismatched final audit snapshot: %+v", snapshot)
	}
}

func assertAgentRunSnapshot(t *testing.T, snapshot agentapplication.RunSnapshot) {
	t.Helper()
	if snapshot.Steps == nil || snapshot.StepCount != len(snapshot.Steps) ||
		!agentdomain.TokenUsageIsValid(snapshot.PromptTokens, snapshot.CompletionTokens, snapshot.TotalTokens) {
		t.Fatalf("invalid snapshot shape = %+v", snapshot)
	}
	if snapshot.WorkerID != "" || snapshot.LeaseToken != "" ||
		snapshot.LeaseExpiresAt != nil || snapshot.HeartbeatAt != nil {
		t.Fatalf("snapshot leaked worker internals = %+v", snapshot.Run)
	}
	promptTokens := 0
	completionTokens := 0
	totalTokens := 0
	for index, step := range snapshot.Steps {
		if step.RunID != snapshot.ID || step.SequenceNo != index+1 {
			t.Fatalf("snapshot step association/order = %+v", snapshot.Steps)
		}
		promptTokens += step.PromptTokens
		completionTokens += step.CompletionTokens
		totalTokens += step.TotalTokens
		if snapshot.Status.IsTerminal() && !step.Status.IsTerminal() {
			t.Fatalf("terminal snapshot contains active step = %+v", step)
		}
	}
	if promptTokens != snapshot.PromptTokens || completionTokens != snapshot.CompletionTokens ||
		totalTokens != snapshot.TotalTokens {
		t.Fatalf("snapshot token aggregate Run=%d/%d/%d Steps=%d/%d/%d", snapshot.PromptTokens, snapshot.CompletionTokens, snapshot.TotalTokens, promptTokens, completionTokens, totalTokens)
	}
	if snapshot.Status == agentdomain.RunStatusSucceeded {
		last := snapshot.Steps[len(snapshot.Steps)-1]
		if snapshot.Result == nil || last.Kind != agentdomain.StepKindFinal || last.Status != agentdomain.StepStatusSucceeded {
			t.Fatalf("succeeded snapshot final invariant = %+v", snapshot)
		}
	}
}
