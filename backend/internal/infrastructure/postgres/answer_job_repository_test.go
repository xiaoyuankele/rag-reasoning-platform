package postgres_test

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"

	answerapplication "rag-reasoning-platform/backend/internal/application/answer"
	accessdomain "rag-reasoning-platform/backend/internal/domain/access"
	conversationdomain "rag-reasoning-platform/backend/internal/domain/conversation"
	documentdomain "rag-reasoning-platform/backend/internal/domain/document"
	postgresrepository "rag-reasoning-platform/backend/internal/infrastructure/postgres"
	"rag-reasoning-platform/backend/migrations"
)

// TestAnswerJobRepository 使用真实 PostgreSQL 验证异步问答的权限、状态和并发领取。
// 测试只写临时 schema，不调用 Embedding 或 Generation Provider。
func TestAnswerJobRepository(t *testing.T) {
	if os.Getenv("RUN_DATABASE_TESTS") != "1" {
		t.Skip("set RUN_DATABASE_TESTS=1 to run PostgreSQL integration tests")
	}

	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	pool := openIsolatedDocumentTestPool(t, ctx)
	for indexName, fragments := range map[string][]string{
		"idx_answer_jobs_standalone_terminal_completed": {"conversation_id IS NULL", "completed_at", "status"},
		"idx_answer_jobs_standalone_owner_created":      {"conversation_id IS NULL", "owner_user_id", "created_at DESC"},
		"idx_answer_jobs_live_document":                 {"document_id", "document_id IS NOT NULL"},
		"idx_answer_jobs_standalone_succeeded_sources":  {"USING gin", "jsonb_path_ops", "conversation_id IS NULL", "status = 'succeeded'"},
	} {
		var definition string
		if err := pool.QueryRow(
			ctx,
			`SELECT indexdef FROM pg_indexes WHERE schemaname = CURRENT_SCHEMA() AND indexname = $1`,
			indexName,
		).Scan(&definition); err != nil {
			t.Fatalf("find retention index %s: %v", indexName, err)
		}
		for _, fragment := range fragments {
			if !strings.Contains(definition, fragment) {
				t.Fatalf("retention index %s = %q, missing %q", indexName, definition, fragment)
			}
		}
	}
	var legacyIndexCount int
	if err := pool.QueryRow(
		ctx,
		`SELECT COUNT(*) FROM pg_indexes WHERE schemaname = CURRENT_SCHEMA() AND indexname = 'idx_answer_jobs_terminal_completed'`,
	).Scan(&legacyIndexCount); err != nil || legacyIndexCount != 0 {
		t.Fatalf("legacy retention index count/error = %d / %v", legacyIndexCount, err)
	}
	repository := postgresrepository.NewAnswerJobRepository(
		pool,
		answerapplication.JobAdmissionLimits{
			MaxQueuedJobsPerOwner: 10,
			MaxQueuedJobsGlobal:   100,
		},
		answerapplication.JobSchedulingPolicy{
			MaxInFlightPerOwner:         1,
			MaxBorrowedInFlightPerOwner: 1,
			StarvationThreshold:         30 * time.Second,
		},
	)

	ownerAScope := answerJobTestOwnerScope(t, ctx, pool, "answer-owner-a")
	ownerBScope := answerJobTestOwnerScope(t, ctx, pool, "answer-owner-b")

	t.Run("lists only current owner jobs with stable pagination", func(t *testing.T) {
		ownerListAScope := answerJobTestOwnerScope(t, ctx, pool, "answer-list-owner-a")
		ownerListBScope := answerJobTestOwnerScope(t, ctx, pool, "answer-list-owner-b")
		ownerAJobs := make([]answerapplication.Job, 0, 3)
		for index := range 3 {
			job, err := repository.CreateAnswerJob(
				ctx,
				ownerListAScope,
				answerapplication.Input{
					Query:            fmt.Sprintf("owner A list question %d", index),
					TopK:             5,
					ResponseLanguage: answerapplication.ResponseLanguageAuto,
				},
			)
			if err != nil {
				t.Fatalf("create owner A list fixture: %v", err)
			}
			ownerAJobs = append(ownerAJobs, job)
		}
		ownerBJob, err := repository.CreateAnswerJob(
			ctx,
			ownerListBScope,
			answerapplication.Input{
				Query:            "owner B private question",
				TopK:             5,
				ResponseLanguage: answerapplication.ResponseLanguageAuto,
			},
		)
		if err != nil {
			t.Fatalf("create owner B list fixture: %v", err)
		}

		firstPage, err := repository.ListAnswerJobs(
			ctx,
			ownerListAScope,
			answerapplication.JobListOptions{Limit: 2, Offset: 0},
		)
		if err != nil {
			t.Fatalf("list owner A first page: %v", err)
		}
		if firstPage.Total != 3 || len(firstPage.Jobs) != 2 ||
			firstPage.Jobs[0].ID != ownerAJobs[2].ID ||
			firstPage.Jobs[1].ID != ownerAJobs[1].ID {
			t.Fatalf("first page = %+v, want latest two of three owner A jobs", firstPage)
		}

		secondPage, err := repository.ListAnswerJobs(
			ctx,
			ownerListAScope,
			answerapplication.JobListOptions{Limit: 2, Offset: 2},
		)
		if err != nil || secondPage.Total != 3 || len(secondPage.Jobs) != 1 ||
			secondPage.Jobs[0].ID != ownerAJobs[0].ID {
			t.Fatalf("second page = %+v, %v, want oldest owner A job", secondPage, err)
		}

		ownerBPage, err := repository.ListAnswerJobs(
			ctx,
			ownerListBScope,
			answerapplication.JobListOptions{Limit: 20, Offset: 0},
		)
		if err != nil || ownerBPage.Total != 1 || len(ownerBPage.Jobs) != 1 ||
			ownerBPage.Jobs[0].ID != ownerBJob.ID {
			t.Fatalf("owner B page = %+v, %v, want only private job", ownerBPage, err)
		}

		for _, fixture := range append(ownerAJobs, ownerBJob) {
			scope := ownerListAScope
			if fixture.OwnerUserID == ownerListBScope.OwnerUserID() {
				scope = ownerListBScope
			}
			if _, err := repository.CancelAnswerJob(ctx, scope, fixture.ID); err != nil {
				t.Fatalf("cancel list fixture %d: %v", fixture.ID, err)
			}
		}
	})

	t.Run("scopes lookup and makes cancellation idempotent", func(t *testing.T) {
		job, err := repository.CreateAnswerJob(
			ctx,
			ownerAScope,
			answerapplication.Input{
				Query:            "owner isolated question",
				TopK:             5,
				ResponseLanguage: answerapplication.ResponseLanguageAuto,
			},
		)
		if err != nil {
			t.Fatalf("create answer job: %v", err)
		}

		if _, err := repository.GetAnswerJobByID(ctx, ownerBScope, job.ID); !errors.Is(err, answerapplication.ErrAnswerJobNotFound) {
			t.Fatalf("cross-owner lookup error = %v, want not found", err)
		}

		canceled, err := repository.CancelAnswerJob(ctx, ownerAScope, job.ID)
		if err != nil {
			t.Fatalf("cancel answer job: %v", err)
		}
		if canceled.Status != answerapplication.JobStatusCanceled || canceled.CompletedAt == nil {
			t.Fatalf("canceled job = %+v, want canceled terminal snapshot", canceled)
		}
		repeated, err := repository.CancelAnswerJob(ctx, ownerAScope, job.ID)
		if err != nil || repeated.Status != answerapplication.JobStatusCanceled {
			t.Fatalf("repeat cancellation = (%+v, %v), want idempotent canceled", repeated, err)
		}
	})

	t.Run("serializes the final global queue slot", func(t *testing.T) {
		limitedRepository := postgresrepository.NewAnswerJobRepository(
			pool,
			answerapplication.JobAdmissionLimits{
				MaxQueuedJobsPerOwner: 1,
				MaxQueuedJobsGlobal:   1,
			},
			answerapplication.JobSchedulingPolicy{
				MaxInFlightPerOwner:         1,
				MaxBorrowedInFlightPerOwner: 1,
				StarvationThreshold:         30 * time.Second,
			},
		)
		ownerCScope := answerJobTestOwnerScope(t, ctx, pool, "answer-owner-c")
		ownerDScope := answerJobTestOwnerScope(t, ctx, pool, "answer-owner-d")

		type createResult struct {
			job   answerapplication.Job
			scope accessdomain.OwnerScope
			err   error
		}
		start := make(chan struct{})
		results := make(chan createResult, 2)
		var group sync.WaitGroup
		for index, scope := range []accessdomain.OwnerScope{ownerCScope, ownerDScope} {
			group.Add(1)
			go func(index int, scope accessdomain.OwnerScope) {
				defer group.Done()
				<-start
				job, err := limitedRepository.CreateAnswerJob(
					ctx,
					scope,
					answerapplication.Input{
						Query:            fmt.Sprintf("capacity question %d", index),
						TopK:             5,
						ResponseLanguage: answerapplication.ResponseLanguageAuto,
					},
				)
				results <- createResult{job: job, scope: scope, err: err}
			}(index, scope)
		}
		close(start)
		group.Wait()
		close(results)

		createdCount := 0
		capacityRejectedCount := 0
		for result := range results {
			switch {
			case result.err == nil:
				createdCount++
				if _, err := limitedRepository.CancelAnswerJob(ctx, result.scope, result.job.ID); err != nil {
					t.Fatalf("cancel capacity fixture: %v", err)
				}
			case errors.Is(result.err, answerapplication.ErrAnswerGlobalQueueCapacity):
				capacityRejectedCount++
			default:
				t.Fatalf("unexpected capacity create error: %v", result.err)
			}
		}
		if createdCount != 1 || capacityRejectedCount != 1 {
			t.Fatalf(
				"created/rejected = %d/%d, want 1/1",
				createdCount,
				capacityRejectedCount,
			)
		}
	})

	t.Run("concurrent workers claim different owners and persist results", func(t *testing.T) {
		fixtures := []struct {
			scope accessdomain.OwnerScope
			query string
		}{
			{scope: ownerAScope, query: "first owner question"},
			{scope: ownerBScope, query: "second owner question"},
		}
		for _, fixture := range fixtures {
			if _, err := repository.CreateAnswerJob(
				ctx,
				fixture.scope,
				answerapplication.Input{
					Query:            fixture.query,
					TopK:             5,
					ResponseLanguage: answerapplication.ResponseLanguageChinese,
				},
			); err != nil {
				t.Fatalf("create concurrent answer job: %v", err)
			}
		}

		type claimResult struct {
			job answerapplication.Job
			err error
		}
		start := make(chan struct{})
		results := make(chan claimResult, 2)
		var group sync.WaitGroup
		group.Add(2)
		for range 2 {
			go func() {
				defer group.Done()
				<-start
				job, err := repository.ClaimNextAnswerJob(ctx)
				results <- claimResult{job: job, err: err}
			}()
		}
		close(start)
		group.Wait()
		close(results)

		claimedOwners := make(map[int64]struct{}, 2)
		claimedJobs := make([]answerapplication.Job, 0, 2)
		for result := range results {
			if result.err != nil {
				t.Fatalf("claim answer job concurrently: %v", result.err)
			}
			if result.job.Status != answerapplication.JobStatusProcessing || result.job.AttemptCount != 1 {
				t.Fatalf("claimed job = %+v, want first processing attempt", result.job)
			}
			if _, duplicate := claimedOwners[result.job.OwnerUserID]; duplicate {
				t.Fatalf("both workers claimed owner %d despite base limit 1", result.job.OwnerUserID)
			}
			claimedOwners[result.job.OwnerUserID] = struct{}{}
			claimedJobs = append(claimedJobs, result.job)
		}
		if len(claimedOwners) != 2 {
			t.Fatalf("claimed owners = %v, want two distinct owners", claimedOwners)
		}
		queueStats, err := repository.GetAnswerJobQueueStats(ctx)
		if err != nil {
			t.Fatalf("get answer job queue stats after claim: %v", err)
		}
		if queueStats.QueuedCount != 0 || queueStats.ReadyQueuedCount != 0 ||
			queueStats.ProcessingCount != 2 ||
			queueStats.MaxOwnerProcessingCount != 1 ||
			queueStats.OldestReadyWait != 0 {
			t.Fatalf("queue stats after claim = %+v, want two owners processing", queueStats)
		}

		for _, job := range claimedJobs {
			output := answerapplication.Output{
				Query:            job.Query,
				Answer:           "integration answer",
				ResponseLanguage: answerapplication.ResponseLanguageChinese,
				Sources:          []answerapplication.Source{},
				PromptTokens:     8,
				CompletionTokens: 3,
				TotalTokens:      11,
			}
			if err := repository.MarkAnswerJobSucceeded(
				ctx,
				job.ID,
				job.LeaseToken,
				output,
			); err != nil {
				t.Fatalf("mark answer job %d succeeded: %v", job.ID, err)
			}
			scope, err := accessdomain.NewOwnerScope(job.OwnerUserID)
			if err != nil {
				t.Fatalf("rebuild owner scope: %v", err)
			}
			stored, err := repository.GetAnswerJobByID(ctx, scope, job.ID)
			if err != nil {
				t.Fatalf("get completed answer job %d: %v", job.ID, err)
			}
			if stored.Status != answerapplication.JobStatusSucceeded || stored.Result == nil ||
				stored.Result.Answer != "integration answer" || stored.Result.TotalTokens != 11 ||
				stored.Result.Sources == nil {
				t.Fatalf("stored answer job = %+v, want complete result snapshot", stored)
			}
		}
		queueStats, err = repository.GetAnswerJobQueueStats(ctx)
		if err != nil {
			t.Fatalf("get answer job queue stats after completion: %v", err)
		}
		if queueStats.QueuedCount != 0 || queueStats.ProcessingCount != 0 {
			t.Fatalf("queue stats after completion = %+v, want an idle queue", queueStats)
		}
	})

	t.Run("deletes only expired terminal jobs", func(t *testing.T) {
		expired, err := repository.CreateAnswerJob(
			ctx,
			ownerAScope,
			answerapplication.Input{
				Query:            "expired answer",
				TopK:             5,
				ResponseLanguage: answerapplication.ResponseLanguageAuto,
			},
		)
		if err != nil {
			t.Fatalf("create expired fixture: %v", err)
		}
		expired, err = repository.CancelAnswerJob(ctx, ownerAScope, expired.ID)
		if err != nil {
			t.Fatalf("cancel expired fixture: %v", err)
		}
		fresh, err := repository.CreateAnswerJob(
			ctx,
			ownerAScope,
			answerapplication.Input{
				Query:            "fresh answer",
				TopK:             5,
				ResponseLanguage: answerapplication.ResponseLanguageAuto,
			},
		)
		if err != nil {
			t.Fatalf("create fresh fixture: %v", err)
		}
		fresh, err = repository.CancelAnswerJob(ctx, ownerAScope, fresh.ID)
		if err != nil {
			t.Fatalf("cancel fresh fixture: %v", err)
		}
		active, err := repository.CreateAnswerJob(
			ctx,
			ownerAScope,
			answerapplication.Input{
				Query:            "active answer",
				TopK:             5,
				ResponseLanguage: answerapplication.ResponseLanguageAuto,
			},
		)
		if err != nil {
			t.Fatalf("create active fixture: %v", err)
		}

		cutoff := time.Now().Add(-time.Hour)
		if _, err := pool.Exec(
			ctx,
			`UPDATE answer_jobs SET completed_at = $2 WHERE id = $1`,
			expired.ID,
			cutoff.Add(-time.Hour),
		); err != nil {
			t.Fatalf("backdate expired fixture: %v", err)
		}
		deletedCount, err := repository.DeleteExpiredStandaloneAnswerJobs(ctx, cutoff, 10)
		if err != nil || deletedCount != 1 {
			t.Fatalf("DeleteExpiredStandaloneAnswerJobs() = %d, %v, want 1 nil", deletedCount, err)
		}
		if _, err := repository.GetAnswerJobByID(ctx, ownerAScope, expired.ID); !errors.Is(err, answerapplication.ErrAnswerJobNotFound) {
			t.Fatalf("expired lookup error = %v, want not found", err)
		}
		if _, err := repository.GetAnswerJobByID(ctx, ownerAScope, fresh.ID); err != nil {
			t.Fatalf("fresh terminal job was deleted: %v", err)
		}
		if _, err := repository.GetAnswerJobByID(ctx, ownerAScope, active.ID); err != nil {
			t.Fatalf("active job was deleted: %v", err)
		}
		if _, err := repository.CancelAnswerJob(ctx, ownerAScope, active.ID); err != nil {
			t.Fatalf("cancel active cleanup fixture: %v", err)
		}
	})
}

func TestAnswerJobRepositoryFinalizesLinkedConversationAssistant(t *testing.T) {
	if os.Getenv("RUN_DATABASE_TESTS") != "1" {
		t.Skip("set RUN_DATABASE_TESTS=1 to run PostgreSQL integration tests")
	}

	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	pool := openIsolatedDocumentTestPool(t, ctx)
	owner := answerJobTestOwnerScope(t, ctx, pool, "conversation-answer")
	conversationRepository := postgresrepository.NewConversationRepository(pool)
	answerRepository := postgresrepository.NewAnswerJobRepository(
		pool,
		answerapplication.JobAdmissionLimits{MaxQueuedJobsPerOwner: 5, MaxQueuedJobsGlobal: 20},
		answerapplication.JobSchedulingPolicy{
			MaxInFlightPerOwner: 1, MaxBorrowedInFlightPerOwner: 1, StarvationThreshold: time.Second,
		},
	)

	created, err := conversationRepository.CreateConversation(ctx, owner, conversationdomain.CreateInput{Title: "M2"})
	if err != nil {
		t.Fatalf("create conversation: %v", err)
	}
	turn, err := conversationRepository.StartTurn(ctx, owner, conversationdomain.StartTurnInput{
		ConversationID: created.ID,
		Content:        "Explain the source",
	})
	if err != nil {
		t.Fatalf("start conversation turn: %v", err)
	}
	conversationID := created.ID
	userMessageID := turn.UserMessage.ID
	assistantMessageID := turn.AssistantMessage.ID
	job, err := answerRepository.CreateAnswerJob(ctx, owner, answerapplication.Input{
		Query: "Explain the source", TopK: 5, ResponseLanguage: answerapplication.ResponseLanguageChinese,
		ConversationID: &conversationID, UserMessageID: &userMessageID, AssistantMessageID: &assistantMessageID,
	})
	if err != nil {
		t.Fatalf("create linked answer job: %v", err)
	}
	if job.ConversationID == nil || *job.ConversationID != conversationID ||
		job.AssistantMessageID == nil || *job.AssistantMessageID != assistantMessageID {
		t.Fatalf("linked answer job = %+v", job)
	}
	repeated, err := answerRepository.CreateAnswerJob(ctx, owner, answerapplication.Input{
		Query: "Explain the source", TopK: 5, ResponseLanguage: answerapplication.ResponseLanguageChinese,
		ConversationID: &conversationID, UserMessageID: &userMessageID, AssistantMessageID: &assistantMessageID,
	})
	if err != nil || repeated.ID != job.ID {
		t.Fatalf("idempotent linked answer job = %+v, %v; want original %d", repeated, err, job.ID)
	}
	secondTurn, err := conversationRepository.StartTurn(ctx, owner, conversationdomain.StartTurnInput{
		ConversationID: created.ID,
		Content:        "Ask another question",
	})
	if err != nil {
		t.Fatalf("start second conversation turn: %v", err)
	}
	secondUserMessageID := secondTurn.UserMessage.ID
	secondAssistantMessageID := secondTurn.AssistantMessage.ID
	if _, err := answerRepository.CreateAnswerJob(ctx, owner, answerapplication.Input{
		Query: "Ask another question", TopK: 5, ResponseLanguage: answerapplication.ResponseLanguageChinese,
		ConversationID: &conversationID, UserMessageID: &secondUserMessageID, AssistantMessageID: &secondAssistantMessageID,
	}); !errors.Is(err, answerapplication.ErrConversationAnswerActive) {
		t.Fatalf("second active conversation job error = %v, want ErrConversationAnswerActive", err)
	}
	if _, err := conversationRepository.FailAssistantIfUnqueued(ctx, owner, conversationdomain.FailAssistantIfUnqueuedInput{
		ConversationID: conversationID,
		MessageID:      secondAssistantMessageID,
		Content:        "another answer is already active",
	}); err != nil {
		t.Fatalf("close second unqueued assistant: %v", err)
	}

	claimed, err := answerRepository.ClaimNextAnswerJob(ctx)
	if err != nil {
		t.Fatalf("claim linked answer job: %v", err)
	}
	if err := answerRepository.MarkAnswerJobSucceeded(ctx, claimed.ID, claimed.LeaseToken, answerapplication.Output{
		Query: "Explain the source", Answer: "这是回答。", ResponseLanguage: answerapplication.ResponseLanguageChinese,
		Sources: []answerapplication.Source{}, PromptTokens: 2, CompletionTokens: 3, TotalTokens: 5,
	}); err != nil {
		t.Fatalf("complete linked answer job: %v", err)
	}

	messages, err := conversationRepository.ListMessages(ctx, owner, conversationID, conversationdomain.ListOptions{Limit: 10, Offset: 0})
	if err != nil {
		t.Fatalf("list completed conversation messages: %v", err)
	}
	if len(messages.Messages) != 4 || messages.Messages[1].Status != conversationdomain.MessageStatusCompleted ||
		messages.Messages[1].Content != "这是回答。" ||
		messages.Messages[3].Status != conversationdomain.MessageStatusFailed {
		t.Fatalf("completed conversation messages = %+v", messages.Messages)
	}
}

func TestAnswerJobRepositoryRejectsConversationWithActiveAgentRun(t *testing.T) {
	if os.Getenv("RUN_DATABASE_TESTS") != "1" {
		t.Skip("set RUN_DATABASE_TESTS=1 to run PostgreSQL integration tests")
	}

	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	pool := openIsolatedDocumentTestPool(t, ctx)
	owner := answerJobTestOwnerScope(t, ctx, pool, "answer-active-agent")
	conversationRepository := postgresrepository.NewConversationRepository(pool)
	agentRepository := newAgentTestRepository(pool, "agent-worker-answer-guard")
	answerRepository := postgresrepository.NewAnswerJobRepository(
		pool,
		answerapplication.JobAdmissionLimits{MaxQueuedJobsPerOwner: 5, MaxQueuedJobsGlobal: 20},
		answerapplication.JobSchedulingPolicy{
			MaxInFlightPerOwner: 1, MaxBorrowedInFlightPerOwner: 1, StarvationThreshold: time.Second,
		},
	)
	conversation := createAgentTestConversation(t, ctx, pool, owner, "active agent guard")
	queueAgentTestRun(t, ctx, agentRepository, owner, conversation.ID, "answer-guard-agent")
	turn, err := conversationRepository.StartTurn(ctx, owner, conversationdomain.StartTurnInput{
		ConversationID: conversation.ID,
		Content:        "answer must wait",
	})
	if err != nil {
		t.Fatalf("start answer turn behind active agent: %v", err)
	}
	conversationID := conversation.ID
	userMessageID := turn.UserMessage.ID
	assistantMessageID := turn.AssistantMessage.ID
	_, err = answerRepository.CreateAnswerJob(ctx, owner, answerapplication.Input{
		Query: "answer must wait", TopK: 5, ResponseLanguage: answerapplication.ResponseLanguageEnglish,
		ConversationID: &conversationID, UserMessageID: &userMessageID, AssistantMessageID: &assistantMessageID,
	})
	if !errors.Is(err, answerapplication.ErrConversationAnswerActive) {
		t.Fatalf("answer creation behind active agent error = %v", err)
	}
}

func TestAnswerJobRetentionPreservesConversationHistory(t *testing.T) {
	if os.Getenv("RUN_DATABASE_TESTS") != "1" {
		t.Skip("set RUN_DATABASE_TESTS=1 to run PostgreSQL integration tests")
	}

	ctx, cancel := context.WithTimeout(context.Background(), 45*time.Second)
	defer cancel()
	pool := openIsolatedDocumentTestPool(t, ctx)
	ownerA := answerJobTestOwnerScope(t, ctx, pool, "retention-owner-a")
	ownerB := answerJobTestOwnerScope(t, ctx, pool, "retention-owner-b")
	conversationRepository := postgresrepository.NewConversationRepository(pool)
	answerRepository := postgresrepository.NewAnswerJobRepository(
		pool,
		answerapplication.JobAdmissionLimits{MaxQueuedJobsPerOwner: 20, MaxQueuedJobsGlobal: 100},
		answerapplication.JobSchedulingPolicy{
			MaxInFlightPerOwner: 1, MaxBorrowedInFlightPerOwner: 1, StarvationThreshold: time.Second,
		},
	)
	jobService, err := answerapplication.NewJobService(answerRepository)
	if err != nil {
		t.Fatalf("create answer job service: %v", err)
	}
	cutoff := time.Now().UTC().Add(-time.Hour)

	finish := func(job answerapplication.Job, status answerapplication.JobStatus, answer string) answerapplication.Job {
		t.Helper()
		switch status {
		case answerapplication.JobStatusCanceled:
			canceled, err := answerRepository.CancelAnswerJob(ctx, ownerA, job.ID)
			if err != nil {
				t.Fatalf("cancel job %d: %v", job.ID, err)
			}
			job = canceled
		case answerapplication.JobStatusFailed:
			claimed, err := answerRepository.ClaimNextAnswerJob(ctx)
			if err != nil || claimed.ID != job.ID {
				t.Fatalf("claim failed fixture = %+v, %v; want %d", claimed, err, job.ID)
			}
			if err := answerRepository.MarkAnswerJobFailed(
				ctx, claimed.ID, claimed.LeaseToken,
				answerapplication.JobErrorCodeExecutionFailed, "generation failed",
			); err != nil {
				t.Fatalf("fail job %d: %v", job.ID, err)
			}
			job, err = answerRepository.GetAnswerJobByID(ctx, ownerA, job.ID)
			if err != nil {
				t.Fatalf("get failed job %d: %v", job.ID, err)
			}
		case answerapplication.JobStatusSucceeded:
			claimed, err := answerRepository.ClaimNextAnswerJob(ctx)
			if err != nil || claimed.ID != job.ID {
				t.Fatalf("claim succeeded fixture = %+v, %v; want %d", claimed, err, job.ID)
			}
			sources := make([]answerapplication.Source, 0)
			if job.ConversationID != nil {
				sources = []answerapplication.Source{{Citation: 1, ChunkID: 71, DocumentID: 81, OriginalName: "retained.pdf"}}
			}
			if err := answerRepository.MarkAnswerJobSucceeded(ctx, claimed.ID, claimed.LeaseToken, answerapplication.Output{
				Query: job.Query, Answer: answer, ResponseLanguage: answerapplication.ResponseLanguageEnglish,
				Sources:      sources,
				PromptTokens: 11, CompletionTokens: 5, TotalTokens: 16,
			}); err != nil {
				t.Fatalf("succeed job %d: %v", job.ID, err)
			}
			job, err = answerRepository.GetAnswerJobByID(ctx, ownerA, job.ID)
			if err != nil {
				t.Fatalf("get succeeded job %d: %v", job.ID, err)
			}
		default:
			t.Fatalf("unsupported terminal status %q", status)
		}
		if _, err := pool.Exec(ctx, `UPDATE answer_jobs SET completed_at = $2 WHERE id = $1`, job.ID, cutoff.Add(-time.Hour)); err != nil {
			t.Fatalf("backdate job %d: %v", job.ID, err)
		}
		return job
	}

	standalone := make([]answerapplication.Job, 0, 3)
	for _, fixture := range []struct {
		status answerapplication.JobStatus
		query  string
	}{
		{status: answerapplication.JobStatusSucceeded, query: "standalone succeeded"},
		{status: answerapplication.JobStatusFailed, query: "standalone failed"},
		{status: answerapplication.JobStatusCanceled, query: "standalone canceled"},
	} {
		job, err := answerRepository.CreateAnswerJob(ctx, ownerA, answerapplication.Input{
			Query: fixture.query, TopK: 5, ResponseLanguage: answerapplication.ResponseLanguageEnglish,
		})
		if err != nil {
			t.Fatalf("create %s: %v", fixture.query, err)
		}
		standalone = append(standalone, finish(job, fixture.status, "standalone answer"))
	}

	conversation, err := conversationRepository.CreateConversation(ctx, ownerA, conversationdomain.CreateInput{Title: "retained history"})
	if err != nil {
		t.Fatalf("create retained conversation: %v", err)
	}
	type linkedFixture struct {
		turn conversationdomain.StartTurnResult
		job  answerapplication.Job
	}
	linked := make([]linkedFixture, 0, 3)
	for index, fixture := range []struct {
		status answerapplication.JobStatus
		query  string
	}{
		{status: answerapplication.JobStatusSucceeded, query: "linked succeeded"},
		{status: answerapplication.JobStatusFailed, query: "linked failed"},
		{status: answerapplication.JobStatusCanceled, query: "linked canceled"},
	} {
		clientMessageID := fmt.Sprintf("retained-client-%d", index)
		turn, err := conversationRepository.StartTurn(ctx, ownerA, conversationdomain.StartTurnInput{
			ConversationID: conversation.ID, Content: fixture.query, ClientMessageID: &clientMessageID,
		})
		if err != nil {
			t.Fatalf("start %s: %v", fixture.query, err)
		}
		job, err := jobService.QueueConversation(ctx, ownerA, answerapplication.ConversationQueueInput{
			ConversationID: conversation.ID, UserMessageID: turn.UserMessage.ID, AssistantMessageID: turn.AssistantMessage.ID,
			Input: answerapplication.Input{Query: fixture.query, TopK: 7, ResponseLanguage: answerapplication.ResponseLanguageEnglish},
		})
		if err != nil {
			t.Fatalf("queue %s: %v", fixture.query, err)
		}
		linked = append(linked, linkedFixture{turn: turn, job: finish(job, fixture.status, "retained answer")})
	}
	standaloneList, err := answerRepository.ListAnswerJobs(ctx, ownerA, answerapplication.JobListOptions{Limit: 20})
	if err != nil || standaloneList.Total != 3 || len(standaloneList.Jobs) != 3 {
		t.Fatalf("standalone list before cleanup = %+v, %v", standaloneList, err)
	}
	for _, job := range standaloneList.Jobs {
		if job.ConversationID != nil || job.DocumentID != nil {
			t.Fatalf("standalone list leaked conversation job %+v", job)
		}
	}

	firstDeleted, err := answerRepository.DeleteExpiredStandaloneAnswerJobs(ctx, cutoff, 2)
	if err != nil || firstDeleted != 2 {
		t.Fatalf("first retention batch = %d, %v, want 2", firstDeleted, err)
	}
	secondDeleted, err := answerRepository.DeleteExpiredStandaloneAnswerJobs(ctx, cutoff, 2)
	if err != nil || secondDeleted != 1 {
		t.Fatalf("second retention batch = %d, %v, want 1", secondDeleted, err)
	}
	thirdDeleted, err := answerRepository.DeleteExpiredStandaloneAnswerJobs(ctx, cutoff, 2)
	if err != nil || thirdDeleted != 0 {
		t.Fatalf("third retention batch = %d, %v, want 0", thirdDeleted, err)
	}
	for _, job := range standalone {
		if _, err := answerRepository.GetAnswerJobByID(ctx, ownerA, job.ID); !errors.Is(err, answerapplication.ErrAnswerJobNotFound) {
			t.Fatalf("expired standalone job %d lookup error = %v, want not found", job.ID, err)
		}
	}
	for _, fixture := range linked {
		stored, err := answerRepository.GetAnswerJobByID(ctx, ownerA, fixture.job.ID)
		if err != nil || stored.Status != fixture.job.Status || stored.Query != fixture.job.Query ||
			stored.TopK != 7 || stored.RequestedResponseLanguage != answerapplication.ResponseLanguageEnglish {
			t.Fatalf("retained linked job = %+v, %v; want %+v", stored, err, fixture.job)
		}
		if _, err := answerRepository.GetAnswerJobByID(ctx, ownerB, fixture.job.ID); !errors.Is(err, answerapplication.ErrAnswerJobNotFound) {
			t.Fatalf("cross-owner retained job %d error = %v", fixture.job.ID, err)
		}
	}
	succeeded := linked[0]
	storedSucceeded, err := answerRepository.GetAnswerJobByID(ctx, ownerA, succeeded.job.ID)
	if err != nil || storedSucceeded.Result == nil || storedSucceeded.Result.Answer != "retained answer" ||
		storedSucceeded.Result.PromptTokens != 11 || storedSucceeded.Result.CompletionTokens != 5 ||
		storedSucceeded.Result.TotalTokens != 16 || len(storedSucceeded.Result.Sources) != 1 ||
		storedSucceeded.Result.Sources[0].OriginalName != "retained.pdf" {
		t.Fatalf("retained succeeded snapshot = %+v, %v", storedSucceeded, err)
	}
	replayed, err := jobService.QueueConversation(ctx, ownerA, answerapplication.ConversationQueueInput{
		ConversationID: conversation.ID,
		UserMessageID:  succeeded.turn.UserMessage.ID, AssistantMessageID: succeeded.turn.AssistantMessage.ID,
		Input: answerapplication.Input{Query: succeeded.job.Query, TopK: 7, ResponseLanguage: answerapplication.ResponseLanguageEnglish},
	})
	if err != nil || replayed.ID != succeeded.job.ID || replayed.Status != answerapplication.JobStatusSucceeded {
		t.Fatalf("permanent conversation replay = %+v, %v; want job %d", replayed, err, succeeded.job.ID)
	}
	latest, err := jobService.GetLatestConversationJob(ctx, ownerA, conversation.ID)
	if err != nil || latest.ID != linked[len(linked)-1].job.ID || latest.Status != answerapplication.JobStatusCanceled {
		t.Fatalf("latest retained conversation job = %+v, %v", latest, err)
	}
	unrelatedStandalone, err := answerRepository.CreateAnswerJob(ctx, ownerA, answerapplication.Input{
		Query: "unrelated standalone", TopK: 5, ResponseLanguage: answerapplication.ResponseLanguageEnglish,
	})
	if err != nil {
		t.Fatalf("create unrelated standalone job: %v", err)
	}

	if err := conversationRepository.DeleteConversation(ctx, ownerA, conversation.ID); err != nil {
		t.Fatalf("delete retained conversation: %v", err)
	}
	var linkedCount int
	if err := pool.QueryRow(ctx, `SELECT COUNT(*) FROM answer_jobs WHERE conversation_id = $1`, conversation.ID).Scan(&linkedCount); err != nil || linkedCount != 0 {
		t.Fatalf("linked jobs after conversation delete = %d, %v", linkedCount, err)
	}
	if _, err := answerRepository.GetAnswerJobByID(ctx, ownerA, unrelatedStandalone.ID); err != nil {
		t.Fatalf("conversation delete removed unrelated standalone job: %v", err)
	}
	if _, err := answerRepository.CancelAnswerJob(ctx, ownerA, unrelatedStandalone.ID); err != nil {
		t.Fatalf("cancel unrelated standalone fixture: %v", err)
	}
}

func TestDocumentDeletePreservesConversationAnswerHistory(t *testing.T) {
	if os.Getenv("RUN_DATABASE_TESTS") != "1" {
		t.Skip("set RUN_DATABASE_TESTS=1 to run PostgreSQL integration tests")
	}

	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	pool := openIsolatedDocumentTestPool(t, ctx)
	documents := newOwnedDocumentFixture(t, ctx, pool)
	document, err := documents.Create(ctx, documentdomain.CreateInput{
		OriginalName: "retained-source.pdf",
		StoragePath:  fmt.Sprintf("retention/%d.pdf", time.Now().UnixNano()),
		MIMEType:     "application/pdf",
		SizeBytes:    1024,
		SHA256:       strings.Repeat("a", 64),
	})
	if err != nil {
		t.Fatalf("create retained source document: %v", err)
	}
	conversationRepository := postgresrepository.NewConversationRepository(pool)
	answerRepository := postgresrepository.NewAnswerJobRepository(
		pool,
		answerapplication.JobAdmissionLimits{MaxQueuedJobsPerOwner: 10, MaxQueuedJobsGlobal: 50},
		answerapplication.JobSchedulingPolicy{
			MaxInFlightPerOwner: 1, MaxBorrowedInFlightPerOwner: 1, StarvationThreshold: time.Second,
		},
	)
	jobService, err := answerapplication.NewJobService(answerRepository)
	if err != nil {
		t.Fatalf("create answer job service: %v", err)
	}
	conversation, err := conversationRepository.CreateConversation(ctx, documents.scope, conversationdomain.CreateInput{Title: "document history"})
	if err != nil {
		t.Fatalf("create retained conversation: %v", err)
	}
	clientMessageID := "document-retained-turn"
	turn, err := conversationRepository.StartTurn(ctx, documents.scope, conversationdomain.StartTurnInput{
		ConversationID: conversation.ID, Content: "question", ClientMessageID: &clientMessageID,
	})
	if err != nil {
		t.Fatalf("start retained turn: %v", err)
	}
	linked, err := jobService.QueueConversation(ctx, documents.scope, answerapplication.ConversationQueueInput{
		ConversationID: conversation.ID, UserMessageID: turn.UserMessage.ID, AssistantMessageID: turn.AssistantMessage.ID,
		Input: answerapplication.Input{
			Query: "question", DocumentID: &document.ID, TopK: 5, ResponseLanguage: answerapplication.ResponseLanguageEnglish,
		},
	})
	if err != nil {
		t.Fatalf("queue linked document job: %v", err)
	}
	claimed, err := answerRepository.ClaimNextAnswerJob(ctx)
	if err != nil || claimed.ID != linked.ID {
		t.Fatalf("claim linked document job = %+v, %v", claimed, err)
	}
	standalone, err := answerRepository.CreateAnswerJob(ctx, documents.scope, answerapplication.Input{
		Query: "standalone document question", DocumentID: &document.ID, TopK: 5,
		ResponseLanguage: answerapplication.ResponseLanguageEnglish,
	})
	if err != nil {
		t.Fatalf("create standalone document job: %v", err)
	}

	if err := documents.Delete(ctx, document.ID); err != nil {
		t.Fatalf("delete source document: %v", err)
	}
	if _, err := answerRepository.GetAnswerJobByID(ctx, documents.scope, standalone.ID); !errors.Is(err, answerapplication.ErrAnswerJobNotFound) {
		t.Fatalf("standalone job after document delete error = %v, want not found", err)
	}
	processing, err := answerRepository.GetAnswerJobByID(ctx, documents.scope, linked.ID)
	if err != nil || processing.Status != answerapplication.JobStatusProcessing ||
		processing.DocumentID == nil || *processing.DocumentID != document.ID {
		t.Fatalf("linked processing job after document delete = %+v, %v", processing, err)
	}
	var liveDocumentID *int64
	var requestedDocumentID *int64
	if err := pool.QueryRow(
		ctx,
		`SELECT document_id, requested_document_id FROM answer_jobs WHERE id = $1`,
		linked.ID,
	).Scan(&liveDocumentID, &requestedDocumentID); err != nil || liveDocumentID != nil ||
		requestedDocumentID == nil || *requestedDocumentID != document.ID {
		t.Fatalf("linked live/requested document IDs = %v / %v, %v", liveDocumentID, requestedDocumentID, err)
	}
	if err := answerRepository.MarkAnswerJobSucceeded(ctx, claimed.ID, claimed.LeaseToken, answerapplication.Output{
		Query: "question", Answer: "retained after document delete", ResponseLanguage: answerapplication.ResponseLanguageEnglish,
		Sources:      []answerapplication.Source{{Citation: 1, ChunkID: 7, DocumentID: document.ID, OriginalName: document.OriginalName}},
		PromptTokens: 9, CompletionTokens: 4, TotalTokens: 13,
	}); err != nil {
		t.Fatalf("finish linked job after document delete: %v", err)
	}
	retained, err := answerRepository.GetAnswerJobByID(ctx, documents.scope, linked.ID)
	if err != nil || retained.DocumentID == nil || *retained.DocumentID != document.ID || retained.Result == nil ||
		retained.Result.Answer != "retained after document delete" || retained.Result.TotalTokens != 13 ||
		len(retained.Result.Sources) != 1 || retained.Result.Sources[0].DocumentID != document.ID {
		t.Fatalf("retained linked document job = %+v, %v", retained, err)
	}
	latest, err := jobService.GetLatestConversationJob(ctx, documents.scope, conversation.ID)
	if err != nil || latest.ID != linked.ID || latest.Result == nil {
		t.Fatalf("latest job after document delete = %+v, %v", latest, err)
	}
	replayed, err := jobService.QueueConversation(ctx, documents.scope, answerapplication.ConversationQueueInput{
		ConversationID: conversation.ID, UserMessageID: turn.UserMessage.ID, AssistantMessageID: turn.AssistantMessage.ID,
		Input: answerapplication.Input{
			Query: "question", DocumentID: &document.ID, TopK: 5, ResponseLanguage: answerapplication.ResponseLanguageEnglish,
		},
	})
	if err != nil || replayed.ID != linked.ID || replayed.Status != answerapplication.JobStatusSucceeded {
		t.Fatalf("replay after document delete = %+v, %v", replayed, err)
	}
	retryTurn, err := conversationRepository.StartTurn(ctx, documents.scope, conversationdomain.StartTurnInput{
		ConversationID: conversation.ID, Content: "retry after document delete",
	})
	if err != nil {
		t.Fatalf("start retry turn after document delete: %v", err)
	}
	if _, err := jobService.QueueConversation(ctx, documents.scope, answerapplication.ConversationQueueInput{
		ConversationID: conversation.ID,
		UserMessageID:  retryTurn.UserMessage.ID, AssistantMessageID: retryTurn.AssistantMessage.ID,
		Input: answerapplication.Input{
			Query: "retry after document delete", DocumentID: &document.ID, TopK: 5,
			ResponseLanguage: answerapplication.ResponseLanguageEnglish,
		},
	}); !errors.Is(err, documentdomain.ErrNotFound) {
		t.Fatalf("new-turn retry after document delete error = %v, want document ErrNotFound", err)
	}
	if _, err := conversationRepository.FailAssistantIfUnqueued(ctx, documents.scope, conversationdomain.FailAssistantIfUnqueuedInput{
		ConversationID: conversation.ID, MessageID: retryTurn.AssistantMessage.ID, Content: "document not found",
	}); err != nil {
		t.Fatalf("close failed retry turn: %v", err)
	}
	otherOwner := answerJobTestOwnerScope(t, ctx, pool, "document-history-other")
	if _, err := answerRepository.GetAnswerJobByID(ctx, otherOwner, linked.ID); !errors.Is(err, answerapplication.ErrAnswerJobNotFound) {
		t.Fatalf("cross-owner retained document job error = %v", err)
	}
	if err := conversationRepository.DeleteConversation(ctx, documents.scope, conversation.ID); err != nil {
		t.Fatalf("delete retained document conversation: %v", err)
	}
	if _, err := answerRepository.GetAnswerJobByID(ctx, documents.scope, linked.ID); !errors.Is(err, answerapplication.ErrAnswerJobNotFound) {
		t.Fatalf("linked job after conversation delete error = %v, want not found", err)
	}
}

func TestAnswerJobRetentionSkipsLockedStandaloneRows(t *testing.T) {
	if os.Getenv("RUN_DATABASE_TESTS") != "1" {
		t.Skip("set RUN_DATABASE_TESTS=1 to run PostgreSQL integration tests")
	}

	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	pool := openIsolatedDocumentTestPool(t, ctx)
	owner := answerJobTestOwnerScope(t, ctx, pool, "retention-lock")
	repository := postgresrepository.NewAnswerJobRepository(
		pool,
		answerapplication.JobAdmissionLimits{MaxQueuedJobsPerOwner: 10, MaxQueuedJobsGlobal: 50},
		answerapplication.JobSchedulingPolicy{
			MaxInFlightPerOwner: 1, MaxBorrowedInFlightPerOwner: 1, StarvationThreshold: time.Second,
		},
	)
	cutoff := time.Now().UTC().Add(-time.Hour)
	jobs := make([]answerapplication.Job, 0, 3)
	for index := range 3 {
		job, err := repository.CreateAnswerJob(ctx, owner, answerapplication.Input{
			Query: fmt.Sprintf("locked retention %d", index), TopK: 5,
			ResponseLanguage: answerapplication.ResponseLanguageEnglish,
		})
		if err != nil {
			t.Fatalf("create locked retention fixture %d: %v", index, err)
		}
		job, err = repository.CancelAnswerJob(ctx, owner, job.ID)
		if err != nil {
			t.Fatalf("cancel locked retention fixture %d: %v", index, err)
		}
		if _, err := pool.Exec(ctx, `UPDATE answer_jobs SET completed_at = $2 WHERE id = $1`, job.ID, cutoff.Add(-time.Hour)); err != nil {
			t.Fatalf("backdate locked retention fixture %d: %v", index, err)
		}
		jobs = append(jobs, job)
	}

	lockTx, err := pool.Begin(ctx)
	if err != nil {
		t.Fatalf("begin retention row lock: %v", err)
	}
	var lockedJobID int64
	if err := lockTx.QueryRow(ctx, `SELECT id FROM answer_jobs WHERE id = $1 FOR UPDATE`, jobs[0].ID).Scan(&lockedJobID); err != nil {
		_ = lockTx.Rollback(ctx)
		t.Fatalf("lock retention row: %v", err)
	}
	deleted, err := repository.DeleteExpiredStandaloneAnswerJobs(ctx, cutoff, 3)
	if err != nil || deleted != 2 {
		_ = lockTx.Rollback(ctx)
		t.Fatalf("cleanup with locked row = %d, %v, want 2", deleted, err)
	}
	if err := lockTx.Commit(ctx); err != nil {
		t.Fatalf("commit retention row lock: %v", err)
	}
	deleted, err = repository.DeleteExpiredStandaloneAnswerJobs(ctx, cutoff, 3)
	if err != nil || deleted != 1 {
		t.Fatalf("cleanup after unlocking row = %d, %v, want 1", deleted, err)
	}
	deleted, err = repository.DeleteExpiredStandaloneAnswerJobs(ctx, cutoff, 3)
	if err != nil || deleted != 0 {
		t.Fatalf("final cleanup after unlocking row = %d, %v, want 0", deleted, err)
	}
}

func TestStandaloneAnswerFinalizeSerializesWithDocumentDelete(t *testing.T) {
	if os.Getenv("RUN_DATABASE_TESTS") != "1" {
		t.Skip("set RUN_DATABASE_TESTS=1 to run PostgreSQL integration tests")
	}

	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	pool := openIsolatedDocumentTestPool(t, ctx)
	documents := newOwnedDocumentFixture(t, ctx, pool)
	repository := postgresrepository.NewAnswerJobRepository(
		pool,
		answerapplication.JobAdmissionLimits{MaxQueuedJobsPerOwner: 10, MaxQueuedJobsGlobal: 50},
		answerapplication.JobSchedulingPolicy{
			MaxInFlightPerOwner: 1, MaxBorrowedInFlightPerOwner: 1, StarvationThreshold: time.Second,
		},
	)
	createDocument := func(name string, marker string) documentdomain.Document {
		t.Helper()
		document, err := documents.Create(ctx, documentdomain.CreateInput{
			OriginalName: name,
			StoragePath:  fmt.Sprintf("source-finalize/%d-%s", time.Now().UnixNano(), name),
			MIMEType:     "application/pdf",
			SizeBytes:    1024,
			SHA256:       strings.Repeat(marker, 64),
		})
		if err != nil {
			t.Fatalf("create %s: %v", name, err)
		}
		return document
	}
	createProcessingAllDocumentJob := func(query string) answerapplication.Job {
		t.Helper()
		job, err := repository.CreateAnswerJob(ctx, documents.scope, answerapplication.Input{
			Query: query, TopK: 5, ResponseLanguage: answerapplication.ResponseLanguageEnglish,
		})
		if err != nil {
			t.Fatalf("create %s job: %v", query, err)
		}
		claimed, err := repository.ClaimNextAnswerJob(ctx)
		if err != nil || claimed.ID != job.ID {
			t.Fatalf("claim %s job = %+v, %v", query, claimed, err)
		}
		return claimed
	}
	outputFor := func(document documentdomain.Document) answerapplication.Output {
		return answerapplication.Output{
			Query: "all documents", Answer: "answer", ResponseLanguage: answerapplication.ResponseLanguageEnglish,
			Sources:      []answerapplication.Source{{Citation: 1, ChunkID: 7, DocumentID: document.ID, OriginalName: document.OriginalName}},
			PromptTokens: 8, CompletionTokens: 3, TotalTokens: 11,
		}
	}

	deletedFirstDocument := createDocument("deleted-first.pdf", "c")
	deletedFirstJob := createProcessingAllDocumentJob("delete wins")
	if err := documents.Delete(ctx, deletedFirstDocument.ID); err != nil {
		t.Fatalf("delete source before finalization: %v", err)
	}
	if err := repository.MarkAnswerJobSucceeded(
		ctx, deletedFirstJob.ID, deletedFirstJob.LeaseToken, outputFor(deletedFirstDocument),
	); !errors.Is(err, answerapplication.ErrAnswerJobSourceDocumentsUnavailable) {
		t.Fatalf("finalize after source delete error = %v, want typed unavailable", err)
	}
	if err := repository.MarkAnswerJobFailed(
		ctx,
		deletedFirstJob.ID,
		deletedFirstJob.LeaseToken,
		answerapplication.JobErrorCodeExecutionFailed,
		"source document unavailable",
	); err != nil {
		t.Fatalf("fail job after source validation error: %v", err)
	}
	failed, err := repository.GetAnswerJobByID(ctx, documents.scope, deletedFirstJob.ID)
	if err != nil || failed.Status != answerapplication.JobStatusFailed || failed.Result != nil {
		t.Fatalf("failed source-validation job = %+v, %v", failed, err)
	}

	finalizedFirstDocument := createDocument("finalized-first.pdf", "d")
	finalizedFirstJob := createProcessingAllDocumentJob("finalize wins")
	if err := repository.MarkAnswerJobSucceeded(
		ctx, finalizedFirstJob.ID, finalizedFirstJob.LeaseToken, outputFor(finalizedFirstDocument),
	); err != nil {
		t.Fatalf("finalize before document delete: %v", err)
	}
	if err := documents.Delete(ctx, finalizedFirstDocument.ID); err != nil {
		t.Fatalf("delete document after finalization: %v", err)
	}
	if _, err := repository.GetAnswerJobByID(ctx, documents.scope, finalizedFirstJob.ID); !errors.Is(err, answerapplication.ErrAnswerJobNotFound) {
		t.Fatalf("succeeded all-document job after source delete error = %v, want not found", err)
	}
}

func TestRequestedDocumentSnapshotMigrationDownFailsClosed(t *testing.T) {
	if os.Getenv("RUN_DATABASE_TESTS") != "1" {
		t.Skip("set RUN_DATABASE_TESTS=1 to run PostgreSQL integration tests")
	}

	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	downSQL, err := os.ReadFile(filepath.Join("..", "..", "..", "migrations", "000035_snapshot_requested_answer_document.down.sql"))
	if err != nil {
		t.Fatalf("read migration 35 down: %v", err)
	}
	upSQL, err := migrations.Files.ReadFile("000035_snapshot_requested_answer_document.up.sql")
	if err != nil {
		t.Fatalf("read migration 35 up: %v", err)
	}
	execScript := func(pool *pgxpool.Pool, script []byte) error {
		connection, err := pool.Acquire(ctx)
		if err != nil {
			return err
		}
		defer connection.Release()
		_, err = connection.Conn().PgConn().Exec(ctx, string(script)).ReadAll()
		if err != nil {
			_, _ = connection.Exec(ctx, "ROLLBACK")
		}
		return err
	}
	columnExists := func(pool *pgxpool.Pool) bool {
		var exists bool
		if err := pool.QueryRow(
			ctx,
			`SELECT EXISTS (
				SELECT 1
				FROM information_schema.columns
				WHERE table_schema = CURRENT_SCHEMA()
				  AND table_name = 'answer_jobs'
				  AND column_name = 'requested_document_id'
			)`,
		).Scan(&exists); err != nil {
			t.Fatalf("check requested_document_id: %v", err)
		}
		return exists
	}

	failClosedPool := openIsolatedDocumentTestPool(t, ctx)
	failClosedOwner := answerJobTestOwnerScope(t, ctx, failClosedPool, "migration-35-fail")
	if _, err := failClosedPool.Exec(
		ctx,
		`INSERT INTO answer_jobs (
			owner_user_id, document_id, requested_document_id,
			query, top_k, requested_response_language
		 ) VALUES ($1, NULL, 999999, 'snapshot required', 5, 'en')`,
		failClosedOwner.OwnerUserID(),
	); err != nil {
		t.Fatalf("insert fail-closed fixture: %v", err)
	}
	if err := execScript(failClosedPool, downSQL); err == nil {
		t.Fatal("migration 35 down succeeded despite cleared live document scope")
	}
	if !columnExists(failClosedPool) {
		t.Fatal("requested_document_id disappeared after rejected down migration")
	}

	safePool := openIsolatedDocumentTestPool(t, ctx)
	safeDocuments := newOwnedDocumentFixture(t, ctx, safePool)
	safeDocument, err := safeDocuments.Create(ctx, documentdomain.CreateInput{
		OriginalName: "migration-35-safe.pdf",
		StoragePath:  fmt.Sprintf("migration-35/%d.pdf", time.Now().UnixNano()),
		MIMEType:     "application/pdf",
		SizeBytes:    1024,
		SHA256:       strings.Repeat("e", 64),
	})
	if err != nil {
		t.Fatalf("create safe migration document: %v", err)
	}
	safeRepository := postgresrepository.NewAnswerJobRepository(
		safePool,
		answerapplication.JobAdmissionLimits{MaxQueuedJobsPerOwner: 5, MaxQueuedJobsGlobal: 20},
		answerapplication.JobSchedulingPolicy{
			MaxInFlightPerOwner: 1, MaxBorrowedInFlightPerOwner: 1, StarvationThreshold: time.Second,
		},
	)
	safeJob, err := safeRepository.CreateAnswerJob(ctx, safeDocuments.scope, answerapplication.Input{
		Query: "safe migration", DocumentID: &safeDocument.ID, TopK: 5,
		ResponseLanguage: answerapplication.ResponseLanguageEnglish,
	})
	if err != nil {
		t.Fatalf("create safe migration job: %v", err)
	}
	if err := execScript(safePool, downSQL); err != nil {
		t.Fatalf("safe migration 35 down: %v", err)
	}
	if columnExists(safePool) {
		t.Fatal("requested_document_id still exists after safe down migration")
	}
	if err := execScript(safePool, upSQL); err != nil {
		t.Fatalf("migration 35 up after safe down: %v", err)
	}
	if !columnExists(safePool) {
		t.Fatal("requested_document_id missing after migration 35 up")
	}
	var backfilledDocumentID *int64
	if err := safePool.QueryRow(
		ctx,
		`SELECT requested_document_id FROM answer_jobs WHERE id = $1`,
		safeJob.ID,
	).Scan(&backfilledDocumentID); err != nil || backfilledDocumentID == nil || *backfilledDocumentID != safeDocument.ID {
		t.Fatalf("backfilled requested document ID = %v, %v", backfilledDocumentID, err)
	}
}

func TestMigration37CleansUnsafeLegacyStandaloneSources(t *testing.T) {
	if os.Getenv("RUN_DATABASE_TESTS") != "1" {
		t.Skip("set RUN_DATABASE_TESTS=1 to run PostgreSQL integration tests")
	}

	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	pool := openIsolatedDocumentTestPool(t, ctx)
	upSQL, err := migrations.Files.ReadFile("000037_backfill_standalone_answer_source_privacy.up.sql")
	if err != nil {
		t.Fatalf("read migration 37 up: %v", err)
	}
	downSQL, err := os.ReadFile(filepath.Join("..", "..", "..", "migrations", "000037_backfill_standalone_answer_source_privacy.down.sql"))
	if err != nil {
		t.Fatalf("read migration 37 down: %v", err)
	}
	execScript := func(script []byte) error {
		connection, err := pool.Acquire(ctx)
		if err != nil {
			return err
		}
		defer connection.Release()
		_, err = connection.Conn().PgConn().Exec(ctx, string(script)).ReadAll()
		if err != nil {
			_, _ = connection.Exec(ctx, "ROLLBACK")
		}
		return err
	}
	ownerA := newOwnedDocumentFixture(t, ctx, pool)
	ownerB := newOwnedDocumentFixture(t, ctx, pool)
	documentA, err := ownerA.Create(ctx, documentdomain.CreateInput{
		OriginalName: "valid-source.pdf",
		StoragePath:  fmt.Sprintf("migration-36/%d-a.pdf", time.Now().UnixNano()),
		MIMEType:     "application/pdf",
		SizeBytes:    1024,
		SHA256:       strings.Repeat("f", 64),
	})
	if err != nil {
		t.Fatalf("create owner A migration document: %v", err)
	}
	documentB, err := ownerB.Create(ctx, documentdomain.CreateInput{
		OriginalName: "wrong-owner.pdf",
		StoragePath:  fmt.Sprintf("migration-36/%d-b.pdf", time.Now().UnixNano()),
		MIMEType:     "application/pdf",
		SizeBytes:    1024,
		SHA256:       strings.Repeat("1", 64),
	})
	if err != nil {
		t.Fatalf("create owner B migration document: %v", err)
	}
	insertSucceededStandalone := func(query string, sources string) int64 {
		t.Helper()
		var jobID int64
		if err := pool.QueryRow(
			ctx,
			`INSERT INTO answer_jobs (
				owner_user_id, query, top_k, requested_response_language,
				status, answer_text, resolved_response_language, sources,
				prompt_tokens, completion_tokens, total_tokens, completed_at
			 ) VALUES ($1, $2, 5, 'en', 'succeeded', 'legacy answer', 'en', $3::jsonb, 1, 1, 2, CURRENT_TIMESTAMP)
			 RETURNING id`,
			ownerA.scope.OwnerUserID(),
			query,
			sources,
		).Scan(&jobID); err != nil {
			t.Fatalf("insert %s legacy standalone: %v", query, err)
		}
		return jobID
	}
	validSnake := insertSucceededStandalone("valid snake", fmt.Sprintf(`[{"document_id":%d}]`, documentA.ID))
	validCamel := insertSucceededStandalone("valid camel", fmt.Sprintf(`[{"DocumentID":%d}]`, documentA.ID))
	missing := insertSucceededStandalone("missing", `[{"document_id":999999999}]`)
	wrongOwner := insertSucceededStandalone("wrong owner", fmt.Sprintf(`[{"DocumentID":%d}]`, documentB.ID))
	invalid := insertSucceededStandalone("invalid", `[{"document_id":"not-a-number"}]`)

	conversationRepository := postgresrepository.NewConversationRepository(pool)
	answerRepository := postgresrepository.NewAnswerJobRepository(
		pool,
		answerapplication.JobAdmissionLimits{MaxQueuedJobsPerOwner: 5, MaxQueuedJobsGlobal: 20},
		answerapplication.JobSchedulingPolicy{
			MaxInFlightPerOwner: 1, MaxBorrowedInFlightPerOwner: 1, StarvationThreshold: time.Second,
		},
	)
	conversation, err := conversationRepository.CreateConversation(ctx, ownerA.scope, conversationdomain.CreateInput{Title: "linked legacy source"})
	if err != nil {
		t.Fatalf("create linked legacy conversation: %v", err)
	}
	turn, err := conversationRepository.StartTurn(ctx, ownerA.scope, conversationdomain.StartTurnInput{
		ConversationID: conversation.ID, Content: "linked legacy",
	})
	if err != nil {
		t.Fatalf("start linked legacy turn: %v", err)
	}
	conversationID := conversation.ID
	userMessageID := turn.UserMessage.ID
	assistantMessageID := turn.AssistantMessage.ID
	linked, err := answerRepository.CreateAnswerJob(ctx, ownerA.scope, answerapplication.Input{
		Query: "linked legacy", TopK: 5, ResponseLanguage: answerapplication.ResponseLanguageEnglish,
		ConversationID: &conversationID, UserMessageID: &userMessageID, AssistantMessageID: &assistantMessageID,
	})
	if err != nil {
		t.Fatalf("create linked legacy job: %v", err)
	}
	claimed, err := answerRepository.ClaimNextAnswerJob(ctx)
	if err != nil || claimed.ID != linked.ID {
		t.Fatalf("claim linked legacy job = %+v, %v", claimed, err)
	}
	if err := answerRepository.MarkAnswerJobSucceeded(ctx, claimed.ID, claimed.LeaseToken, answerapplication.Output{
		Query: "linked legacy", Answer: "linked answer", ResponseLanguage: answerapplication.ResponseLanguageEnglish,
		Sources:      []answerapplication.Source{{Citation: 1, ChunkID: 7, DocumentID: 999999999, OriginalName: "deleted.pdf"}},
		PromptTokens: 1, CompletionTokens: 1, TotalTokens: 2,
	}); err != nil {
		t.Fatalf("complete linked legacy job: %v", err)
	}
	if err := execScript(upSQL); err != nil {
		t.Fatalf("migration 37 legacy cleanup up: %v", err)
	}

	for _, jobID := range []int64{validSnake, validCamel, linked.ID} {
		var exists bool
		if err := pool.QueryRow(ctx, `SELECT EXISTS (SELECT 1 FROM answer_jobs WHERE id = $1)`, jobID).Scan(&exists); err != nil || !exists {
			t.Fatalf("retained migration 37 job %d exists/error = %t / %v", jobID, exists, err)
		}
	}
	for _, jobID := range []int64{missing, wrongOwner, invalid} {
		var exists bool
		if err := pool.QueryRow(ctx, `SELECT EXISTS (SELECT 1 FROM answer_jobs WHERE id = $1)`, jobID).Scan(&exists); err != nil || exists {
			t.Fatalf("unsafe migration 37 job %d exists/error = %t / %v", jobID, exists, err)
		}
	}
	if err := execScript(downSQL); err != nil {
		t.Fatalf("migration 37 irreversible no-op down: %v", err)
	}
	for _, jobID := range []int64{missing, wrongOwner, invalid} {
		var exists bool
		if err := pool.QueryRow(ctx, `SELECT EXISTS (SELECT 1 FROM answer_jobs WHERE id = $1)`, jobID).Scan(&exists); err != nil || exists {
			t.Fatalf("migration 37 down restored unsafe job %d exists/error = %t / %v", jobID, exists, err)
		}
	}
}

func TestRequestedDocumentSnapshotDownLockBlocksConcurrentDocumentDelete(t *testing.T) {
	if os.Getenv("RUN_DATABASE_TESTS") != "1" {
		t.Skip("set RUN_DATABASE_TESTS=1 to run PostgreSQL integration tests")
	}

	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	pool := openIsolatedDocumentTestPool(t, ctx)
	documents := newOwnedDocumentFixture(t, ctx, pool)
	document, err := documents.Create(ctx, documentdomain.CreateInput{
		OriginalName: "down-lock.pdf",
		StoragePath:  fmt.Sprintf("down-lock/%d.pdf", time.Now().UnixNano()),
		MIMEType:     "application/pdf",
		SizeBytes:    1024,
		SHA256:       strings.Repeat("2", 64),
	})
	if err != nil {
		t.Fatalf("create down-lock document: %v", err)
	}
	repository := postgresrepository.NewAnswerJobRepository(
		pool,
		answerapplication.JobAdmissionLimits{MaxQueuedJobsPerOwner: 5, MaxQueuedJobsGlobal: 20},
		answerapplication.JobSchedulingPolicy{
			MaxInFlightPerOwner: 1, MaxBorrowedInFlightPerOwner: 1, StarvationThreshold: time.Second,
		},
	)
	if _, err := repository.CreateAnswerJob(ctx, documents.scope, answerapplication.Input{
		Query: "down lock", DocumentID: &document.ID, TopK: 5,
		ResponseLanguage: answerapplication.ResponseLanguageEnglish,
	}); err != nil {
		t.Fatalf("create down-lock answer job: %v", err)
	}

	downTx, err := pool.Begin(ctx)
	if err != nil {
		t.Fatalf("begin simulated migration 35 down: %v", err)
	}
	if _, err := downTx.Exec(ctx, `LOCK TABLE answer_jobs IN ACCESS EXCLUSIVE MODE`); err != nil {
		_ = downTx.Rollback(ctx)
		t.Fatalf("lock answer_jobs for down: %v", err)
	}
	var dangerousCount int
	if err := downTx.QueryRow(
		ctx,
		`SELECT COUNT(*)
		 FROM answer_jobs
		 WHERE requested_document_id IS DISTINCT FROM document_id`,
	).Scan(&dangerousCount); err != nil || dangerousCount != 0 {
		_ = downTx.Rollback(ctx)
		t.Fatalf("down precheck count/error = %d / %v", dangerousCount, err)
	}
	deleteStarted := make(chan struct{})
	deleteDone := make(chan error, 1)
	go func() {
		close(deleteStarted)
		deleteDone <- documents.Delete(ctx, document.ID)
	}()
	<-deleteStarted
	select {
	case err := <-deleteDone:
		_ = downTx.Rollback(ctx)
		t.Fatalf("document delete crossed ACCESS EXCLUSIVE down lock: %v", err)
	case <-time.After(150 * time.Millisecond):
		// 预期：DELETE 在 trigger/FK 访问 answer_jobs 时阻塞。
	}
	if _, err := downTx.Exec(ctx, `ALTER TABLE answer_jobs DROP COLUMN requested_document_id`); err != nil {
		_ = downTx.Rollback(ctx)
		t.Fatalf("drop requested snapshot under lock: %v", err)
	}
	if err := downTx.Commit(ctx); err != nil {
		t.Fatalf("commit simulated migration 35 down: %v", err)
	}
	if err := <-deleteDone; err != nil {
		t.Fatalf("document delete after down commit: %v", err)
	}
}

func answerJobTestOwnerScope(
	t *testing.T,
	ctx context.Context,
	pool *pgxpool.Pool,
	name string,
) accessdomain.OwnerScope {
	t.Helper()

	ownerID := insertScopedRepositoryUser(
		t,
		ctx,
		pool,
		fmt.Sprintf("%s-%d@example.com", name, time.Now().UnixNano()),
	)
	scope, err := accessdomain.NewOwnerScope(ownerID)
	if err != nil {
		t.Fatalf("create answer job owner scope: %v", err)
	}
	return scope
}
