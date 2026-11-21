package postgres_test

import (
	"context"
	"errors"
	"os"
	"testing"
	"time"

	embeddingdomain "rag-reasoning-platform/backend/internal/domain/embedding"
	postgresrepository "rag-reasoning-platform/backend/internal/infrastructure/postgres"
)

// TestScopedEmbeddingJobRepositoryCancellation 验证取消的状态边界和幂等性。
func TestScopedEmbeddingJobRepositoryCancellation(t *testing.T) {
	if os.Getenv("RUN_DATABASE_TESTS") != "1" {
		t.Skip("set RUN_DATABASE_TESTS=1 to run PostgreSQL integration tests")
	}

	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	pool := openIsolatedDocumentTestPool(t, ctx)
	documents := newOwnedDocumentFixture(t, ctx, pool)
	jobs := postgresrepository.NewScopedEmbeddingJobRepository(pool, testEmbeddingAdmissionLimits())

	t.Run("waiting document can be canceled idempotently", func(t *testing.T) {
		createdDocument, err := documents.Create(ctx, scopedDocumentInput(
			"cancel-waiting.pdf", "cancel-tests/waiting.pdf", "a",
		))
		if err != nil {
			t.Fatalf("create waiting document: %v", err)
		}
		requested, err := jobs.RequestEmbeddingJob(ctx, documents.scope, createdDocument.ID, "test-model", 8)
		if err != nil {
			t.Fatalf("request waiting job: %v", err)
		}

		canceledJob, err := jobs.CancelEmbeddingJob(ctx, documents.scope, requested.Job.ID)
		if err != nil {
			t.Fatalf("cancel waiting job: %v", err)
		}
		if canceledJob.Status != embeddingdomain.JobStatusCanceled || canceledJob.CompletedAt == nil {
			t.Fatalf("canceled job = %+v, want canceled with completion time", canceledJob)
		}

		repeatedJob, err := jobs.CancelEmbeddingJob(ctx, documents.scope, requested.Job.ID)
		if err != nil {
			t.Fatalf("repeat cancellation: %v", err)
		}
		if repeatedJob.ID != canceledJob.ID || repeatedJob.Status != embeddingdomain.JobStatusCanceled {
			t.Fatalf("repeat cancellation job = %+v, want original canceled job", repeatedJob)
		}
	})

	t.Run("queued job can be canceled", func(t *testing.T) {
		createdDocument, err := documents.Create(ctx, scopedDocumentInput(
			"cancel-queued.pdf", "cancel-tests/queued.pdf", "b",
		))
		if err != nil {
			t.Fatalf("create queued document: %v", err)
		}
		if _, err := pool.Exec(ctx, "UPDATE documents SET status = 'ready' WHERE id = $1", createdDocument.ID); err != nil {
			t.Fatalf("mark document ready: %v", err)
		}
		requested, err := jobs.RequestEmbeddingJob(ctx, documents.scope, createdDocument.ID, "test-model", 8)
		if err != nil {
			t.Fatalf("request queued job: %v", err)
		}
		canceledJob, err := jobs.CancelEmbeddingJob(ctx, documents.scope, requested.Job.ID)
		if err != nil || canceledJob.Status != embeddingdomain.JobStatusCanceled {
			t.Fatalf("CancelEmbeddingJob(queued) = (%+v, %v), want canceled", canceledJob, err)
		}
	})

	t.Run("processing job cannot be canceled", func(t *testing.T) {
		createdDocument, err := documents.Create(ctx, scopedDocumentInput(
			"cancel-processing.pdf", "cancel-tests/processing.pdf", "c",
		))
		if err != nil {
			t.Fatalf("create processing document: %v", err)
		}
		if _, err := pool.Exec(ctx, "UPDATE documents SET status = 'ready' WHERE id = $1", createdDocument.ID); err != nil {
			t.Fatalf("mark document ready: %v", err)
		}
		requested, err := jobs.RequestEmbeddingJob(ctx, documents.scope, createdDocument.ID, "test-model", 8)
		if err != nil {
			t.Fatalf("request processing fixture job: %v", err)
		}
		if _, err := pool.Exec(ctx, "UPDATE embedding_jobs SET status = 'processing' WHERE id = $1", requested.Job.ID); err != nil {
			t.Fatalf("mark job processing: %v", err)
		}
		_, err = jobs.CancelEmbeddingJob(ctx, documents.scope, requested.Job.ID)
		if !errors.Is(err, embeddingdomain.ErrJobProcessingCannotCancel) {
			t.Fatalf("CancelEmbeddingJob(processing) error = %v, want ErrJobProcessingCannotCancel", err)
		}
	})

	t.Run("terminal job cannot be canceled", func(t *testing.T) {
		createdDocument, err := documents.Create(ctx, scopedDocumentInput(
			"cancel-terminal.pdf", "cancel-tests/terminal.pdf", "d",
		))
		if err != nil {
			t.Fatalf("create terminal document: %v", err)
		}
		if _, err := pool.Exec(ctx, "UPDATE documents SET status = 'ready' WHERE id = $1", createdDocument.ID); err != nil {
			t.Fatalf("mark document ready: %v", err)
		}
		requested, err := jobs.RequestEmbeddingJob(ctx, documents.scope, createdDocument.ID, "test-model", 8)
		if err != nil {
			t.Fatalf("request terminal fixture job: %v", err)
		}
		if _, err := pool.Exec(
			ctx,
			"UPDATE embedding_jobs SET status = 'failed', completed_at = CURRENT_TIMESTAMP WHERE id = $1",
			requested.Job.ID,
		); err != nil {
			t.Fatalf("mark job failed: %v", err)
		}
		_, err = jobs.CancelEmbeddingJob(ctx, documents.scope, requested.Job.ID)
		if !errors.Is(err, embeddingdomain.ErrJobTerminalCannotCancel) {
			t.Fatalf("CancelEmbeddingJob(failed) error = %v, want ErrJobTerminalCannotCancel", err)
		}
	})

	t.Run("another owner sees not found", func(t *testing.T) {
		otherDocuments := newOwnedDocumentFixture(t, ctx, pool)
		createdDocument, err := documents.Create(ctx, scopedDocumentInput(
			"cancel-owner.pdf", "cancel-tests/owner.pdf", "e",
		))
		if err != nil {
			t.Fatalf("create owner document: %v", err)
		}
		requested, err := jobs.RequestEmbeddingJob(ctx, documents.scope, createdDocument.ID, "test-model", 8)
		if err != nil {
			t.Fatalf("request owner job: %v", err)
		}
		_, err = jobs.CancelEmbeddingJob(ctx, otherDocuments.scope, requested.Job.ID)
		if !errors.Is(err, embeddingdomain.ErrJobNotFound) {
			t.Fatalf("CancelEmbeddingJob(other owner) error = %v, want ErrJobNotFound", err)
		}
	})
}
