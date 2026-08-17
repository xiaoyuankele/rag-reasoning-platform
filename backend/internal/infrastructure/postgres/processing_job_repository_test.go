package postgres_test

import (
	"context"
	"errors"
	"fmt"
	"os"
	"strings"
	"testing"
	"time"

	"rag-reasoning-platform/backend/internal/config"
	documentdomain "rag-reasoning-platform/backend/internal/domain/document"
	"rag-reasoning-platform/backend/internal/infrastructure/database"
	postgresrepository "rag-reasoning-platform/backend/internal/infrastructure/postgres"
	"rag-reasoning-platform/backend/migrations"
)

func TestProcessingJobRepositoryCreate(t *testing.T) {
	if os.Getenv("RUN_DATABASE_TESTS") != "1" {
		t.Skip("set RUN_DATABASE_TESTS=1 to run PostgreSQL integration tests")
	}

	ctx, cancel := context.WithTimeout(
		context.Background(),
		20*time.Second,
	)
	defer cancel()

	databaseConfig, err := config.LoadDatabase()
	if err != nil {
		t.Fatalf("load database configuration: %v", err)
	}

	pool, err := database.Open(
		ctx,
		databaseConfig.ConnectionString(),
	)
	if err != nil {
		t.Fatalf("open database: %v", err)
	}
	t.Cleanup(pool.Close)

	if err := database.Migrate(ctx, pool, migrations.Files); err != nil {
		t.Fatalf("migrate database: %v", err)
	}

	documentRepository := newOwnedDocumentFixture(t, ctx, pool)
	jobRepository := postgresrepository.NewProcessingJobRepository(pool)
	uniqueValue := time.Now().UnixNano()

	createdDocument, err := documentRepository.Create(
		ctx,
		documentdomain.CreateInput{
			OriginalName: "processing-job-test.pdf",
			StoragePath: fmt.Sprintf(
				"integration-tests/processing-job-%d.pdf",
				uniqueValue,
			),
			MIMEType:  "application/pdf",
			SizeBytes: 4096,
			SHA256:    strings.Repeat("e", 64),
		},
	)
	if err != nil {
		t.Fatalf("create document: %v", err)
	}
	defer func() {
		cleanupContext, cleanupCancel := context.WithTimeout(
			context.Background(),
			5*time.Second,
		)
		defer cleanupCancel()

		if _, cleanupErr := pool.Exec(
			cleanupContext,
			"DELETE FROM documents WHERE id = $1",
			createdDocument.ID,
		); cleanupErr != nil {
			t.Errorf("clean up processing job document: %v", cleanupErr)
		}
	}()

	createdJob, err := jobRepository.CreateProcessingJob(
		ctx,
		createdDocument.ID,
	)
	if err != nil {
		t.Fatalf("create processing job: %v", err)
	}

	if createdJob.ID <= 0 {
		t.Fatalf("processing job ID = %d, want positive", createdJob.ID)
	}
	if createdJob.DocumentID != createdDocument.ID {
		t.Fatalf(
			"processing job document ID = %d, want %d",
			createdJob.DocumentID,
			createdDocument.ID,
		)
	}
	if createdJob.Status != documentdomain.ProcessingJobStatusQueued {
		t.Fatalf(
			"processing job status = %q, want %q",
			createdJob.Status,
			documentdomain.ProcessingJobStatusQueued,
		)
	}
	if createdJob.AttemptCount != 0 {
		t.Fatalf(
			"processing job attempt count = %d, want 0",
			createdJob.AttemptCount,
		)
	}
	if createdJob.ErrorMessage != nil ||
		createdJob.StartedAt != nil ||
		createdJob.CompletedAt != nil {
		t.Fatal("new queued job must not contain error or execution timestamps")
	}
	if createdJob.CreatedAt.IsZero() || createdJob.UpdatedAt.IsZero() {
		t.Fatal("new queued job must contain database timestamps")
	}

	foundJob, err := jobRepository.GetProcessingJobByID(
		ctx,
		createdJob.ID,
	)
	if err != nil {
		t.Fatalf("get processing job by ID: %v", err)
	}
	if foundJob.ID != createdJob.ID ||
		foundJob.DocumentID != createdDocument.ID ||
		foundJob.Status != documentdomain.ProcessingJobStatusQueued {
		t.Fatalf(
			"found processing job = %+v, want created job %+v",
			foundJob,
			createdJob,
		)
	}

	_, err = jobRepository.GetProcessingJobByID(ctx, -1)
	if !errors.Is(err, documentdomain.ErrProcessingJobNotFound) {
		t.Fatalf(
			"missing GetProcessingJobByID() error = %v, want ErrProcessingJobNotFound",
			err,
		)
	}

	_, err = jobRepository.CreateProcessingJob(
		ctx,
		createdDocument.ID,
	)
	if !errors.Is(err, documentdomain.ErrActiveProcessingJobExists) {
		t.Fatalf(
			"duplicate CreateProcessingJob() error = %v, want ErrActiveProcessingJobExists",
			err,
		)
	}

	if err := documentRepository.Delete(ctx, createdDocument.ID); err != nil {
		t.Fatalf("delete document: %v", err)
	}

	var remainingJobs int
	if err := pool.QueryRow(
		ctx,
		"SELECT COUNT(*) FROM document_jobs WHERE document_id = $1",
		createdDocument.ID,
	).Scan(&remainingJobs); err != nil {
		t.Fatalf("count jobs after document deletion: %v", err)
	}
	if remainingJobs != 0 {
		t.Fatalf(
			"remaining jobs after document deletion = %d, want 0",
			remainingJobs,
		)
	}
}
