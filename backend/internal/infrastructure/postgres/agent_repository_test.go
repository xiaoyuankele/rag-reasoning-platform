package postgres_test

import (
	"context"
	"errors"
	"fmt"
	"os"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"

	agentapplication "rag-reasoning-platform/backend/internal/application/agent"
	answerapplication "rag-reasoning-platform/backend/internal/application/answer"
	accessdomain "rag-reasoning-platform/backend/internal/domain/access"
	agentdomain "rag-reasoning-platform/backend/internal/domain/agent"
	conversationdomain "rag-reasoning-platform/backend/internal/domain/conversation"
	documentdomain "rag-reasoning-platform/backend/internal/domain/document"
	postgresrepository "rag-reasoning-platform/backend/internal/infrastructure/postgres"
)

func newAgentTestRepository(pool *pgxpool.Pool, workerID string) *postgresrepository.AgentRepository {
	return postgresrepository.NewAgentRepositoryWithPolicies(
		pool,
		agentdomain.AdmissionLimits{MaxQueuedRunsPerOwner: 20, MaxQueuedRunsGlobal: 100},
		agentdomain.SchedulingPolicy{
			MaxInFlightPerOwner:         1,
			MaxBorrowedInFlightPerOwner: 2,
			StarvationThreshold:         time.Second,
		},
		agentdomain.LeasePolicy{
			WorkerID:      workerID,
			LeaseDuration: time.Minute,
			MaxAttempts:   agentdomain.MaxAutomaticAttempts,
		},
	)
}

func createAgentTestConversation(
	t *testing.T,
	ctx context.Context,
	pool *pgxpool.Pool,
	scope accessdomain.OwnerScope,
	title string,
) conversationdomain.Conversation {
	t.Helper()
	conversation, err := postgresrepository.NewConversationRepository(pool).CreateConversation(
		ctx,
		scope,
		conversationdomain.CreateInput{Title: title},
	)
	if err != nil {
		t.Fatalf("create agent test conversation: %v", err)
	}
	return conversation
}

func queueAgentTestRun(
	t *testing.T,
	ctx context.Context,
	repository *postgresrepository.AgentRepository,
	scope accessdomain.OwnerScope,
	conversationID int64,
	clientRunID string,
) agentapplication.QueueResult {
	t.Helper()
	result, err := repository.CreateRun(ctx, scope, agentapplication.CreateRunInput{
		ConversationID:   conversationID,
		Content:          "What does the evidence say?",
		ClientRunID:      clientRunID,
		ResponseLanguage: agentdomain.ResponseLanguageEnglish,
		Budget: agentdomain.Budget{
			MaxSteps:       agentdomain.MaxPersistedSteps,
			MaxTotalTokens: 48_000,
			DeadlineAt:     time.Now().Add(2 * time.Minute),
		},
	})
	if err != nil {
		t.Fatalf("queue agent test run: %v", err)
	}
	return result
}

func TestAgentRepositoryCreatesReplaysScopesAndCancels(t *testing.T) {
	if os.Getenv("RUN_DATABASE_TESTS") != "1" {
		t.Skip("set RUN_DATABASE_TESTS=1 to run PostgreSQL integration tests")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 45*time.Second)
	defer cancel()
	pool := openIsolatedDocumentTestPool(t, ctx)
	ownerA := answerJobTestOwnerScope(t, ctx, pool, "agent-owner-a")
	ownerB := answerJobTestOwnerScope(t, ctx, pool, "agent-owner-b")
	repository := newAgentTestRepository(pool, "agent-worker-create")
	conversation := createAgentTestConversation(t, ctx, pool, ownerA, "agent replay")

	created := queueAgentTestRun(t, ctx, repository, ownerA, conversation.ID, "run-1")
	if !created.Created || created.Run.Status != agentdomain.RunStatusQueued ||
		created.UserMessage.Role != conversationdomain.RoleUser ||
		created.AssistantMessage.Role != conversationdomain.RoleAssistant ||
		created.AssistantMessage.SequenceNo != created.UserMessage.SequenceNo+1 ||
		created.AssistantMessage.Status != conversationdomain.MessageStatusGenerating {
		t.Fatalf("created agent run = %+v", created)
	}
	replayed, err := repository.CreateRun(ctx, ownerA, agentapplication.CreateRunInput{
		ConversationID:   conversation.ID,
		Content:          created.UserMessage.Content,
		ClientRunID:      "run-1",
		ResponseLanguage: agentdomain.ResponseLanguageEnglish,
		Budget: agentdomain.Budget{
			MaxSteps:       1,
			MaxTotalTokens: 1,
			DeadlineAt:     time.Now().Add(time.Hour),
		},
	})
	if err != nil || replayed.Created || replayed.Run.ID != created.Run.ID ||
		replayed.UserMessage.ID != created.UserMessage.ID ||
		replayed.AssistantMessage.ID != created.AssistantMessage.ID {
		t.Fatalf("idempotent agent replay = %+v, %v", replayed, err)
	}
	differentDocumentID := int64(999999)
	for _, different := range []agentapplication.CreateRunInput{
		{
			ConversationID: conversation.ID, Content: "different", ClientRunID: "run-1",
			ResponseLanguage: agentdomain.ResponseLanguageEnglish,
		},
		{
			ConversationID: conversation.ID, Content: created.UserMessage.Content, ClientRunID: "run-1",
			RequestedDocumentID: &differentDocumentID, ResponseLanguage: agentdomain.ResponseLanguageEnglish,
		},
		{
			ConversationID: conversation.ID, Content: created.UserMessage.Content, ClientRunID: "run-1",
			ResponseLanguage: agentdomain.ResponseLanguageChinese,
		},
	} {
		different.Budget = agentdomain.Budget{
			MaxSteps: 5, MaxTotalTokens: 48_000, DeadlineAt: time.Now().Add(time.Minute),
		}
		if _, err := repository.CreateRun(ctx, ownerA, different); !errors.Is(err, agentapplication.ErrIdempotencyConflict) {
			t.Fatalf("different idempotent request %+v error = %v", different, err)
		}
	}
	active, err := repository.GetActiveRunByConversationID(ctx, ownerA, conversation.ID)
	if err != nil || active.ID != created.Run.ID {
		t.Fatalf("active conversation agent run = %+v, %v", active, err)
	}
	latest, err := repository.GetLatestRunByConversationID(ctx, ownerA, conversation.ID)
	if err != nil || latest.ID != created.Run.ID {
		t.Fatalf("latest conversation agent run = %+v, %v", latest, err)
	}
	if _, err := repository.GetRunByID(ctx, ownerB, created.Run.ID); !errors.Is(err, agentapplication.ErrRunNotFound) {
		t.Fatalf("cross-owner run lookup error = %v", err)
	}
	if _, err := repository.ListSteps(ctx, ownerB, created.Run.ID); !errors.Is(err, agentapplication.ErrRunNotFound) {
		t.Fatalf("cross-owner step lookup error = %v", err)
	}

	documentRepository := postgresrepository.NewScopedDocumentRepository(pool)
	foreignDocument, err := documentRepository.Create(ctx, ownerB, documentdomain.CreateInput{
		OriginalName: "foreign.pdf",
		StoragePath:  fmt.Sprintf("agent-tests/%d.pdf", time.Now().UnixNano()),
		MIMEType:     "application/pdf",
		SizeBytes:    1,
		SHA256:       strings.Repeat("a", 64),
	})
	if err != nil {
		t.Fatalf("create foreign document: %v", err)
	}
	documentConversation := createAgentTestConversation(t, ctx, pool, ownerA, "foreign document")
	_, err = repository.CreateRun(ctx, ownerA, agentapplication.CreateRunInput{
		ConversationID: documentConversation.ID, Content: "question", ClientRunID: "foreign-doc",
		RequestedDocumentID: &foreignDocument.ID,
		ResponseLanguage:    agentdomain.ResponseLanguageAuto,
		Budget:              agentdomain.Budget{MaxSteps: 5, MaxTotalTokens: 48_000, DeadlineAt: time.Now().Add(time.Minute)},
	})
	if !errors.Is(err, documentdomain.ErrNotFound) {
		t.Fatalf("cross-owner document error = %v", err)
	}

	canceled, err := repository.CancelRun(ctx, ownerA, created.Run.ID)
	if err != nil || canceled.Status != agentdomain.RunStatusCanceled || canceled.CompletedAt == nil {
		t.Fatalf("cancel agent run = %+v, %v", canceled, err)
	}
	if _, err := repository.CancelRun(ctx, ownerA, created.Run.ID); !errors.Is(err, agentapplication.ErrRunTerminalCannotCancel) {
		t.Fatalf("repeat canceled run error = %v", err)
	}
	messages, err := postgresrepository.NewConversationRepository(pool).ListMessages(
		ctx,
		ownerA,
		conversation.ID,
		conversationdomain.ListOptions{Limit: 10},
	)
	if err != nil || len(messages.Messages) != 2 ||
		messages.Messages[1].Status != conversationdomain.MessageStatusInterrupted {
		t.Fatalf("canceled agent messages = %+v, %v", messages.Messages, err)
	}
}

func TestAgentRepositoryConcurrentReplayCreatesOneRunAndTurn(t *testing.T) {
	if os.Getenv("RUN_DATABASE_TESTS") != "1" {
		t.Skip("set RUN_DATABASE_TESTS=1 to run PostgreSQL integration tests")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 45*time.Second)
	defer cancel()
	pool := openIsolatedDocumentTestPool(t, ctx)
	owner := answerJobTestOwnerScope(t, ctx, pool, "agent-concurrent")
	repository := newAgentTestRepository(pool, "agent-worker-replay")
	conversation := createAgentTestConversation(t, ctx, pool, owner, "eight-way replay")

	const callers = 8
	start := make(chan struct{})
	results := make(chan agentapplication.QueueResult, callers)
	errorsCh := make(chan error, callers)
	var ready sync.WaitGroup
	var done sync.WaitGroup
	ready.Add(callers)
	done.Add(callers)
	for range callers {
		go func() {
			defer done.Done()
			ready.Done()
			<-start
			result, err := repository.CreateRun(ctx, owner, agentapplication.CreateRunInput{
				ConversationID: conversation.ID, Content: "same question", ClientRunID: "same-run",
				ResponseLanguage: agentdomain.ResponseLanguageEnglish,
				Budget:           agentdomain.Budget{MaxSteps: 5, MaxTotalTokens: 48_000, DeadlineAt: time.Now().Add(2 * time.Minute)},
			})
			if err != nil {
				errorsCh <- err
				return
			}
			results <- result
		}()
	}
	ready.Wait()
	close(start)
	done.Wait()
	close(results)
	close(errorsCh)
	for err := range errorsCh {
		t.Errorf("concurrent agent replay: %v", err)
	}
	var runID int64
	createdCount := 0
	for result := range results {
		if runID == 0 {
			runID = result.Run.ID
		}
		if result.Run.ID != runID {
			t.Fatalf("concurrent replay returned run IDs %d and %d", runID, result.Run.ID)
		}
		if result.Created {
			createdCount++
		}
	}
	if createdCount != 1 {
		t.Fatalf("concurrent replay created count = %d, want 1", createdCount)
	}
	var runCount int
	var messageCount int
	if err := pool.QueryRow(ctx, `SELECT COUNT(*) FROM agent_runs WHERE conversation_id = $1`, conversation.ID).Scan(&runCount); err != nil {
		t.Fatalf("count concurrent agent runs: %v", err)
	}
	if err := pool.QueryRow(ctx, `SELECT COUNT(*) FROM conversation_messages WHERE conversation_id = $1`, conversation.ID).Scan(&messageCount); err != nil {
		t.Fatalf("count concurrent agent messages: %v", err)
	}
	if runCount != 1 || messageCount != 2 {
		t.Fatalf("concurrent replay persisted runs=%d messages=%d", runCount, messageCount)
	}
}

func TestAgentRepositoryAdmissionAndMigrationLeaseConstraints(t *testing.T) {
	if os.Getenv("RUN_DATABASE_TESTS") != "1" {
		t.Skip("set RUN_DATABASE_TESTS=1 to run PostgreSQL integration tests")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	pool := openIsolatedDocumentTestPool(t, ctx)
	ownerA := answerJobTestOwnerScope(t, ctx, pool, "agent-capacity-a")
	ownerB := answerJobTestOwnerScope(t, ctx, pool, "agent-capacity-b")
	repository := postgresrepository.NewAgentRepository(
		pool,
		agentdomain.AdmissionLimits{MaxQueuedRunsPerOwner: 1, MaxQueuedRunsGlobal: 1},
		agentdomain.SchedulingPolicy{
			MaxInFlightPerOwner: 1, MaxBorrowedInFlightPerOwner: 1, StarvationThreshold: time.Second,
		},
	)
	firstConversation := createAgentTestConversation(t, ctx, pool, ownerA, "capacity first")
	first := queueAgentTestRun(t, ctx, repository, ownerA, firstConversation.ID, "capacity-first")
	replayed, err := repository.CreateRun(ctx, ownerA, agentapplication.CreateRunInput{
		ConversationID: firstConversation.ID, Content: first.UserMessage.Content, ClientRunID: "capacity-first",
		ResponseLanguage: agentdomain.ResponseLanguageEnglish,
		Budget:           agentdomain.Budget{MaxSteps: 1, MaxTotalTokens: 1, DeadlineAt: time.Now().Add(time.Minute)},
	})
	if err != nil || replayed.Run.ID != first.Run.ID || replayed.Created {
		t.Fatalf("capacity idempotent replay = %+v, %v", replayed, err)
	}
	ownerConversation := createAgentTestConversation(t, ctx, pool, ownerA, "owner capacity")
	_, err = repository.CreateRun(ctx, ownerA, agentapplication.CreateRunInput{
		ConversationID: ownerConversation.ID, Content: "owner full", ClientRunID: "owner-full",
		ResponseLanguage: agentdomain.ResponseLanguageEnglish,
		Budget:           agentdomain.Budget{MaxSteps: 5, MaxTotalTokens: 48_000, DeadlineAt: time.Now().Add(time.Minute)},
	})
	if !errors.Is(err, agentapplication.ErrOwnerQueueCapacity) {
		t.Fatalf("owner capacity error = %v", err)
	}
	globalConversation := createAgentTestConversation(t, ctx, pool, ownerB, "global capacity")
	_, err = repository.CreateRun(ctx, ownerB, agentapplication.CreateRunInput{
		ConversationID: globalConversation.ID, Content: "global full", ClientRunID: "global-full",
		ResponseLanguage: agentdomain.ResponseLanguageEnglish,
		Budget:           agentdomain.Budget{MaxSteps: 5, MaxTotalTokens: 48_000, DeadlineAt: time.Now().Add(time.Minute)},
	})
	if !errors.Is(err, agentapplication.ErrGlobalQueueCapacity) {
		t.Fatalf("global capacity error = %v", err)
	}

	if _, err := pool.Exec(
		ctx,
		`UPDATE agent_runs
		 SET status = 'processing', attempt_count = 1, started_at = CURRENT_TIMESTAMP
		 WHERE id = $1`,
		first.Run.ID,
	); err == nil {
		t.Fatal("migration allowed processing run without lease fields")
	}
	if _, err := pool.Exec(
		ctx,
		`UPDATE agent_runs
		 SET worker_id = 'invalid', lease_token = 'invalid',
		     lease_expires_at = CURRENT_TIMESTAMP + INTERVAL '1 minute',
		     heartbeat_at = CURRENT_TIMESTAMP
		 WHERE id = $1`,
		first.Run.ID,
	); err == nil {
		t.Fatal("migration allowed queued run with lease fields")
	}
}

func TestAgentAndAnswerCreationAreMutuallyExclusive(t *testing.T) {
	if os.Getenv("RUN_DATABASE_TESTS") != "1" {
		t.Skip("set RUN_DATABASE_TESTS=1 to run PostgreSQL integration tests")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	pool := openIsolatedDocumentTestPool(t, ctx)
	owner := answerJobTestOwnerScope(t, ctx, pool, "agent-answer-mutual")
	agentRepository := newAgentTestRepository(pool, "agent-worker-mutual")
	answerRepository := postgresrepository.NewAnswerJobRepository(
		pool,
		answerapplication.JobAdmissionLimits{MaxQueuedJobsPerOwner: 10, MaxQueuedJobsGlobal: 20},
		answerapplication.JobSchedulingPolicy{
			MaxInFlightPerOwner: 1, MaxBorrowedInFlightPerOwner: 1, StarvationThreshold: time.Second,
		},
	)
	conversationRepository := postgresrepository.NewConversationRepository(pool)

	answerConversation := createAgentTestConversation(t, ctx, pool, owner, "answer first")
	answerTurn, err := conversationRepository.StartTurn(ctx, owner, conversationdomain.StartTurnInput{
		ConversationID: answerConversation.ID, Content: "answer first",
	})
	if err != nil {
		t.Fatalf("start answer-first turn: %v", err)
	}
	conversationID := answerConversation.ID
	userMessageID := answerTurn.UserMessage.ID
	assistantMessageID := answerTurn.AssistantMessage.ID
	answerJob, err := answerRepository.CreateAnswerJob(ctx, owner, answerapplication.Input{
		Query: "answer first", TopK: 5, ResponseLanguage: answerapplication.ResponseLanguageEnglish,
		ConversationID: &conversationID, UserMessageID: &userMessageID, AssistantMessageID: &assistantMessageID,
	})
	if err != nil {
		t.Fatalf("create answer-first job: %v", err)
	}
	_, err = agentRepository.CreateRun(ctx, owner, agentapplication.CreateRunInput{
		ConversationID: answerConversation.ID, Content: "agent blocked", ClientRunID: "blocked-by-answer",
		ResponseLanguage: agentdomain.ResponseLanguageEnglish,
		Budget:           agentdomain.Budget{MaxSteps: 5, MaxTotalTokens: 48_000, DeadlineAt: time.Now().Add(time.Minute)},
	})
	if !errors.Is(err, agentapplication.ErrConversationExecutionActive) {
		t.Fatalf("answer-to-agent conflict error = %v", err)
	}
	if _, err := answerRepository.CancelAnswerJob(ctx, owner, answerJob.ID); err != nil {
		t.Fatalf("cancel answer-first fixture: %v", err)
	}

	agentConversation := createAgentTestConversation(t, ctx, pool, owner, "agent first")
	queueAgentTestRun(t, ctx, agentRepository, owner, agentConversation.ID, "agent-first")
	answerAfterAgentTurn, err := conversationRepository.StartTurn(ctx, owner, conversationdomain.StartTurnInput{
		ConversationID: agentConversation.ID, Content: "answer blocked",
	})
	if err != nil {
		t.Fatalf("start answer-after-agent turn: %v", err)
	}
	conversationID = agentConversation.ID
	userMessageID = answerAfterAgentTurn.UserMessage.ID
	assistantMessageID = answerAfterAgentTurn.AssistantMessage.ID
	_, err = answerRepository.CreateAnswerJob(ctx, owner, answerapplication.Input{
		Query: "answer blocked", TopK: 5, ResponseLanguage: answerapplication.ResponseLanguageEnglish,
		ConversationID: &conversationID, UserMessageID: &userMessageID, AssistantMessageID: &assistantMessageID,
	})
	if !errors.Is(err, answerapplication.ErrConversationAnswerActive) {
		t.Fatalf("agent-to-answer conflict error = %v", err)
	}

	raceConversation := createAgentTestConversation(t, ctx, pool, owner, "agent answer race")
	raceTurn, err := conversationRepository.StartTurn(ctx, owner, conversationdomain.StartTurnInput{
		ConversationID: raceConversation.ID, Content: "race answer",
	})
	if err != nil {
		t.Fatalf("start agent-answer race turn: %v", err)
	}
	raceConversationID := raceConversation.ID
	raceUserMessageID := raceTurn.UserMessage.ID
	raceAssistantMessageID := raceTurn.AssistantMessage.ID
	start := make(chan struct{})
	errorsCh := make(chan error, 2)
	var done sync.WaitGroup
	done.Add(2)
	go func() {
		defer done.Done()
		<-start
		_, createErr := agentRepository.CreateRun(ctx, owner, agentapplication.CreateRunInput{
			ConversationID: raceConversation.ID, Content: "race agent", ClientRunID: "race-agent",
			ResponseLanguage: agentdomain.ResponseLanguageEnglish,
			Budget:           agentdomain.Budget{MaxSteps: 5, MaxTotalTokens: 48_000, DeadlineAt: time.Now().Add(time.Minute)},
		})
		errorsCh <- createErr
	}()
	go func() {
		defer done.Done()
		<-start
		_, createErr := answerRepository.CreateAnswerJob(ctx, owner, answerapplication.Input{
			Query: "race answer", TopK: 5, ResponseLanguage: answerapplication.ResponseLanguageEnglish,
			ConversationID: &raceConversationID,
			UserMessageID:  &raceUserMessageID, AssistantMessageID: &raceAssistantMessageID,
		})
		errorsCh <- createErr
	}()
	close(start)
	done.Wait()
	close(errorsCh)
	successCount := 0
	conflictCount := 0
	for createErr := range errorsCh {
		switch {
		case createErr == nil:
			successCount++
		case errors.Is(createErr, agentapplication.ErrConversationExecutionActive),
			errors.Is(createErr, answerapplication.ErrConversationAnswerActive):
			conflictCount++
		default:
			t.Fatalf("unexpected agent-answer race error: %v", createErr)
		}
	}
	if successCount != 1 || conflictCount != 1 {
		t.Fatalf("agent-answer race successes=%d conflicts=%d", successCount, conflictCount)
	}
	var activeAnswerCount int
	var activeAgentCount int
	if err := pool.QueryRow(
		ctx,
		`SELECT COUNT(*) FROM answer_jobs WHERE conversation_id = $1 AND status IN ('queued', 'processing')`,
		raceConversation.ID,
	).Scan(&activeAnswerCount); err != nil {
		t.Fatalf("count active race answers: %v", err)
	}
	if err := pool.QueryRow(
		ctx,
		`SELECT COUNT(*) FROM agent_runs WHERE conversation_id = $1 AND status IN ('queued', 'processing')`,
		raceConversation.ID,
	).Scan(&activeAgentCount); err != nil {
		t.Fatalf("count active race agents: %v", err)
	}
	if activeAnswerCount+activeAgentCount != 1 {
		t.Fatalf("agent-answer race active answers=%d agents=%d", activeAnswerCount, activeAgentCount)
	}
}

func TestAgentQueueClaimsOwnersFairlyAndBorrowsIdleCapacity(t *testing.T) {
	if os.Getenv("RUN_DATABASE_TESTS") != "1" {
		t.Skip("set RUN_DATABASE_TESTS=1 to run PostgreSQL integration tests")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	pool := openIsolatedDocumentTestPool(t, ctx)
	ownerA := answerJobTestOwnerScope(t, ctx, pool, "agent-fair-a")
	ownerB := answerJobTestOwnerScope(t, ctx, pool, "agent-fair-b")
	repository := newAgentTestRepository(pool, "agent-worker-fair")
	for index := range 3 {
		conversation := createAgentTestConversation(t, ctx, pool, ownerA, fmt.Sprintf("owner A %d", index))
		queueAgentTestRun(t, ctx, repository, ownerA, conversation.ID, fmt.Sprintf("owner-a-%d", index))
	}
	conversationB := createAgentTestConversation(t, ctx, pool, ownerB, "owner B")
	queueAgentTestRun(t, ctx, repository, ownerB, conversationB.ID, "owner-b")

	first, err := repository.ClaimNextRun(ctx)
	if err != nil {
		t.Fatalf("claim first fair run: %v", err)
	}
	second, err := repository.ClaimNextRun(ctx)
	if err != nil {
		t.Fatalf("claim second fair run: %v", err)
	}
	if first.OwnerUserID == second.OwnerUserID {
		t.Fatalf("first two fair claims used same owner %d", first.OwnerUserID)
	}
	third, err := repository.ClaimNextRun(ctx)
	if err != nil {
		t.Fatalf("claim borrowed run: %v", err)
	}
	if third.OwnerUserID != ownerA.OwnerUserID() {
		t.Fatalf("borrowed run owner = %d, want owner A %d", third.OwnerUserID, ownerA.OwnerUserID())
	}
	if _, err := repository.ClaimNextRun(ctx); !errors.Is(err, agentapplication.ErrNoQueuedRun) {
		t.Fatalf("claim above borrowed limit: %v", err)
	}
	stats, err := repository.GetQueueStats(ctx)
	if err != nil || stats.QueuedCount != 1 || stats.ProcessingCount != 3 || stats.MaxOwnerProcessingCount != 2 {
		t.Fatalf("agent queue stats = %+v, %v", stats, err)
	}
}

func TestAgentConcurrentClaimRechecksOwnerLimit(t *testing.T) {
	if os.Getenv("RUN_DATABASE_TESTS") != "1" {
		t.Skip("set RUN_DATABASE_TESTS=1 to run PostgreSQL integration tests")
	}
	t.Run("fixed limit", func(t *testing.T) { testOverlappingAgentClaims(t, false) })
	t.Run("base slots before borrowing", func(t *testing.T) { testOverlappingAgentClaims(t, true) })
}

func testOverlappingAgentClaims(t *testing.T, allowBorrowing bool) {
	t.Helper()
	ctx, cancel := context.WithTimeout(t.Context(), 20*time.Second)
	defer cancel()
	pool := openIsolatedDocumentTestPool(t, ctx)
	owner := answerJobTestOwnerScope(t, ctx, pool, "agent-overlap")
	borrowedLimit := 1
	if allowBorrowing {
		borrowedLimit = 2
	}
	newClaimer := func(p *pgxpool.Pool) *postgresrepository.AgentRepository {
		return postgresrepository.NewAgentRepository(p,
			agentdomain.AdmissionLimits{MaxQueuedRunsPerOwner: 2, MaxQueuedRunsGlobal: 3},
			agentdomain.SchedulingPolicy{MaxInFlightPerOwner: 1, MaxBorrowedInFlightPerOwner: borrowedLimit, StarvationThreshold: time.Second})
	}
	winner := newClaimer(pool)
	for _, name := range []string{"first", "second"} {
		conversation := createAgentTestConversation(t, ctx, pool, owner, name)
		queueAgentTestRun(t, ctx, winner, owner, conversation.ID, name)
	}
	var otherRunID int64
	if allowBorrowing {
		other := answerJobTestOwnerScope(t, ctx, pool, "agent-overlap-other")
		conversation := createAgentTestConversation(t, ctx, pool, other, "other")
		otherRunID = queueAgentTestRun(t, ctx, winner, other, conversation.ID, "other").Run.ID
	}

	// Only the late claimer sees this session-local view. Pause its candidate
	// statement after taking its snapshot but before it can lock the schedule.
	cfg := pool.Config()
	cfg.MaxConns = 1
	latePool, err := pgxpool.NewWithConfig(ctx, cfg)
	if err != nil {
		t.Fatal(err)
	}
	defer latePool.Close()
	if _, err := latePool.Exec(ctx, `CREATE TEMP VIEW agent_owner_schedules AS
		SELECT * FROM agent_owner_schedules
		WHERE EXISTS (SELECT FROM pg_advisory_xact_lock('agent_runs'::regclass::oid::bigint))`); err != nil {
		t.Fatal(err)
	}
	barrier, err := pool.Begin(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer barrier.Rollback(context.Background())
	if _, err := barrier.Exec(ctx, `SELECT pg_advisory_xact_lock('agent_runs'::regclass::oid::bigint)`); err != nil {
		t.Fatal(err)
	}
	type claimResult struct {
		run agentdomain.Run
		err error
	}
	lateClaim := make(chan claimResult, 1)
	go func() {
		run, err := newClaimer(latePool).ClaimNextRun(ctx)
		lateClaim <- claimResult{run: run, err: err}
	}()
	for {
		var waiting bool
		if err := barrier.QueryRow(ctx, `SELECT EXISTS (SELECT FROM pg_locks
			WHERE locktype = 'advisory' AND objid = 'agent_runs'::regclass::oid AND NOT granted)`).Scan(&waiting); err != nil {
			t.Fatal(err)
		}
		if waiting {
			break
		}
		select {
		case result := <-lateClaim:
			t.Fatalf("late claim returned before the barrier: %v", result.err)
		case <-time.After(time.Millisecond):
		}
	}
	if first, err := winner.ClaimNextRun(ctx); err != nil || first.OwnerUserID != owner.OwnerUserID() {
		t.Fatalf("first claim owner=%d, err=%v", first.OwnerUserID, err)
	}
	if err := barrier.Commit(ctx); err != nil {
		t.Fatal(err)
	}
	late := <-lateClaim
	var processing, queued int
	if err := pool.QueryRow(ctx, `SELECT COUNT(*) FILTER (WHERE status='processing'),
		COUNT(*) FILTER (WHERE status='queued' AND attempt_count=0)
		FROM agent_runs WHERE owner_user_id=$1`, owner.OwnerUserID()).Scan(&processing, &queued); err != nil {
		t.Fatal(err)
	}
	if processing != 1 || queued != 1 {
		t.Fatalf("late claim error=%v, committed processing=%d, untouched queued=%d; want 1 processing and 1 queued", late.err, processing, queued)
	}
	if allowBorrowing {
		if late.err != nil || late.run.ID != otherRunID {
			t.Fatalf("late claim run=%d err=%v; want other owner's base slot (run %d)", late.run.ID, late.err, otherRunID)
		}
	} else if !errors.Is(late.err, agentapplication.ErrNoQueuedRun) {
		t.Fatalf("late claim error=%v; want no queued run", late.err)
	}
}
