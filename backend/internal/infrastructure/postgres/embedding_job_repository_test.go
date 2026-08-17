package postgres_test

import (
	"context"
	"errors"
	"fmt"
	"os"
	"reflect"
	"strings"
	"testing"
	"time"

	"rag-reasoning-platform/backend/internal/config"
	documentdomain "rag-reasoning-platform/backend/internal/domain/document"
	embeddingdomain "rag-reasoning-platform/backend/internal/domain/embedding"
	"rag-reasoning-platform/backend/internal/infrastructure/database"
	postgresrepository "rag-reasoning-platform/backend/internal/infrastructure/postgres"
	"rag-reasoning-platform/backend/migrations"
)

func TestEmbeddingJobRepositoryCreate(t *testing.T) {
	if os.Getenv("RUN_DATABASE_TESTS") != "1" {
		t.Skip("set RUN_DATABASE_TESTS=1 to run PostgreSQL integration tests")
	}

	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
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

	documentRepository := newOwnedDocumentFixture(t, ctx, pool)
	embeddingJobRepository := postgresrepository.NewEmbeddingJobRepository(pool)
	uniqueValue := time.Now().UnixNano()

	createdDocument, err := documentRepository.Create(
		ctx,
		documentdomain.CreateInput{
			OriginalName: "embedding-job-test.pdf",
			StoragePath: fmt.Sprintf(
				"integration-tests/embedding-job-%d.pdf",
				uniqueValue,
			),
			MIMEType:  "application/pdf",
			SizeBytes: 4096,
			SHA256:    strings.Repeat("a", 64),
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
		_, _ = pool.Exec(
			cleanupContext,
			"DELETE FROM documents WHERE id = $1",
			createdDocument.ID,
		)
	}()

	createdJob, err := embeddingJobRepository.CreateEmbeddingJob(
		ctx,
		createdDocument.ID,
		"text-embedding-3-small",
		1536,
	)
	if err != nil {
		t.Fatalf("create embedding job: %v", err)
	}

	if createdJob.ID <= 0 {
		t.Fatalf("embedding job ID = %d, want positive", createdJob.ID)
	}
	if createdJob.DocumentID != createdDocument.ID ||
		createdJob.ModelName != "text-embedding-3-small" ||
		createdJob.Dimensions != 1536 ||
		createdJob.Status != embeddingdomain.JobStatusQueued {
		t.Fatalf("created embedding job = %+v, want configured queued job", createdJob)
	}
	if createdJob.AttemptCount != 0 ||
		createdJob.ErrorMessage != nil ||
		createdJob.StartedAt != nil ||
		createdJob.CompletedAt != nil {
		t.Fatal("new queued embedding job contains execution state")
	}
	if createdJob.CreatedAt.IsZero() || createdJob.UpdatedAt.IsZero() {
		t.Fatal("new queued embedding job must contain database timestamps")
	}

	// 再通过查询端口读取刚创建的任务，验证 SELECT 列顺序和 Scan 映射完整。
	foundJob, err := embeddingJobRepository.GetEmbeddingJobByID(
		ctx,
		createdJob.ID,
	)
	if err != nil {
		t.Fatalf("get embedding job by ID: %v", err)
	}
	if !reflect.DeepEqual(foundJob, createdJob) {
		t.Fatalf(
			"found embedding job = %+v, want %+v",
			foundJob,
			createdJob,
		)
	}

	_, err = embeddingJobRepository.GetEmbeddingJobByID(
		ctx,
		createdJob.ID+1_000_000_000,
	)
	if !errors.Is(err, embeddingdomain.ErrJobNotFound) {
		t.Fatalf(
			"missing GetEmbeddingJobByID() error = %v, want ErrJobNotFound",
			err,
		)
	}

	_, err = embeddingJobRepository.CreateEmbeddingJob(
		ctx,
		createdDocument.ID,
		"another-model",
		768,
	)
	if !errors.Is(err, embeddingdomain.ErrActiveJobExists) {
		t.Fatalf(
			"duplicate CreateEmbeddingJob() error = %v, want ErrActiveJobExists",
			err,
		)
	}

	_, err = embeddingJobRepository.CreateEmbeddingJob(
		ctx,
		createdDocument.ID+999999,
		"text-embedding-3-small",
		1536,
	)
	if !errors.Is(err, documentdomain.ErrNotFound) {
		t.Fatalf(
			"missing document CreateEmbeddingJob() error = %v, want ErrNotFound",
			err,
		)
	}

	if err := documentRepository.Delete(ctx, createdDocument.ID); err != nil {
		t.Fatalf("delete document: %v", err)
	}

	var remainingJobs int
	if err := pool.QueryRow(
		ctx,
		"SELECT COUNT(*) FROM embedding_jobs WHERE document_id = $1",
		createdDocument.ID,
	).Scan(&remainingJobs); err != nil {
		t.Fatalf("count embedding jobs after document deletion: %v", err)
	}
	if remainingJobs != 0 {
		t.Fatalf(
			"remaining embedding jobs after document deletion = %d, want 0",
			remainingJobs,
		)
	}
}

func TestEmbeddingJobRepositoryRequeue(t *testing.T) {
	if os.Getenv("RUN_DATABASE_TESTS") != "1" {
		t.Skip("set RUN_DATABASE_TESTS=1 to run PostgreSQL integration tests")
	}

	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
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

	documentRepository := newOwnedDocumentFixture(t, ctx, pool)
	embeddingJobRepository := postgresrepository.NewEmbeddingJobRepository(pool)
	uniqueValue := time.Now().UnixNano()

	createdDocument, err := documentRepository.Create(
		ctx,
		documentdomain.CreateInput{
			OriginalName: "embedding-requeue-test.pdf",
			StoragePath: fmt.Sprintf(
				"integration-tests/embedding-requeue-%d.pdf",
				uniqueValue,
			),
			MIMEType:  "application/pdf",
			SizeBytes: 4096,
			SHA256:    strings.Repeat("b", 64),
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
		_, _ = pool.Exec(
			cleanupContext,
			"DELETE FROM documents WHERE id = $1",
			createdDocument.ID,
		)
	}()

	createdJob, err := embeddingJobRepository.CreateEmbeddingJob(
		ctx,
		createdDocument.ID,
		"text-embedding-3-small",
		1536,
	)
	if err != nil {
		t.Fatalf("create embedding job: %v", err)
	}

	// 本测试只验证 requeue。直接精确设置当前测试任务，避免领取到开发库中的其他 queued 任务。
	if _, err := pool.Exec(
		ctx,
		`UPDATE embedding_jobs
		SET
			status = 'processing',
			attempt_count = attempt_count + 1,
			started_at = CURRENT_TIMESTAMP,
			updated_at = CURRENT_TIMESTAMP
		WHERE id = $1`,
		createdJob.ID,
	); err != nil {
		t.Fatalf("arrange processing embedding job: %v", err)
	}

	nextAttemptAt := time.Now().UTC().Add(10 * time.Minute).Truncate(time.Microsecond)
	const retryReason = "embedding API temporarily unavailable"
	if err := embeddingJobRepository.RequeueEmbeddingJob(
		ctx,
		createdJob.ID,
		nextAttemptAt,
		retryReason,
	); err != nil {
		t.Fatalf("requeue embedding job: %v", err)
	}

	var (
		status        embeddingdomain.JobStatus
		attemptCount  int
		errorMessage  *string
		storedRetryAt time.Time
		startedAt     *time.Time
		completedAt   *time.Time
		promptTokens  *int
		totalTokens   *int
	)
	if err := pool.QueryRow(
		ctx,
		`SELECT
			status,
			attempt_count,
			error_message,
			next_attempt_at,
			started_at,
			completed_at,
			prompt_tokens,
			total_tokens
		FROM embedding_jobs
		WHERE id = $1`,
		createdJob.ID,
	).Scan(
		&status,
		&attemptCount,
		&errorMessage,
		&storedRetryAt,
		&startedAt,
		&completedAt,
		&promptTokens,
		&totalTokens,
	); err != nil {
		t.Fatalf("read requeued embedding job: %v", err)
	}

	if status != embeddingdomain.JobStatusQueued {
		t.Fatalf("requeued status = %q, want %q", status, embeddingdomain.JobStatusQueued)
	}
	if attemptCount != 1 {
		t.Fatalf("requeued attempt count = %d, want 1", attemptCount)
	}
	if errorMessage == nil || *errorMessage != retryReason {
		t.Fatalf("requeued error message = %v, want %q", errorMessage, retryReason)
	}
	if !storedRetryAt.Equal(nextAttemptAt) {
		t.Fatalf("requeued next attempt at = %v, want %v", storedRetryAt, nextAttemptAt)
	}
	if startedAt != nil || completedAt != nil ||
		promptTokens != nil || totalTokens != nil {
		t.Fatal("requeued job still contains transient execution state")
	}

	// 同一任务已经不再是 processing，重复收尾必须被拒绝，防止旧 Worker 覆盖新状态。
	err = embeddingJobRepository.RequeueEmbeddingJob(
		ctx,
		createdJob.ID,
		nextAttemptAt,
		retryReason,
	)
	if !errors.Is(err, embeddingdomain.ErrJobNotProcessing) {
		t.Fatalf("second requeue error = %v, want ErrJobNotProcessing", err)
	}
}
