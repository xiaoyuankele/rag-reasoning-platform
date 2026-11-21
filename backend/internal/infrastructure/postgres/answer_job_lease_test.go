package postgres_test

import (
	"context"
	"errors"
	"os"
	"testing"
	"time"

	answerapplication "rag-reasoning-platform/backend/internal/application/answer"
	postgresrepository "rag-reasoning-platform/backend/internal/infrastructure/postgres"
)

// TestAnswerJobLeaseFencing 使用真实 PostgreSQL 验证租约续期、过期恢复和
// fencing。它不调用 Embedding 或 Generation Provider，不产生远程费用。
func TestAnswerJobLeaseFencing(t *testing.T) {
	if os.Getenv("RUN_DATABASE_TESTS") != "1" {
		t.Skip("set RUN_DATABASE_TESTS=1 to run PostgreSQL integration tests")
	}

	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	pool := openIsolatedDocumentTestPool(t, ctx)
	repository := postgresrepository.NewAnswerJobRepositoryWithPolicies(
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
		answerapplication.JobLeasePolicy{
			WorkerID:      "answer-lease-test-worker",
			LeaseDuration: time.Minute,
		},
	)
	scope := answerJobTestOwnerScope(t, ctx, pool, "answer-lease-owner")
	created, err := repository.CreateAnswerJob(
		ctx,
		scope,
		answerapplication.Input{
			Query:            "lease fencing question",
			TopK:             5,
			ResponseLanguage: answerapplication.ResponseLanguageEnglish,
		},
	)
	if err != nil {
		t.Fatalf("CreateAnswerJob() error = %v", err)
	}

	firstClaim, err := repository.ClaimNextAnswerJob(ctx)
	if err != nil {
		t.Fatalf("first ClaimNextAnswerJob() error = %v", err)
	}
	if firstClaim.ID != created.ID || firstClaim.LeaseToken == "" {
		t.Fatalf("first claim = %+v, want job %d with lease token", firstClaim, created.ID)
	}

	recovered, err := repository.RequeueExpiredAnswerJobs(
		ctx,
		answerapplication.JobErrorCodeWorkerInterrupted,
		"expired",
	)
	if err != nil || recovered != 0 {
		t.Fatalf("recover active lease = %d, %v, want 0 nil", recovered, err)
	}

	var previousExpiry time.Time
	if err := pool.QueryRow(
		ctx,
		`SELECT lease_expires_at FROM answer_jobs WHERE id = $1`,
		created.ID,
	).Scan(&previousExpiry); err != nil {
		t.Fatalf("read first lease expiry: %v", err)
	}
	if err := repository.RenewAnswerJobLease(
		ctx,
		firstClaim.ID,
		firstClaim.LeaseToken,
	); err != nil {
		t.Fatalf("RenewAnswerJobLease() error = %v", err)
	}
	var renewedExpiry time.Time
	if err := pool.QueryRow(
		ctx,
		`SELECT lease_expires_at FROM answer_jobs WHERE id = $1`,
		created.ID,
	).Scan(&renewedExpiry); err != nil {
		t.Fatalf("read renewed lease expiry: %v", err)
	}
	if renewedExpiry.Before(previousExpiry) {
		t.Fatalf("renewed expiry %v is before previous %v", renewedExpiry, previousExpiry)
	}

	if _, err := pool.Exec(
		ctx,
		`UPDATE answer_jobs
		 SET lease_expires_at = CURRENT_TIMESTAMP - INTERVAL '1 second'
		 WHERE id = $1`,
		created.ID,
	); err != nil {
		t.Fatalf("force answer lease expiry: %v", err)
	}
	recovered, err = repository.RequeueExpiredAnswerJobs(
		ctx,
		answerapplication.JobErrorCodeWorkerInterrupted,
		"expired",
	)
	if err != nil || recovered != 1 {
		t.Fatalf("recover expired lease = %d, %v, want 1 nil", recovered, err)
	}

	secondClaim, err := repository.ClaimNextAnswerJob(ctx)
	if err != nil {
		t.Fatalf("second ClaimNextAnswerJob() error = %v", err)
	}
	if secondClaim.LeaseToken == "" || secondClaim.LeaseToken == firstClaim.LeaseToken {
		t.Fatalf(
			"replacement token = %q, want non-empty and different from %q",
			secondClaim.LeaseToken,
			firstClaim.LeaseToken,
		)
	}

	output := answerapplication.Output{
		Query:            secondClaim.Query,
		Answer:           "lease-safe answer",
		ResponseLanguage: answerapplication.ResponseLanguageEnglish,
		Sources:          []answerapplication.Source{},
		PromptTokens:     8,
		CompletionTokens: 3,
		TotalTokens:      11,
	}
	if err := repository.MarkAnswerJobSucceeded(
		ctx,
		created.ID,
		firstClaim.LeaseToken,
		output,
	); !errors.Is(err, answerapplication.ErrAnswerJobLeaseLost) {
		t.Fatalf("stale success error = %v, want lease lost", err)
	}
	if err := repository.RequeueAnswerJob(
		ctx,
		created.ID,
		firstClaim.LeaseToken,
		time.Now().Add(time.Second),
		answerapplication.JobErrorCodeTemporarilyUnavailable,
		"retry",
	); !errors.Is(err, answerapplication.ErrAnswerJobLeaseLost) {
		t.Fatalf("stale requeue error = %v, want lease lost", err)
	}
	if err := repository.MarkAnswerJobFailed(
		ctx,
		created.ID,
		firstClaim.LeaseToken,
		answerapplication.JobErrorCodeExecutionFailed,
		"failed",
	); !errors.Is(err, answerapplication.ErrAnswerJobLeaseLost) {
		t.Fatalf("stale failure error = %v, want lease lost", err)
	}

	if err := repository.MarkAnswerJobSucceeded(
		ctx,
		created.ID,
		secondClaim.LeaseToken,
		output,
	); err != nil {
		t.Fatalf("current worker success error = %v", err)
	}
	stored, err := repository.GetAnswerJobByID(ctx, scope, created.ID)
	if err != nil {
		t.Fatalf("GetAnswerJobByID() error = %v", err)
	}
	if stored.Status != answerapplication.JobStatusSucceeded ||
		stored.Result == nil ||
		stored.Result.Answer != "lease-safe answer" ||
		stored.LeaseToken != "" {
		t.Fatalf("stored answer job = %+v, want succeeded without public lease token", stored)
	}
}
