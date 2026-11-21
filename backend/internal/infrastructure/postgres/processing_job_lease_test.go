package postgres_test

import (
	"context"
	"errors"
	"fmt"
	"os"
	"strings"
	"testing"
	"time"

	documentdomain "rag-reasoning-platform/backend/internal/domain/document"
	postgresrepository "rag-reasoning-platform/backend/internal/infrastructure/postgres"
)

// TestProcessingJobLeaseRecoveryAndFencing 使用真实 PostgreSQL 验证三条核心规则：
// 1. 心跳后的有效租约不会被恢复；2. 到期任务可以被重新领取；
// 3. 旧 lease_token 不能写 chunks 或任务终态。
func TestProcessingJobLeaseRecoveryAndFencing(t *testing.T) {
	if os.Getenv("RUN_DATABASE_TESTS") != "1" {
		t.Skip("set RUN_DATABASE_TESTS=1 to run PostgreSQL integration tests")
	}

	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	pool := openIsolatedDocumentTestPool(t, ctx)
	documents := newOwnedDocumentFixture(t, ctx, pool)
	leaseDuration := 500 * time.Millisecond
	jobs := postgresrepository.NewProcessingJobRepositoryWithPolicies(
		pool,
		documentdomain.ProcessingJobSchedulingPolicy{
			MaxInFlightPerOwner:         1,
			MaxBorrowedInFlightPerOwner: 2,
			StarvationThreshold:         time.Minute,
		},
		documentdomain.ProcessingJobLeasePolicy{
			WorkerID:      "lease-test-worker",
			LeaseDuration: leaseDuration,
		},
	)
	chunks := postgresrepository.NewChunkRepository(pool)

	createdDocument, err := documents.Create(
		ctx,
		documentdomain.CreateInput{
			OriginalName: "lease-fencing.md",
			StoragePath: fmt.Sprintf(
				"integration-tests/lease-fencing-%d.md",
				time.Now().UnixNano(),
			),
			MIMEType:  "text/markdown",
			SizeBytes: 128,
			SHA256:    strings.Repeat("d", 64),
		},
	)
	if err != nil {
		t.Fatalf("create lease test document: %v", err)
	}
	t.Cleanup(func() {
		cleanupCtx, cleanupCancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cleanupCancel()
		if _, cleanupErr := pool.Exec(
			cleanupCtx,
			"DELETE FROM documents WHERE id = $1",
			createdDocument.ID,
		); cleanupErr != nil {
			t.Errorf("clean up lease test document: %v", cleanupErr)
		}
	})

	if _, err := jobs.CreateProcessingJob(ctx, createdDocument.ID); err != nil {
		t.Fatalf("create lease test job: %v", err)
	}
	firstClaim, err := jobs.ClaimNextProcessingJob(ctx)
	if err != nil {
		t.Fatalf("claim lease test job: %v", err)
	}
	if firstClaim.LeaseToken == "" {
		t.Fatal("first claim lease token must not be empty")
	}

	time.Sleep(150 * time.Millisecond)
	if err := jobs.RenewProcessingJobLease(
		ctx,
		firstClaim.ID,
		firstClaim.LeaseToken,
	); err != nil {
		t.Fatalf("renew processing lease: %v", err)
	}
	time.Sleep(150 * time.Millisecond)
	recovered, err := jobs.RequeueExpiredProcessingJobs(ctx, "lease expired")
	if err != nil {
		t.Fatalf("recover while renewed lease is valid: %v", err)
	}
	if recovered != 0 {
		t.Fatalf("recovered valid leases = %d, want 0", recovered)
	}

	time.Sleep(400 * time.Millisecond)
	recovered, err = jobs.RequeueExpiredProcessingJobs(ctx, "lease expired")
	if err != nil {
		t.Fatalf("recover expired lease: %v", err)
	}
	if recovered != 1 {
		t.Fatalf("recovered expired leases = %d, want 1", recovered)
	}

	secondClaim, err := jobs.ClaimNextProcessingJob(ctx)
	if err != nil {
		t.Fatalf("reclaim expired job: %v", err)
	}
	if secondClaim.ID != firstClaim.ID {
		t.Fatalf("reclaimed job ID = %d, want %d", secondClaim.ID, firstClaim.ID)
	}
	if secondClaim.LeaseToken == firstClaim.LeaseToken {
		t.Fatal("reclaimed job must receive a new fencing token")
	}

	testChunks := []documentdomain.ChunkInput{{Index: 0, Content: "current result"}}
	if err := chunks.ReplaceForProcessingJob(
		ctx,
		firstClaim.ID,
		firstClaim.LeaseToken,
		createdDocument.ID,
		testChunks,
	); !errors.Is(err, documentdomain.ErrProcessingJobLeaseLost) {
		t.Fatalf("stale chunk write error = %v, want ErrProcessingJobLeaseLost", err)
	}
	if err := jobs.MarkProcessingJobSucceeded(
		ctx,
		firstClaim.ID,
		firstClaim.LeaseToken,
		documentdomain.ProcessingCompletion{},
	); !errors.Is(err, documentdomain.ErrProcessingJobLeaseLost) {
		t.Fatalf("stale finalization error = %v, want ErrProcessingJobLeaseLost", err)
	}

	if err := chunks.ReplaceForProcessingJob(
		ctx,
		secondClaim.ID,
		secondClaim.LeaseToken,
		createdDocument.ID,
		testChunks,
	); err != nil {
		t.Fatalf("current lease chunk write: %v", err)
	}
	if err := jobs.MarkProcessingJobSucceeded(
		ctx,
		secondClaim.ID,
		secondClaim.LeaseToken,
		documentdomain.ProcessingCompletion{
			Metrics: documentdomain.ProcessingExecutionMetrics{ChunkCount: 1},
		},
	); err != nil {
		t.Fatalf("current lease finalization: %v", err)
	}
}
