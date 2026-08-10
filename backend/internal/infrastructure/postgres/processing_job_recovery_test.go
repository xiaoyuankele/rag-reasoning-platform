package postgres_test

import (
	"context"
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

// TestProcessingJobRepositoryRecoversInterruptedJobs 使用真实 PostgreSQL
// 验证启动恢复只处理 processing，并且任务和文档会保持一致。
func TestProcessingJobRepositoryRecoversInterruptedJobs(t *testing.T) {
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

	pool, err := database.Open(ctx, databaseConfig.ConnectionString())
	if err != nil {
		t.Fatalf("open database: %v", err)
	}
	defer pool.Close()

	if err := database.Migrate(ctx, pool, migrations.Files); err != nil {
		t.Fatalf("migrate database: %v", err)
	}

	// 恢复方法会处理全部 processing。为保护共享开发数据库，如果测试开始前
	// 已存在此类任务就跳过，避免修改并非由本测试创建的数据。
	var existingProcessingJobs int64
	if err := pool.QueryRow(
		ctx,
		"SELECT COUNT(*) FROM document_jobs WHERE status = 'processing'",
	).Scan(&existingProcessingJobs); err != nil {
		t.Fatalf("count existing processing jobs: %v", err)
	}
	if existingProcessingJobs != 0 {
		t.Skipf(
			"database contains %d existing processing jobs; preserve external data",
			existingProcessingJobs,
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
					"clean up recovery test document %d: %v",
					documentID,
					cleanupErr,
				)
			}
		}
	}()

	interruptedDocument, interruptedJob := createProcessingJobForFinalization(
		t,
		ctx,
		pool,
		documentRepository,
		jobRepository,
		"interrupted-recovery",
	)
	createdDocumentIDs = append(createdDocumentIDs, interruptedDocument.ID)

	uniqueValue := time.Now().UnixNano()
	queuedDocument, err := documentRepository.Create(
		ctx,
		documentdomain.CreateInput{
			OriginalName: "recovery-queued.md",
			StoragePath: fmt.Sprintf(
				"integration-tests/recovery-queued-%d.md",
				uniqueValue,
			),
			MIMEType:  "text/markdown",
			SizeBytes: 128,
			SHA256:    strings.Repeat("a", 64),
		},
	)
	if err != nil {
		t.Fatalf("create queued recovery test document: %v", err)
	}
	createdDocumentIDs = append(createdDocumentIDs, queuedDocument.ID)

	queuedJob, err := jobRepository.CreateProcessingJob(
		ctx,
		queuedDocument.ID,
	)
	if err != nil {
		t.Fatalf("create queued recovery test job: %v", err)
	}

	expectedMessage := "document processing was interrupted"
	recoveredCount, err := jobRepository.MarkInterruptedProcessingJobsFailed(
		ctx,
		expectedMessage,
	)
	if err != nil {
		t.Fatalf("recover interrupted processing jobs: %v", err)
	}
	if recoveredCount != 1 {
		t.Fatalf("recovered count = %d, want 1", recoveredCount)
	}

	assertFinalizedJobAndDocument(
		t,
		ctx,
		jobRepository,
		documentRepository,
		interruptedJob.ID,
		interruptedDocument.ID,
		documentdomain.ProcessingJobStatusFailed,
		documentdomain.StatusFailed,
		&expectedMessage,
		nil,
	)

	foundQueuedJob, err := jobRepository.GetProcessingJobByID(
		ctx,
		queuedJob.ID,
	)
	if err != nil {
		t.Fatalf("get queued recovery test job: %v", err)
	}
	if foundQueuedJob.Status != documentdomain.ProcessingJobStatusQueued {
		t.Fatalf(
			"queued job status = %q, want %q",
			foundQueuedJob.Status,
			documentdomain.ProcessingJobStatusQueued,
		)
	}
	if foundQueuedJob.CompletedAt != nil {
		t.Fatal("queued job completed_at must remain nil")
	}

	foundQueuedDocument, err := documentRepository.GetByID(
		ctx,
		queuedDocument.ID,
	)
	if err != nil {
		t.Fatalf("get queued recovery test document: %v", err)
	}
	if foundQueuedDocument.Status != documentdomain.StatusUploaded {
		t.Fatalf(
			"queued document status = %q, want %q",
			foundQueuedDocument.Status,
			documentdomain.StatusUploaded,
		)
	}

	secondRecoveredCount, err := jobRepository.MarkInterruptedProcessingJobsFailed(
		ctx,
		expectedMessage,
	)
	if err != nil {
		t.Fatalf("second recovery: %v", err)
	}
	if secondRecoveredCount != 0 {
		t.Fatalf(
			"second recovered count = %d, want 0",
			secondRecoveredCount,
		)
	}
}
