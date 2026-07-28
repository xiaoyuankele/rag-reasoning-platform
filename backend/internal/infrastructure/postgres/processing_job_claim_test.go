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

// TestProcessingJobRepositoryClaimNext 使用真实 PostgreSQL 验证：
// 1. 空队列是正常结果；
// 2. 最早任务会被优先领取；
// 3. 任务与文档会一起进入 processing；
// 4. 两个并发领取者不会得到同一任务。
func TestProcessingJobRepositoryClaimNext(t *testing.T) {
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

	// ClaimNextProcessingJob 是全局队列操作。为了不领取开发者手工创建的
	// 待处理任务，数据库已有 queued 记录时主动跳过本测试。
	var existingQueuedJobs int
	if err := pool.QueryRow(
		ctx,
		"SELECT COUNT(*) FROM document_jobs WHERE status = 'queued'",
	).Scan(&existingQueuedJobs); err != nil {
		t.Fatalf("count existing queued jobs: %v", err)
	}
	if existingQueuedJobs != 0 {
		t.Skipf(
			"database contains %d queued jobs; refusing to claim developer data",
			existingQueuedJobs,
		)
	}

	documentRepository := postgresrepository.NewDocumentRepository(pool)
	jobRepository := postgresrepository.NewProcessingJobRepository(pool)

	_, err = jobRepository.ClaimNextProcessingJob(ctx)
	if !errors.Is(err, documentdomain.ErrNoQueuedProcessingJob) {
		t.Fatalf(
			"empty ClaimNextProcessingJob() error = %v, want ErrNoQueuedProcessingJob",
			err,
		)
	}

	createdDocuments := make([]documentdomain.Document, 0, 3)
	createdJobs := make([]documentdomain.ProcessingJob, 0, 3)
	defer func() {
		cleanupContext, cleanupCancel := context.WithTimeout(
			context.Background(),
			5*time.Second,
		)
		defer cleanupCancel()

		for _, createdDocument := range createdDocuments {
			if _, cleanupErr := pool.Exec(
				cleanupContext,
				"DELETE FROM documents WHERE id = $1",
				createdDocument.ID,
			); cleanupErr != nil {
				t.Errorf(
					"clean up claim test document %d: %v",
					createdDocument.ID,
					cleanupErr,
				)
			}
		}
	}()

	uniquePrefix := time.Now().UnixNano()
	for index := 0; index < 3; index++ {
		createdDocument, err := documentRepository.Create(
			ctx,
			documentdomain.CreateInput{
				OriginalName: fmt.Sprintf(
					"claim-next-%d.pdf",
					index+1,
				),
				StoragePath: fmt.Sprintf(
					"integration-tests/claim-next-%d-%d.pdf",
					uniquePrefix,
					index+1,
				),
				MIMEType:  "application/pdf",
				SizeBytes: int64(index + 1),
				SHA256: strings.Repeat(
					string(rune('a'+index)),
					64,
				),
			},
		)
		if err != nil {
			t.Fatalf("create claim test document %d: %v", index+1, err)
		}
		createdDocuments = append(createdDocuments, createdDocument)

		createdJob, err := jobRepository.CreateProcessingJob(
			ctx,
			createdDocument.ID,
		)
		if err != nil {
			t.Fatalf("create claim test job %d: %v", index+1, err)
		}
		createdJobs = append(createdJobs, createdJob)
	}

	firstClaimedJob, err := jobRepository.ClaimNextProcessingJob(ctx)
	if err != nil {
		t.Fatalf("claim first processing job: %v", err)
	}
	if firstClaimedJob.ID != createdJobs[0].ID {
		t.Fatalf(
			"first claimed job ID = %d, want oldest job %d",
			firstClaimedJob.ID,
			createdJobs[0].ID,
		)
	}
	assertClaimedProcessingJob(t, firstClaimedJob)

	// 同时释放两个 goroutine，模拟两个 Worker 并发领取剩余任务。
	type claimResult struct {
		job documentdomain.ProcessingJob
		err error
	}

	start := make(chan struct{})
	results := make(chan claimResult, 2)
	for workerIndex := 0; workerIndex < 2; workerIndex++ {
		go func() {
			<-start
			claimedJob, claimErr :=
				jobRepository.ClaimNextProcessingJob(ctx)
			results <- claimResult{
				job: claimedJob,
				err: claimErr,
			}
		}()
	}
	close(start)

	concurrentlyClaimedIDs := make(map[int64]struct{}, 2)
	for resultIndex := 0; resultIndex < 2; resultIndex++ {
		result := <-results
		if result.err != nil {
			t.Fatalf(
				"concurrent ClaimNextProcessingJob() error: %v",
				result.err,
			)
		}
		assertClaimedProcessingJob(t, result.job)
		concurrentlyClaimedIDs[result.job.ID] = struct{}{}
	}

	if len(concurrentlyClaimedIDs) != 2 {
		t.Fatalf(
			"concurrent claims returned %d distinct jobs, want 2",
			len(concurrentlyClaimedIDs),
		)
	}
	for _, expectedJob := range createdJobs[1:] {
		if _, exists := concurrentlyClaimedIDs[expectedJob.ID]; !exists {
			t.Fatalf(
				"concurrent claims did not return job %d",
				expectedJob.ID,
			)
		}
	}

	for _, createdDocument := range createdDocuments {
		foundDocument, err := documentRepository.GetByID(
			ctx,
			createdDocument.ID,
		)
		if err != nil {
			t.Fatalf(
				"get claimed job document %d: %v",
				createdDocument.ID,
				err,
			)
		}
		if foundDocument.Status != documentdomain.StatusProcessing {
			t.Fatalf(
				"document %d status = %q, want %q",
				foundDocument.ID,
				foundDocument.Status,
				documentdomain.StatusProcessing,
			)
		}
	}

	_, err = jobRepository.ClaimNextProcessingJob(ctx)
	if !errors.Is(err, documentdomain.ErrNoQueuedProcessingJob) {
		t.Fatalf(
			"drained ClaimNextProcessingJob() error = %v, want ErrNoQueuedProcessingJob",
			err,
		)
	}
}

func assertClaimedProcessingJob(
	t *testing.T,
	job documentdomain.ProcessingJob,
) {
	t.Helper()

	if job.Status != documentdomain.ProcessingJobStatusProcessing {
		t.Fatalf(
			"claimed job %d status = %q, want %q",
			job.ID,
			job.Status,
			documentdomain.ProcessingJobStatusProcessing,
		)
	}
	if job.AttemptCount != 1 {
		t.Fatalf(
			"claimed job %d attempt count = %d, want 1",
			job.ID,
			job.AttemptCount,
		)
	}
	if job.ErrorMessage != nil {
		t.Fatalf(
			"claimed job %d error message = %q, want nil",
			job.ID,
			*job.ErrorMessage,
		)
	}
	if job.StartedAt == nil || job.StartedAt.IsZero() {
		t.Fatalf(
			"claimed job %d must contain started_at",
			job.ID,
		)
	}
	if job.CompletedAt != nil {
		t.Fatalf(
			"claimed job %d completed_at must be nil",
			job.ID,
		)
	}
}
