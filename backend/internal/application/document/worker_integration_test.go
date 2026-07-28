package document

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

type integrationDocumentProcessor struct {
	processFunc func(context.Context, documentdomain.Document) error
}

func (p *integrationDocumentProcessor) Process(
	ctx context.Context,
	document documentdomain.Document,
) error {
	return p.processFunc(ctx, document)
}

// TestWorkerRunOnceIntegration 使用真实 PostgreSQL 仓储验证 Worker 的
// 领取、查询、处理以及成功/失败状态回写能够组成完整链路。
func TestWorkerRunOnceIntegration(t *testing.T) {
	if os.Getenv("RUN_DATABASE_TESTS") != "1" {
		t.Skip("set RUN_DATABASE_TESTS=1 to run PostgreSQL integration tests")
	}

	ctx, cancel := context.WithTimeout(
		context.Background(),
		30*time.Second,
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
	defer pool.Close()

	if err := database.Migrate(ctx, pool, migrations.Files); err != nil {
		t.Fatalf("migrate database: %v", err)
	}

	var existingQueuedJobs int
	if err := pool.QueryRow(
		ctx,
		"SELECT COUNT(*) FROM document_jobs WHERE status = 'queued'",
	).Scan(&existingQueuedJobs); err != nil {
		t.Fatalf("count existing queued jobs: %v", err)
	}
	if existingQueuedJobs != 0 {
		t.Skipf(
			"database contains %d queued jobs; refusing to process developer data",
			existingQueuedJobs,
		)
	}

	documentRepository := postgresrepository.NewDocumentRepository(pool)
	jobRepository := postgresrepository.NewProcessingJobRepository(pool)
	createdDocumentIDs := make([]int64, 0, 2)
	defer func() {
		cleanupContext, cleanupCancel := context.WithTimeout(
			context.Background(),
			5*time.Second,
		)
		defer cleanupCancel()

		for _, documentID := range createdDocumentIDs {
			if _, cleanupErr := pool.Exec(
				cleanupContext,
				"DELETE FROM documents WHERE id = $1",
				documentID,
			); cleanupErr != nil {
				t.Errorf(
					"clean up Worker integration document %d: %v",
					documentID,
					cleanupErr,
				)
			}
		}
	}()

	t.Run("successful processor completes job", func(t *testing.T) {
		createdDocument, createdJob :=
			createWorkerIntegrationJob(
				t,
				ctx,
				documentRepository,
				jobRepository,
				"success",
				"a",
			)
		createdDocumentIDs = append(
			createdDocumentIDs,
			createdDocument.ID,
		)

		processor := &integrationDocumentProcessor{
			processFunc: func(
				_ context.Context,
				document documentdomain.Document,
			) error {
				if document.ID != createdDocument.ID {
					t.Fatalf(
						"processor document ID = %d, want %d",
						document.ID,
						createdDocument.ID,
					)
				}
				return nil
			},
		}
		worker := NewWorker(
			jobRepository,
			documentRepository,
			processor,
		)

		handled, err := worker.RunOnce(ctx)
		if err != nil {
			t.Fatalf("RunOnce() success error = %v, want nil", err)
		}
		if !handled {
			t.Fatal("RunOnce() success handled = false, want true")
		}

		assertWorkerIntegrationState(
			t,
			ctx,
			documentRepository,
			jobRepository,
			createdJob.ID,
			createdDocument.ID,
			documentdomain.ProcessingJobStatusSucceeded,
			documentdomain.StatusReady,
			nil,
		)
	})

	t.Run("failed processor fails job safely", func(t *testing.T) {
		createdDocument, createdJob :=
			createWorkerIntegrationJob(
				t,
				ctx,
				documentRepository,
				jobRepository,
				"failure",
				"b",
			)
		createdDocumentIDs = append(
			createdDocumentIDs,
			createdDocument.ID,
		)

		processingError := errors.New(
			"python command contains private internal details",
		)
		processor := &integrationDocumentProcessor{
			processFunc: func(
				context.Context,
				documentdomain.Document,
			) error {
				return processingError
			},
		}
		worker := NewWorker(
			jobRepository,
			documentRepository,
			processor,
		)

		handled, err := worker.RunOnce(ctx)
		if !errors.Is(err, processingError) {
			t.Fatalf(
				"RunOnce() failure error = %v, want processing error",
				err,
			)
		}
		if !handled {
			t.Fatal("RunOnce() failure handled = false, want true")
		}

		expectedMessage := safeProcessingFailureMessage
		assertWorkerIntegrationState(
			t,
			ctx,
			documentRepository,
			jobRepository,
			createdJob.ID,
			createdDocument.ID,
			documentdomain.ProcessingJobStatusFailed,
			documentdomain.StatusFailed,
			&expectedMessage,
		)
	})
}

func createWorkerIntegrationJob(
	t *testing.T,
	ctx context.Context,
	documentRepository *postgresrepository.DocumentRepository,
	jobRepository *postgresrepository.ProcessingJobRepository,
	suffix string,
	hashCharacter string,
) (
	documentdomain.Document,
	documentdomain.ProcessingJob,
) {
	t.Helper()

	uniqueValue := time.Now().UnixNano()
	createdDocument, err := documentRepository.Create(
		ctx,
		documentdomain.CreateInput{
			OriginalName: fmt.Sprintf(
				"worker-integration-%s.pdf",
				suffix,
			),
			StoragePath: fmt.Sprintf(
				"integration-tests/worker-%s-%d.pdf",
				suffix,
				uniqueValue,
			),
			MIMEType:  "application/pdf",
			SizeBytes: 1024,
			SHA256:    strings.Repeat(hashCharacter, 64),
		},
	)
	if err != nil {
		t.Fatalf("create Worker integration document: %v", err)
	}

	createdJob, err := jobRepository.CreateProcessingJob(
		ctx,
		createdDocument.ID,
	)
	if err != nil {
		t.Fatalf("create Worker integration job: %v", err)
	}

	return createdDocument, createdJob
}

func assertWorkerIntegrationState(
	t *testing.T,
	ctx context.Context,
	documentRepository *postgresrepository.DocumentRepository,
	jobRepository *postgresrepository.ProcessingJobRepository,
	jobID int64,
	documentID int64,
	expectedJobStatus documentdomain.ProcessingJobStatus,
	expectedDocumentStatus documentdomain.Status,
	expectedErrorMessage *string,
) {
	t.Helper()

	foundJob, err := jobRepository.GetProcessingJobByID(ctx, jobID)
	if err != nil {
		t.Fatalf("get Worker integration job: %v", err)
	}
	if foundJob.Status != expectedJobStatus {
		t.Fatalf(
			"Worker integration job status = %q, want %q",
			foundJob.Status,
			expectedJobStatus,
		)
	}
	if foundJob.CompletedAt == nil {
		t.Fatal("Worker integration job completed_at must be set")
	}
	assertWorkerOptionalString(
		t,
		"Worker integration job error message",
		foundJob.ErrorMessage,
		expectedErrorMessage,
	)

	foundDocument, err := documentRepository.GetByID(
		ctx,
		documentID,
	)
	if err != nil {
		t.Fatalf("get Worker integration document: %v", err)
	}
	if foundDocument.Status != expectedDocumentStatus {
		t.Fatalf(
			"Worker integration document status = %q, want %q",
			foundDocument.Status,
			expectedDocumentStatus,
		)
	}
	assertWorkerOptionalString(
		t,
		"Worker integration document error message",
		foundDocument.ErrorMessage,
		expectedErrorMessage,
	)
}

func assertWorkerOptionalString(
	t *testing.T,
	name string,
	actual *string,
	expected *string,
) {
	t.Helper()

	if actual == nil && expected == nil {
		return
	}
	if actual == nil || expected == nil || *actual != *expected {
		t.Fatalf(
			"%s = %v, want %v",
			name,
			actual,
			expected,
		)
	}
}
