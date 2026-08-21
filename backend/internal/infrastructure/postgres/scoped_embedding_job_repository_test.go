package postgres_test

import (
	"context"
	"errors"
	"os"
	"testing"
	"time"

	accessdomain "rag-reasoning-platform/backend/internal/domain/access"
	documentdomain "rag-reasoning-platform/backend/internal/domain/document"
	embeddingdomain "rag-reasoning-platform/backend/internal/domain/embedding"
	postgresrepository "rag-reasoning-platform/backend/internal/infrastructure/postgres"
)

// TestScopedEmbeddingJobRepositoryEnforcesOwnerBoundary 验证向量任务的
// 创建和查询都继承关联文档的所有者边界。
func TestScopedEmbeddingJobRepositoryEnforcesOwnerBoundary(t *testing.T) {
	if os.Getenv("RUN_DATABASE_TESTS") != "1" {
		t.Skip("set RUN_DATABASE_TESTS=1 to run PostgreSQL integration tests")
	}

	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	pool := openIsolatedDocumentTestPool(t, ctx)
	ownerAID := insertScopedRepositoryUser(t, ctx, pool, "embedding-owner-a@example.com")
	ownerBID := insertScopedRepositoryUser(t, ctx, pool, "embedding-owner-b@example.com")
	ownerA, err := accessdomain.NewOwnerScope(ownerAID)
	if err != nil {
		t.Fatalf("create owner A scope: %v", err)
	}
	ownerB, err := accessdomain.NewOwnerScope(ownerBID)
	if err != nil {
		t.Fatalf("create owner B scope: %v", err)
	}

	documents := postgresrepository.NewScopedDocumentRepository(pool)
	jobs := postgresrepository.NewScopedEmbeddingJobRepository(pool)
	ownerADocument, err := documents.Create(
		ctx,
		ownerA,
		scopedDocumentInput(
			"embedding-owner-a.md",
			"scoped-tests/embedding-owner-a.md",
			"e",
		),
	)
	if err != nil {
		t.Fatalf("create owner A document: %v", err)
	}
	ownerBDocument, err := documents.Create(
		ctx,
		ownerB,
		scopedDocumentInput(
			"embedding-owner-b.md",
			"scoped-tests/embedding-owner-b.md",
			"f",
		),
	)
	if err != nil {
		t.Fatalf("create owner B document: %v", err)
	}
	if _, err := pool.Exec(
		ctx,
		"UPDATE documents SET status = 'ready' WHERE id = $1",
		ownerADocument.ID,
	); err != nil {
		t.Fatalf("mark owner A document ready: %v", err)
	}

	createdResult, err := jobs.RequestEmbeddingJob(
		ctx,
		ownerA,
		ownerADocument.ID,
		"test-embedding-model",
		1536,
	)
	if err != nil {
		t.Fatalf("RequestEmbeddingJob(owner A) error = %v", err)
	}
	if !createdResult.Created {
		t.Fatal("RequestEmbeddingJob(owner A) Created = false, want true")
	}
	createdJob := createdResult.Job
	if createdJob.DocumentID != ownerADocument.ID ||
		createdJob.ModelName != "test-embedding-model" ||
		createdJob.Dimensions != 1536 ||
		createdJob.Status != embeddingdomain.JobStatusQueued {
		t.Fatalf("RequestEmbeddingJob(owner A) = %+v, want configured queued job", createdJob)
	}

	if _, err := jobs.RequestEmbeddingJob(
		ctx, ownerB, ownerADocument.ID, "test-embedding-model", 1536,
	); !errors.Is(err, documentdomain.ErrNotFound) {
		t.Fatalf("RequestEmbeddingJob(owner B, owner A document) error = %v, want ErrNotFound", err)
	}
	if _, err := jobs.RequestEmbeddingJob(
		ctx, ownerA, ownerBDocument.ID, "test-embedding-model", 1536,
	); !errors.Is(err, documentdomain.ErrNotFound) {
		t.Fatalf("RequestEmbeddingJob(owner A, owner B document) error = %v, want ErrNotFound", err)
	}
	duplicateResult, err := jobs.RequestEmbeddingJob(
		ctx, ownerA, ownerADocument.ID, "another-model", 768,
	)
	if err != nil {
		t.Fatalf("RequestEmbeddingJob(duplicate) error = %v, want nil", err)
	}
	if duplicateResult.Created {
		t.Fatal("RequestEmbeddingJob(duplicate) Created = true, want false")
	}
	if duplicateResult.Job.ID != createdJob.ID {
		t.Fatalf("RequestEmbeddingJob(duplicate) job ID = %d, want %d", duplicateResult.Job.ID, createdJob.ID)
	}

	foundJob, err := jobs.GetEmbeddingJobByID(ctx, ownerA, createdJob.ID)
	if err != nil || foundJob.ID != createdJob.ID {
		t.Fatalf("GetEmbeddingJobByID(owner A) = (%+v, %v), want created job", foundJob, err)
	}
	if _, err := jobs.GetEmbeddingJobByID(
		ctx,
		ownerB,
		createdJob.ID,
	); !errors.Is(err, embeddingdomain.ErrJobNotFound) {
		t.Fatalf("GetEmbeddingJobByID(owner B) error = %v, want ErrJobNotFound", err)
	}
}

// TestScopedEmbeddingJobRepositoryRejectsInvalidScope 验证无效 Scope
// 会在访问数据库前被拒绝。
func TestScopedEmbeddingJobRepositoryRejectsInvalidScope(t *testing.T) {
	var invalidScope accessdomain.OwnerScope
	ctx := context.Background()
	repository := postgresrepository.NewScopedEmbeddingJobRepository(nil)

	if _, err := repository.RequestEmbeddingJob(
		ctx, invalidScope, 1, "test-model", 1536,
	); !errors.Is(err, accessdomain.ErrInvalidOwnerScope) {
		t.Fatalf("RequestEmbeddingJob(invalid scope) error = %v, want ErrInvalidOwnerScope", err)
	}
	if _, err := repository.GetEmbeddingJobByID(
		ctx,
		invalidScope,
		1,
	); !errors.Is(err, accessdomain.ErrInvalidOwnerScope) {
		t.Fatalf("GetEmbeddingJobByID(invalid scope) error = %v, want ErrInvalidOwnerScope", err)
	}
	if _, err := repository.FindLatestEmbeddingJobsByDocumentIDs(
		ctx,
		invalidScope,
		[]int64{1},
	); !errors.Is(err, accessdomain.ErrInvalidOwnerScope) {
		t.Fatalf("FindLatestEmbeddingJobsByDocumentIDs(invalid scope) error = %v, want ErrInvalidOwnerScope", err)
	}
}

func TestScopedEmbeddingJobRepositoryFindsLatestJobsByDocumentIDs(t *testing.T) {
	if os.Getenv("RUN_DATABASE_TESTS") != "1" {
		t.Skip("set RUN_DATABASE_TESTS=1 to run PostgreSQL integration tests")
	}

	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	pool := openIsolatedDocumentTestPool(t, ctx)
	ownerAID := insertScopedRepositoryUser(t, ctx, pool, "latest-embedding-owner-a@example.com")
	ownerBID := insertScopedRepositoryUser(t, ctx, pool, "latest-embedding-owner-b@example.com")
	ownerA, _ := accessdomain.NewOwnerScope(ownerAID)
	ownerB, _ := accessdomain.NewOwnerScope(ownerBID)
	documents := postgresrepository.NewScopedDocumentRepository(pool)
	jobs := postgresrepository.NewScopedEmbeddingJobRepository(pool)

	ownerADocument, err := documents.Create(ctx, ownerA, scopedDocumentInput("latest-a.md", "latest/a.md", "a"))
	if err != nil {
		t.Fatalf("create owner A document: %v", err)
	}
	ownerANoJobDocument, err := documents.Create(ctx, ownerA, scopedDocumentInput("latest-no-job.md", "latest/no-job.md", "b"))
	if err != nil {
		t.Fatalf("create owner A no-job document: %v", err)
	}
	ownerBDocument, err := documents.Create(ctx, ownerB, scopedDocumentInput("latest-b.md", "latest/b.md", "c"))
	if err != nil {
		t.Fatalf("create owner B document: %v", err)
	}
	if _, err := pool.Exec(ctx, "UPDATE documents SET status = 'ready' WHERE id = ANY($1::BIGINT[])", []int64{ownerADocument.ID, ownerBDocument.ID}); err != nil {
		t.Fatalf("mark documents ready: %v", err)
	}

	firstResult, err := jobs.RequestEmbeddingJob(ctx, ownerA, ownerADocument.ID, "old-model", 768)
	if err != nil {
		t.Fatalf("create first owner A job: %v", err)
	}
	if _, err := pool.Exec(ctx, "UPDATE embedding_jobs SET status = 'failed' WHERE id = $1", firstResult.Job.ID); err != nil {
		t.Fatalf("finish first owner A job: %v", err)
	}
	latestResult, err := jobs.RequestEmbeddingJob(ctx, ownerA, ownerADocument.ID, "current-model", 1536)
	if err != nil {
		t.Fatalf("create latest owner A job: %v", err)
	}
	if _, err := jobs.RequestEmbeddingJob(ctx, ownerB, ownerBDocument.ID, "private-model", 1536); err != nil {
		t.Fatalf("create owner B job: %v", err)
	}

	foundJobs, err := jobs.FindLatestEmbeddingJobsByDocumentIDs(
		ctx,
		ownerA,
		[]int64{ownerADocument.ID, ownerANoJobDocument.ID, ownerBDocument.ID, 999999},
	)
	if err != nil {
		t.Fatalf("FindLatestEmbeddingJobsByDocumentIDs() error = %v", err)
	}
	if len(foundJobs) != 1 || foundJobs[0].ID != latestResult.Job.ID || foundJobs[0].ModelName != "current-model" {
		t.Fatalf("found jobs = %+v, want only latest owner A job %+v", foundJobs, latestResult.Job)
	}
}
