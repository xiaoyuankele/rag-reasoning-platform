package postgres_test

import (
	"context"
	"errors"
	"os"
	"testing"
	"time"

	accessdomain "rag-reasoning-platform/backend/internal/domain/access"
	documentdomain "rag-reasoning-platform/backend/internal/domain/document"
	postgresrepository "rag-reasoning-platform/backend/internal/infrastructure/postgres"
)

func TestScopedProcessingJobRepositoryRejectsInvalidAdmissionLimits(t *testing.T) {
	scope, err := accessdomain.NewOwnerScope(1)
	if err != nil {
		t.Fatalf("create owner scope: %v", err)
	}
	repository := postgresrepository.NewScopedProcessingJobRepository(
		nil,
		documentdomain.ProcessingJobAdmissionLimits{},
	)

	if _, err := repository.CreateProcessingJob(
		context.Background(),
		scope,
		1,
	); !errors.Is(err, documentdomain.ErrInvalidProcessingJobAdmissionLimits) {
		t.Fatalf(
			"CreateProcessingJob() error = %v, want ErrInvalidProcessingJobAdmissionLimits",
			err,
		)
	}
}

func TestScopedProcessingJobRepositoryEnforcesAdmissionLimits(t *testing.T) {
	if os.Getenv("RUN_DATABASE_TESTS") != "1" {
		t.Skip("set RUN_DATABASE_TESTS=1 to run PostgreSQL integration tests")
	}

	t.Run("per owner duplicate precedence and terminal release", func(t *testing.T) {
		ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
		defer cancel()
		pool := openIsolatedDocumentTestPool(t, ctx)
		ownerID := insertScopedRepositoryUser(
			t,
			ctx,
			pool,
			"processing-limit-owner@example.com",
		)
		owner, _ := accessdomain.NewOwnerScope(ownerID)
		documents := postgresrepository.NewScopedDocumentRepository(pool)
		jobs := postgresrepository.NewScopedProcessingJobRepository(
			pool,
			documentdomain.ProcessingJobAdmissionLimits{
				MaxActiveJobsPerOwner: 1,
				MaxActiveJobsGlobal:   10,
			},
		)

		firstDocument, err := documents.Create(
			ctx,
			owner,
			scopedDocumentInput(
				"processing-owner-limit-1.md",
				"processing-limits/owner-1.md",
				"8",
			),
		)
		if err != nil {
			t.Fatalf("create first document: %v", err)
		}
		secondDocument, err := documents.Create(
			ctx,
			owner,
			scopedDocumentInput(
				"processing-owner-limit-2.md",
				"processing-limits/owner-2.md",
				"9",
			),
		)
		if err != nil {
			t.Fatalf("create second document: %v", err)
		}

		firstJob, err := jobs.CreateProcessingJob(ctx, owner, firstDocument.ID)
		if err != nil {
			t.Fatalf("create first job: %v", err)
		}
		if _, err := jobs.CreateProcessingJob(
			ctx,
			owner,
			firstDocument.ID,
		); !errors.Is(err, documentdomain.ErrActiveProcessingJobExists) {
			t.Fatalf("duplicate error = %v, want active job conflict", err)
		}
		if _, err := jobs.CreateProcessingJob(
			ctx,
			owner,
			secondDocument.ID,
		); !errors.Is(err, documentdomain.ErrOwnerActiveProcessingJobLimitExceeded) {
			t.Fatalf("second document error = %v, want owner limit", err)
		}

		if _, err := pool.Exec(
			ctx,
			"UPDATE document_jobs SET status = 'succeeded' WHERE id = $1",
			firstJob.ID,
		); err != nil {
			t.Fatalf("mark first job terminal: %v", err)
		}
		if _, err := jobs.CreateProcessingJob(
			ctx,
			owner,
			secondDocument.ID,
		); err != nil {
			t.Fatalf("create job after terminal release: %v", err)
		}
	})

	t.Run("global", func(t *testing.T) {
		ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
		defer cancel()
		pool := openIsolatedDocumentTestPool(t, ctx)
		documents := postgresrepository.NewScopedDocumentRepository(pool)
		jobs := postgresrepository.NewScopedProcessingJobRepository(
			pool,
			documentdomain.ProcessingJobAdmissionLimits{
				MaxActiveJobsPerOwner: 1,
				MaxActiveJobsGlobal:   2,
			},
		)

		type ownedDocument struct {
			scope      accessdomain.OwnerScope
			documentID int64
		}
		ownedDocuments := make([]ownedDocument, 0, 3)
		for index, fixture := range []struct {
			email         string
			storagePath   string
			hashCharacter string
		}{
			{"processing-global-a@example.com", "processing-limits/global-a.md", "a"},
			{"processing-global-b@example.com", "processing-limits/global-b.md", "b"},
			{"processing-global-c@example.com", "processing-limits/global-c.md", "c"},
		} {
			ownerID := insertScopedRepositoryUser(t, ctx, pool, fixture.email)
			owner, _ := accessdomain.NewOwnerScope(ownerID)
			document, err := documents.Create(
				ctx,
				owner,
				scopedDocumentInput(
					fixture.email,
					fixture.storagePath,
					fixture.hashCharacter,
				),
			)
			if err != nil {
				t.Fatalf("create global document %d: %v", index, err)
			}
			ownedDocuments = append(ownedDocuments, ownedDocument{
				scope:      owner,
				documentID: document.ID,
			})
		}

		for index := 0; index < 2; index++ {
			fixture := ownedDocuments[index]
			if _, err := jobs.CreateProcessingJob(
				ctx,
				fixture.scope,
				fixture.documentID,
			); err != nil {
				t.Fatalf("create global job %d: %v", index, err)
			}
		}
		third := ownedDocuments[2]
		if _, err := jobs.CreateProcessingJob(
			ctx,
			third.scope,
			third.documentID,
		); !errors.Is(err, documentdomain.ErrGlobalProcessingJobLimitExceeded) {
			t.Fatalf("third global job error = %v, want global limit", err)
		}
	})
}

func TestScopedProcessingJobRepositorySerializesConcurrentAdmission(t *testing.T) {
	if os.Getenv("RUN_DATABASE_TESTS") != "1" {
		t.Skip("set RUN_DATABASE_TESTS=1 to run PostgreSQL integration tests")
	}

	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	pool := openIsolatedDocumentTestPool(t, ctx)
	documents := postgresrepository.NewScopedDocumentRepository(pool)

	type request struct {
		scope      accessdomain.OwnerScope
		documentID int64
	}
	requests := make([]request, 0, 2)
	for _, fixture := range []struct {
		email         string
		storagePath   string
		hashCharacter string
	}{
		{"processing-concurrent-a@example.com", "processing-limits/concurrent-a.md", "d"},
		{"processing-concurrent-b@example.com", "processing-limits/concurrent-b.md", "e"},
	} {
		ownerID := insertScopedRepositoryUser(t, ctx, pool, fixture.email)
		owner, _ := accessdomain.NewOwnerScope(ownerID)
		document, err := documents.Create(
			ctx,
			owner,
			scopedDocumentInput(
				fixture.email,
				fixture.storagePath,
				fixture.hashCharacter,
			),
		)
		if err != nil {
			t.Fatalf("create concurrent document: %v", err)
		}
		requests = append(requests, request{
			scope:      owner,
			documentID: document.ID,
		})
	}

	jobs := postgresrepository.NewScopedProcessingJobRepository(
		pool,
		documentdomain.ProcessingJobAdmissionLimits{
			MaxActiveJobsPerOwner: 1,
			MaxActiveJobsGlobal:   1,
		},
	)
	start := make(chan struct{})
	results := make(chan error, len(requests))
	for _, queuedRequest := range requests {
		queuedRequest := queuedRequest
		go func() {
			<-start
			_, err := jobs.CreateProcessingJob(
				ctx,
				queuedRequest.scope,
				queuedRequest.documentID,
			)
			results <- err
		}()
	}
	close(start)

	var successCount int
	var capacityCount int
	for range len(requests) {
		err := <-results
		switch {
		case err == nil:
			successCount++
		case errors.Is(err, documentdomain.ErrGlobalProcessingJobLimitExceeded):
			capacityCount++
		default:
			t.Fatalf("concurrent request error = %v", err)
		}
	}
	if successCount != 1 || capacityCount != 1 {
		t.Fatalf(
			"concurrent results success=%d capacity=%d, want 1/1",
			successCount,
			capacityCount,
		)
	}

	var activeCount int
	if err := pool.QueryRow(
		ctx,
		"SELECT COUNT(*) FROM document_jobs WHERE status IN ('queued', 'processing')",
	).Scan(&activeCount); err != nil {
		t.Fatalf("count active jobs: %v", err)
	}
	if activeCount != 1 {
		t.Fatalf("active job count = %d, want 1", activeCount)
	}
}
