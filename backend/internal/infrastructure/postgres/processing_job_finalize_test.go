package postgres_test

import (
	"context"
	"crypto/sha256"
	"errors"
	"fmt"
	"os"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"

	"rag-reasoning-platform/backend/internal/config"
	documentdomain "rag-reasoning-platform/backend/internal/domain/document"
	embeddingdomain "rag-reasoning-platform/backend/internal/domain/embedding"
	"rag-reasoning-platform/backend/internal/infrastructure/database"
	postgresrepository "rag-reasoning-platform/backend/internal/infrastructure/postgres"
	"rag-reasoning-platform/backend/migrations"
)

// TestProcessingJobRepositoryFinalize 使用真实 PostgreSQL 验证任务和文档
// 的成功、失败状态会在同一事务中完成更新。
func TestProcessingJobRepositoryFinalize(t *testing.T) {
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
	t.Cleanup(pool.Close)

	if err := database.Migrate(ctx, pool, migrations.Files); err != nil {
		t.Fatalf("migrate database: %v", err)
	}

	documentRepository := newOwnedDocumentFixture(t, ctx, pool)
	jobRepository := postgresrepository.NewProcessingJobRepository(pool)
	createdDocumentIDs := make([]int64, 0, 3)
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
					"clean up finalize test document %d: %v",
					documentID,
					cleanupErr,
				)
			}
		}
	}()

	t.Run("succeeded job makes document ready", func(t *testing.T) {
		detectedTitle := "Automatically detected title"
		createdDocument, createdJob := createProcessingJobForFinalization(
			t,
			ctx,
			pool,
			documentRepository,
			jobRepository,
			"success",
		)
		createdDocumentIDs = append(
			createdDocumentIDs,
			createdDocument.ID,
		)

		// 用户可以在解析尚未结束时先表达“完成后向量化”的意图。
		// 此时任务必须等待，不能被 Embedding Worker 提前领取。
		embeddingJobs := postgresrepository.NewScopedEmbeddingJobRepository(pool)
		waitingEmbeddingResult, err := embeddingJobs.RequestEmbeddingJob(
			ctx,
			documentRepository.scope,
			createdDocument.ID,
			"test-embedding-model",
			8,
		)
		if err != nil {
			t.Fatalf("request waiting embedding job: %v", err)
		}
		waitingEmbeddingJob := waitingEmbeddingResult.Job
		if waitingEmbeddingJob.Status != embeddingdomain.JobStatusWaitingDocument {
			t.Fatalf(
				"embedding job status before parsing success = %q, want %q",
				waitingEmbeddingJob.Status,
				embeddingdomain.JobStatusWaitingDocument,
			)
		}

		if err := jobRepository.MarkProcessingJobSucceeded(
			ctx,
			createdJob.ID,
			documentdomain.ProcessingCompletion{
				DetectedTitle: &detectedTitle,
			},
		); err != nil {
			t.Fatalf("mark processing job succeeded: %v", err)
		}

		assertFinalizedJobAndDocument(
			t,
			ctx,
			jobRepository,
			documentRepository,
			createdJob.ID,
			createdDocument.ID,
			documentdomain.ProcessingJobStatusSucceeded,
			documentdomain.StatusReady,
			nil,
			&detectedTitle,
		)

		activatedEmbeddingJob, err := embeddingJobs.GetEmbeddingJobByID(
			ctx,
			documentRepository.scope,
			waitingEmbeddingJob.ID,
		)
		if err != nil {
			t.Fatalf("get activated embedding job: %v", err)
		}
		if activatedEmbeddingJob.Status != embeddingdomain.JobStatusQueued {
			t.Fatalf(
				"embedding job status after parsing success = %q, want %q",
				activatedEmbeddingJob.Status,
				embeddingdomain.JobStatusQueued,
			)
		}

		err = jobRepository.MarkProcessingJobSucceeded(
			ctx,
			createdJob.ID,
			documentdomain.ProcessingCompletion{
				DetectedTitle: &detectedTitle,
			},
		)
		if !errors.Is(
			err,
			documentdomain.ErrProcessingJobNotProcessing,
		) {
			t.Fatalf(
				"second success finalization error = %v, want ErrProcessingJobNotProcessing",
				err,
			)
		}
	})

	t.Run("succeeded job preserves existing title", func(t *testing.T) {
		createdDocument, createdJob := createProcessingJobForFinalization(
			t,
			ctx,
			pool,
			documentRepository,
			jobRepository,
			"existing-title",
		)
		createdDocumentIDs = append(createdDocumentIDs, createdDocument.ID)

		existingTitle := "User confirmed title"
		if _, err := pool.Exec(
			ctx,
			"UPDATE documents SET title = $1 WHERE id = $2",
			existingTitle,
			createdDocument.ID,
		); err != nil {
			t.Fatalf("set existing document title: %v", err)
		}

		detectedTitle := "Different automatic title"
		if err := jobRepository.MarkProcessingJobSucceeded(
			ctx,
			createdJob.ID,
			documentdomain.ProcessingCompletion{
				DetectedTitle: &detectedTitle,
			},
		); err != nil {
			t.Fatalf("mark processing job succeeded: %v", err)
		}

		assertFinalizedJobAndDocument(
			t,
			ctx,
			jobRepository,
			documentRepository,
			createdJob.ID,
			createdDocument.ID,
			documentdomain.ProcessingJobStatusSucceeded,
			documentdomain.StatusReady,
			nil,
			&existingTitle,
		)
	})

	t.Run("failed job makes document failed", func(t *testing.T) {
		createdDocument, createdJob := createProcessingJobForFinalization(
			t,
			ctx,
			pool,
			documentRepository,
			jobRepository,
			"failure",
		)
		createdDocumentIDs = append(
			createdDocumentIDs,
			createdDocument.ID,
		)

		embeddingJobs := postgresrepository.NewScopedEmbeddingJobRepository(pool)
		waitingEmbeddingResult, err := embeddingJobs.RequestEmbeddingJob(
			ctx,
			documentRepository.scope,
			createdDocument.ID,
			"test-embedding-model",
			8,
		)
		if err != nil {
			t.Fatalf("request waiting embedding job: %v", err)
		}
		waitingEmbeddingJob := waitingEmbeddingResult.Job
		if waitingEmbeddingJob.Status != embeddingdomain.JobStatusWaitingDocument {
			t.Fatalf(
				"embedding job status before parsing failure = %q, want %q",
				waitingEmbeddingJob.Status,
				embeddingdomain.JobStatusWaitingDocument,
			)
		}

		safeErrorMessage := "document processing failed"
		if err := jobRepository.MarkProcessingJobFailed(
			ctx,
			createdJob.ID,
			safeErrorMessage,
		); err != nil {
			t.Fatalf("mark processing job failed: %v", err)
		}

		assertFinalizedJobAndDocument(
			t,
			ctx,
			jobRepository,
			documentRepository,
			createdJob.ID,
			createdDocument.ID,
			documentdomain.ProcessingJobStatusFailed,
			documentdomain.StatusFailed,
			&safeErrorMessage,
			nil,
		)

		stillWaitingJob, err := embeddingJobs.GetEmbeddingJobByID(
			ctx,
			documentRepository.scope,
			waitingEmbeddingJob.ID,
		)
		if err != nil {
			t.Fatalf("get embedding job after parsing failure: %v", err)
		}
		if stillWaitingJob.Status != embeddingdomain.JobStatusWaitingDocument {
			t.Fatalf(
				"embedding job status after parsing failure = %q, want %q",
				stillWaitingJob.Status,
				embeddingdomain.JobStatusWaitingDocument,
			)
		}

		err = jobRepository.MarkProcessingJobFailed(
			ctx,
			createdJob.ID,
			safeErrorMessage,
		)
		if !errors.Is(
			err,
			documentdomain.ErrProcessingJobNotProcessing,
		) {
			t.Fatalf(
				"second failure finalization error = %v, want ErrProcessingJobNotProcessing",
				err,
			)
		}
	})
}

func createProcessingJobForFinalization(
	t *testing.T,
	ctx context.Context,
	pool *pgxpool.Pool,
	documentRepository *ownedDocumentFixture,
	jobRepository *postgresrepository.ProcessingJobRepository,
	suffix string,
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
				"finalize-%s.pdf",
				suffix,
			),
			StoragePath: fmt.Sprintf(
				"integration-tests/finalize-%s-%d.pdf",
				suffix,
				uniqueValue,
			),
			MIMEType:  "application/pdf",
			SizeBytes: 1024,
			SHA256:    fmt.Sprintf("%x", sha256.Sum256([]byte(suffix))),
		},
	)
	if err != nil {
		t.Fatalf("create finalize test document: %v", err)
	}

	createdJob, err := jobRepository.CreateProcessingJob(
		ctx,
		createdDocument.ID,
	)
	if err != nil {
		t.Fatalf("create finalize test job: %v", err)
	}

	transaction, err := pool.Begin(ctx)
	if err != nil {
		t.Fatalf("begin finalize test preparation: %v", err)
	}
	defer func() {
		_ = transaction.Rollback(context.Background())
	}()

	if _, err := transaction.Exec(
		ctx,
		`
			UPDATE document_jobs
			SET
				status = 'processing',
				attempt_count = 1,
				started_at = CURRENT_TIMESTAMP,
				updated_at = CURRENT_TIMESTAMP
			WHERE id = $1
		`,
		createdJob.ID,
	); err != nil {
		t.Fatalf("prepare processing job: %v", err)
	}

	if _, err := transaction.Exec(
		ctx,
		`
			UPDATE documents
			SET
				status = 'processing',
				updated_at = CURRENT_TIMESTAMP
			WHERE id = $1
		`,
		createdDocument.ID,
	); err != nil {
		t.Fatalf("prepare processing document: %v", err)
	}

	if err := transaction.Commit(ctx); err != nil {
		t.Fatalf("commit finalize test preparation: %v", err)
	}

	return createdDocument, createdJob
}

func assertFinalizedJobAndDocument(
	t *testing.T,
	ctx context.Context,
	jobRepository *postgresrepository.ProcessingJobRepository,
	documentRepository *ownedDocumentFixture,
	jobID int64,
	documentID int64,
	expectedJobStatus documentdomain.ProcessingJobStatus,
	expectedDocumentStatus documentdomain.Status,
	expectedErrorMessage *string,
	expectedTitle *string,
) {
	t.Helper()

	foundJob, err := jobRepository.GetProcessingJobByID(ctx, jobID)
	if err != nil {
		t.Fatalf("get finalized processing job: %v", err)
	}
	if foundJob.Status != expectedJobStatus {
		t.Fatalf(
			"finalized job status = %q, want %q",
			foundJob.Status,
			expectedJobStatus,
		)
	}
	if foundJob.CompletedAt == nil || foundJob.CompletedAt.IsZero() {
		t.Fatal("finalized job must contain completed_at")
	}
	assertOptionalString(
		t,
		"finalized job error message",
		foundJob.ErrorMessage,
		expectedErrorMessage,
	)

	foundDocument, err := documentRepository.GetByID(
		ctx,
		documentID,
	)
	if err != nil {
		t.Fatalf("get finalized processing job document: %v", err)
	}
	if foundDocument.Status != expectedDocumentStatus {
		t.Fatalf(
			"finalized document status = %q, want %q",
			foundDocument.Status,
			expectedDocumentStatus,
		)
	}
	assertOptionalString(
		t,
		"finalized document error message",
		foundDocument.ErrorMessage,
		expectedErrorMessage,
	)
	assertOptionalString(
		t,
		"finalized document title",
		foundDocument.Title,
		expectedTitle,
	)
}

func assertOptionalString(
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
