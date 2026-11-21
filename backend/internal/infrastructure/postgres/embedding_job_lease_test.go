package postgres_test

import (
	"context"
	"errors"
	"os"
	"testing"
	"time"

	"rag-reasoning-platform/backend/internal/config"
	embeddingdomain "rag-reasoning-platform/backend/internal/domain/embedding"
	"rag-reasoning-platform/backend/internal/infrastructure/database"
	postgresrepository "rag-reasoning-platform/backend/internal/infrastructure/postgres"
	"rag-reasoning-platform/backend/migrations"
)

// TestEmbeddingJobLeaseRejectsStaleWorker 使用真实 PostgreSQL 验证租约续期、
// 过期恢复、重新领取和 fencing。该测试保护多进程部署最关键的正确性边界。
func TestEmbeddingJobLeaseRejectsStaleWorker(t *testing.T) {
	if os.Getenv("RUN_DATABASE_TESTS") != "1" {
		t.Skip("set RUN_DATABASE_TESTS=1 to run PostgreSQL integration tests")
	}

	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	databaseConfig, err := config.LoadDatabase()
	if err != nil {
		t.Fatalf("load database configuration: %v", err)
	}
	pool, err := database.Open(ctx, databaseConfig.ConnectionString())
	if err != nil {
		t.Fatalf("open database: %v", err)
	}
	t.Cleanup(pool.Close)
	if err := database.Migrate(ctx, pool, migrations.Files); err != nil {
		t.Fatalf("migrate database: %v", err)
	}
	if err := database.RefreshVectorTypes(ctx, pool); err != nil {
		t.Fatalf("refresh pgvector types: %v", err)
	}
	refuseToClaimDeveloperEmbeddingJobs(t, ctx, pool)

	documentRepository := newOwnedDocumentFixture(t, ctx, pool)
	chunkRepository := postgresrepository.NewChunkRepository(pool)
	jobRepository := postgresrepository.NewEmbeddingJobRepositoryWithPolicies(
		pool,
		embeddingdomain.JobSchedulingPolicy{
			MaxInFlightPerOwner:         1,
			MaxBorrowedInFlightPerOwner: 2,
			StarvationThreshold:         time.Minute,
		},
		embeddingdomain.JobLeasePolicy{
			WorkerID:      "embedding-lease-test-worker",
			LeaseDuration: time.Minute,
		},
	)
	fixture := createEmbeddingWorkerFixture(
		t,
		ctx,
		pool,
		documentRepository,
		chunkRepository,
		jobRepository,
		"lease-fencing",
		[]string{"lease protected chunk"},
	)

	firstClaim, err := jobRepository.ClaimNextEmbeddingJob(ctx)
	if err != nil {
		t.Fatalf("claim first embedding lease: %v", err)
	}
	if firstClaim.ID != fixture.job.ID || firstClaim.LeaseToken == "" {
		t.Fatalf("first claim = %+v, want fixture with lease token", firstClaim)
	}
	if err := jobRepository.RenewEmbeddingJobLease(
		ctx,
		firstClaim.ID,
		firstClaim.LeaseToken,
	); err != nil {
		t.Fatalf("renew current embedding lease: %v", err)
	}

	// 用数据库时间强制模拟 Worker 失联，不依赖不稳定的 Sleep。
	if _, err := pool.Exec(
		ctx,
		`UPDATE embedding_jobs
		 SET lease_expires_at = CURRENT_TIMESTAMP - INTERVAL '1 second'
		 WHERE id = $1`,
		firstClaim.ID,
	); err != nil {
		t.Fatalf("expire first embedding lease: %v", err)
	}
	recoveredCount, err := jobRepository.RequeueExpiredEmbeddingJobs(
		ctx,
		"embedding job lease expired and was requeued",
	)
	if err != nil || recoveredCount != 1 {
		t.Fatalf("recover expired embedding lease = (%d, %v), want (1, nil)", recoveredCount, err)
	}

	secondClaim, err := jobRepository.ClaimNextEmbeddingJob(ctx)
	if err != nil {
		t.Fatalf("claim recovered embedding job: %v", err)
	}
	if secondClaim.ID != firstClaim.ID ||
		secondClaim.LeaseToken == "" ||
		secondClaim.LeaseToken == firstClaim.LeaseToken {
		t.Fatalf("second claim = %+v, want same job with new token", secondClaim)
	}

	completion := embeddingdomain.JobCompletion{
		Vectors: []embeddingdomain.ChunkVector{
			{ChunkID: fixture.chunks[0].ID, Values: embeddingVector(1536, 0.5)},
		},
		PromptTokens: 1,
		TotalTokens:  1,
	}
	if err := jobRepository.MarkEmbeddingJobSucceeded(
		ctx,
		firstClaim.ID,
		firstClaim.LeaseToken,
		completion,
	); !errors.Is(err, embeddingdomain.ErrJobLeaseLost) {
		t.Fatalf("stale success error = %v, want ErrJobLeaseLost", err)
	}
	if err := jobRepository.RequeueEmbeddingJob(
		ctx,
		firstClaim.ID,
		firstClaim.LeaseToken,
		time.Now(),
		"stale worker retry",
	); !errors.Is(err, embeddingdomain.ErrJobLeaseLost) {
		t.Fatalf("stale requeue error = %v, want ErrJobLeaseLost", err)
	}
	if err := jobRepository.MarkEmbeddingJobFailed(
		ctx,
		firstClaim.ID,
		firstClaim.LeaseToken,
		"stale worker failure",
	); !errors.Is(err, embeddingdomain.ErrJobLeaseLost) {
		t.Fatalf("stale failure error = %v, want ErrJobLeaseLost", err)
	}
	assertEmbeddingJobState(
		t,
		ctx,
		pool,
		secondClaim.ID,
		embeddingdomain.JobStatusProcessing,
		0,
	)

	if err := jobRepository.MarkEmbeddingJobSucceeded(
		ctx,
		secondClaim.ID,
		secondClaim.LeaseToken,
		completion,
	); err != nil {
		t.Fatalf("current lease finalization: %v", err)
	}
	assertEmbeddingJobState(
		t,
		ctx,
		pool,
		secondClaim.ID,
		embeddingdomain.JobStatusSucceeded,
		1,
	)
}
