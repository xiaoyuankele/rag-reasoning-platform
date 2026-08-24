package postgres_test

import (
	"context"
	"errors"
	"fmt"
	"os"
	"sync"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"

	answerapplication "rag-reasoning-platform/backend/internal/application/answer"
	accessdomain "rag-reasoning-platform/backend/internal/domain/access"
	postgresrepository "rag-reasoning-platform/backend/internal/infrastructure/postgres"
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
			if err := repository.MarkAnswerJobSucceeded(ctx, job.ID, output); err != nil {
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
	})
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
