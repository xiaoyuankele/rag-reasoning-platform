package postgres_test

import (
	"context"
	"errors"
	"fmt"
	"os"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"

	"rag-reasoning-platform/backend/internal/config"
	documentdomain "rag-reasoning-platform/backend/internal/domain/document"
	"rag-reasoning-platform/backend/internal/infrastructure/database"
	postgresrepository "rag-reasoning-platform/backend/internal/infrastructure/postgres"
	"rag-reasoning-platform/backend/migrations"
)

// TestProcessingJobRepositoryClaimNextUsesOwnerFairScheduling 使用真实
// PostgreSQL 验证 Owner 公平调度的完整行为：
//  1. 有多个等待用户时，每个用户先获得一个基础槽位；
//  2. 没有其他用户可获得基础槽位时，繁忙用户可以借用第二个槽位；
//  3. 借用已达到绝对上限时，剩余任务继续排队而不是无限占用 Worker；
//  4. Worker 完成任务释放槽位后，排队任务可以继续被领取。
func TestProcessingJobRepositoryClaimNextUsesOwnerFairScheduling(
	t *testing.T,
) {
	ctx, pool := openProcessingJobClaimTestDatabase(t)
	refuseToClaimDeveloperQueuedJobs(t, ctx, pool)

	policy := documentdomain.ProcessingJobSchedulingPolicy{
		MaxInFlightPerOwner:         1,
		MaxBorrowedInFlightPerOwner: 2,
		StarvationThreshold:         2 * time.Minute,
	}
	jobRepository :=
		postgresrepository.NewProcessingJobRepositoryWithSchedulingPolicy(
			pool,
			policy,
		)

	t.Run("empty queue", func(t *testing.T) {
		_, err := jobRepository.ClaimNextProcessingJob(ctx)
		if !errors.Is(err, documentdomain.ErrNoQueuedProcessingJob) {
			t.Fatalf(
				"ClaimNextProcessingJob() error = %v, want ErrNoQueuedProcessingJob",
				err,
			)
		}
	})

	t.Run("different owners receive base capacity before borrowing", func(t *testing.T) {
		ownerA := newOwnedDocumentFixture(t, ctx, pool)
		ownerB := newOwnedDocumentFixture(t, ctx, pool)

		ownerAFirst := createQueuedProcessingJob(
			t, ctx, ownerA, jobRepository, "owner-a-first", 1,
		)
		ownerASecond := createQueuedProcessingJob(
			t, ctx, ownerA, jobRepository, "owner-a-second", 2,
		)
		ownerBFirst := createQueuedProcessingJob(
			t, ctx, ownerB, jobRepository, "owner-b-first", 1,
		)

		firstClaim, err := jobRepository.ClaimNextProcessingJob(ctx)
		if err != nil {
			t.Fatalf("claim first owner A job: %v", err)
		}
		if firstClaim.ID != ownerAFirst.ID {
			t.Fatalf(
				"first claimed job ID = %d, want oldest owner A job %d",
				firstClaim.ID,
				ownerAFirst.ID,
			)
		}

		// Owner A 已使用一个基础槽位。即使 A 的第二个任务更早，
		// 调度器也必须先让尚未获得服务的 Owner B 使用基础槽位。
		secondClaim, err := jobRepository.ClaimNextProcessingJob(ctx)
		if err != nil {
			t.Fatalf("claim owner B base-capacity job: %v", err)
		}
		if secondClaim.ID != ownerBFirst.ID {
			t.Fatalf(
				"second claimed job ID = %d, want owner B job %d",
				secondClaim.ID,
				ownerBFirst.ID,
			)
		}

		// 此时已经没有其他 Owner 等待基础槽位，因此 Owner A 可以借用
		// 第二个槽位，避免 Worker 在低负载时被公平规则白白闲置。
		thirdClaim, err := jobRepository.ClaimNextProcessingJob(ctx)
		if err != nil {
			t.Fatalf("claim owner A borrowed-capacity job: %v", err)
		}
		if thirdClaim.ID != ownerASecond.ID {
			t.Fatalf(
				"third claimed job ID = %d, want owner A second job %d",
				thirdClaim.ID,
				ownerASecond.ID,
			)
		}

		for _, claimedJob := range []documentdomain.ProcessingJob{
			firstClaim,
			secondClaim,
			thirdClaim,
		} {
			assertClaimedProcessingJob(t, claimedJob)
			failClaimedProcessingJobForCleanup(
				t,
				ctx,
				jobRepository,
				claimedJob,
			)
		}
	})

	t.Run("borrow limit blocks then released slot resumes queue", func(t *testing.T) {
		owner := newOwnedDocumentFixture(t, ctx, pool)
		createdJobs := make([]documentdomain.ProcessingJob, 0, 3)
		for index := 0; index < 3; index++ {
			createdJobs = append(
				createdJobs,
				createQueuedProcessingJob(
					t,
					ctx,
					owner,
					jobRepository,
					fmt.Sprintf("borrow-limit-%d", index+1),
					index+1,
				),
			)
		}

		firstClaim, err := jobRepository.ClaimNextProcessingJob(ctx)
		if err != nil {
			t.Fatalf("claim base-capacity job: %v", err)
		}
		secondClaim, err := jobRepository.ClaimNextProcessingJob(ctx)
		if err != nil {
			t.Fatalf("claim borrowed-capacity job: %v", err)
		}
		if firstClaim.ID != createdJobs[0].ID ||
			secondClaim.ID != createdJobs[1].ID {
			t.Fatalf(
				"claimed job IDs = [%d %d], want [%d %d]",
				firstClaim.ID,
				secondClaim.ID,
				createdJobs[0].ID,
				createdJobs[1].ID,
			)
		}

		_, err = jobRepository.ClaimNextProcessingJob(ctx)
		if !errors.Is(err, documentdomain.ErrNoQueuedProcessingJob) {
			t.Fatalf(
				"claim above borrowed limit error = %v, want ErrNoQueuedProcessingJob",
				err,
			)
		}

		// 第一条任务完成后，在途数从 2 降为 1；第三条任务此时可以再次
		// 借用第二个槽位。这里验证的是“完成即释放”，不需要额外计数表。
		failClaimedProcessingJobForCleanup(
			t,
			ctx,
			jobRepository,
			firstClaim,
		)
		thirdClaim, err := jobRepository.ClaimNextProcessingJob(ctx)
		if err != nil {
			t.Fatalf("claim after releasing owner slot: %v", err)
		}
		if thirdClaim.ID != createdJobs[2].ID {
			t.Fatalf(
				"claim after release ID = %d, want %d",
				thirdClaim.ID,
				createdJobs[2].ID,
			)
		}

		failClaimedProcessingJobForCleanup(
			t,
			ctx,
			jobRepository,
			secondClaim,
		)
		failClaimedProcessingJobForCleanup(
			t,
			ctx,
			jobRepository,
			thirdClaim,
		)
	})

	t.Run("starved owner wins before recent owner", func(t *testing.T) {
		starvedOwner := newOwnedDocumentFixture(t, ctx, pool)
		recentOwner := newOwnedDocumentFixture(t, ctx, pool)

		starvedJob := createQueuedProcessingJob(
			t, ctx, starvedOwner, jobRepository, "starved-owner", 1,
		)
		recentJob := createQueuedProcessingJob(
			t, ctx, recentOwner, jobRepository, "recent-owner", 1,
		)

		// 人工构造可重复的时间线：Owner A 已等待超过阈值，但其最近派发
		// 时间反而更新；Owner B 刚进入队列且从未派发。防饥饿优先级应当
		// 压过普通的 last_dispatched_at 排序。
		if _, err := pool.Exec(
			ctx,
			`UPDATE document_jobs
			 SET created_at = CURRENT_TIMESTAMP - INTERVAL '10 minutes'
			 WHERE id = $1`,
			starvedJob.ID,
		); err != nil {
			t.Fatalf("age starved processing job: %v", err)
		}
		if _, err := pool.Exec(
			ctx,
			`UPDATE document_processing_owner_schedules
			 SET last_dispatched_at = CURRENT_TIMESTAMP,
			     updated_at = CURRENT_TIMESTAMP
			 WHERE owner_user_id = $1`,
			starvedOwner.scope.OwnerUserID(),
		); err != nil {
			t.Fatalf("set starved owner dispatch cursor: %v", err)
		}

		firstClaim, err := jobRepository.ClaimNextProcessingJob(ctx)
		if err != nil {
			t.Fatalf("claim starved owner job: %v", err)
		}
		if firstClaim.ID != starvedJob.ID {
			t.Fatalf(
				"first claimed job ID = %d, want starved job %d",
				firstClaim.ID,
				starvedJob.ID,
			)
		}
		failClaimedProcessingJobForCleanup(
			t, ctx, jobRepository, firstClaim,
		)

		secondClaim, err := jobRepository.ClaimNextProcessingJob(ctx)
		if err != nil {
			t.Fatalf("claim recent owner job: %v", err)
		}
		if secondClaim.ID != recentJob.ID {
			t.Fatalf(
				"second claimed job ID = %d, want recent job %d",
				secondClaim.ID,
				recentJob.ID,
			)
		}
		failClaimedProcessingJobForCleanup(
			t, ctx, jobRepository, secondClaim,
		)
	})
}

// TestProcessingJobRepositoryConcurrentClaimsUseDifferentOwners 验证两个
// Worker 同时领取时会锁定不同 Owner，而不是把两个基础槽位都给同一用户。
func TestProcessingJobRepositoryConcurrentClaimsUseDifferentOwners(
	t *testing.T,
) {
	ctx, pool := openProcessingJobClaimTestDatabase(t)
	refuseToClaimDeveloperQueuedJobs(t, ctx, pool)

	jobRepository := postgresrepository.NewProcessingJobRepository(pool)
	ownerA := newOwnedDocumentFixture(t, ctx, pool)
	ownerB := newOwnedDocumentFixture(t, ctx, pool)
	jobA := createQueuedProcessingJob(
		t, ctx, ownerA, jobRepository, "concurrent-owner-a", 1,
	)
	jobB := createQueuedProcessingJob(
		t, ctx, ownerB, jobRepository, "concurrent-owner-b", 1,
	)

	type claimResult struct {
		job documentdomain.ProcessingJob
		err error
	}

	start := make(chan struct{})
	results := make(chan claimResult, 2)
	for workerIndex := 0; workerIndex < 2; workerIndex++ {
		go func() {
			<-start
			claimedJob, err := jobRepository.ClaimNextProcessingJob(ctx)
			results <- claimResult{job: claimedJob, err: err}
		}()
	}
	close(start)

	claimedByID := make(map[int64]documentdomain.ProcessingJob, 2)
	for resultIndex := 0; resultIndex < 2; resultIndex++ {
		result := <-results
		if result.err != nil {
			t.Fatalf("concurrent claim error: %v", result.err)
		}
		assertClaimedProcessingJob(t, result.job)
		claimedByID[result.job.ID] = result.job
	}

	if len(claimedByID) != 2 {
		t.Fatalf(
			"concurrent claims returned %d distinct jobs, want 2",
			len(claimedByID),
		)
	}
	for _, expectedJob := range []documentdomain.ProcessingJob{jobA, jobB} {
		claimedJob, exists := claimedByID[expectedJob.ID]
		if !exists {
			t.Fatalf(
				"concurrent claims did not return expected job %d",
				expectedJob.ID,
			)
		}
		failClaimedProcessingJobForCleanup(
			t, ctx, jobRepository, claimedJob,
		)
	}
}

func TestProcessingJobRepositoryRejectsInvalidSchedulingPolicy(
	t *testing.T,
) {
	repository :=
		postgresrepository.NewProcessingJobRepositoryWithSchedulingPolicy(
			nil,
			documentdomain.ProcessingJobSchedulingPolicy{
				MaxInFlightPerOwner:         2,
				MaxBorrowedInFlightPerOwner: 1,
				StarvationThreshold:         time.Minute,
			},
		)

	_, err := repository.ClaimNextProcessingJob(context.Background())
	if !errors.Is(
		err,
		documentdomain.ErrInvalidProcessingJobSchedulingPolicy,
	) {
		t.Fatalf(
			"ClaimNextProcessingJob() error = %v, want ErrInvalidProcessingJobSchedulingPolicy",
			err,
		)
	}
}

func openProcessingJobClaimTestDatabase(
	t *testing.T,
) (context.Context, *pgxpool.Pool) {
	t.Helper()

	if os.Getenv("RUN_DATABASE_TESTS") != "1" {
		t.Skip("set RUN_DATABASE_TESTS=1 to run PostgreSQL integration tests")
	}

	ctx, cancel := context.WithTimeout(
		context.Background(),
		60*time.Second,
	)
	t.Cleanup(cancel)

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

	return ctx, pool
}

func refuseToClaimDeveloperQueuedJobs(
	t *testing.T,
	ctx context.Context,
	pool *pgxpool.Pool,
) {
	t.Helper()

	// ClaimNextProcessingJob 是全局队列操作。为避免领取开发者手工创建的
	// 正式任务，只要数据库已有 queued 记录就主动跳过集成测试。
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
}

func createQueuedProcessingJob(
	t *testing.T,
	ctx context.Context,
	owner *ownedDocumentFixture,
	jobRepository *postgresrepository.ProcessingJobRepository,
	label string,
	sequence int,
) documentdomain.ProcessingJob {
	t.Helper()

	uniqueValue := time.Now().UnixNano() + int64(sequence)
	createdDocument, err := owner.Create(
		ctx,
		documentdomain.CreateInput{
			OriginalName: fmt.Sprintf("%s.pdf", label),
			StoragePath: fmt.Sprintf(
				"integration-tests/%s-%d.pdf",
				label,
				uniqueValue,
			),
			MIMEType:  "application/pdf",
			SizeBytes: int64(sequence),
			SHA256:    fmt.Sprintf("%064x", uniqueValue),
		},
	)
	if err != nil {
		t.Fatalf("create %s document: %v", label, err)
	}

	createdJob, err := jobRepository.CreateProcessingJob(
		ctx,
		createdDocument.ID,
	)
	if err != nil {
		t.Fatalf("create %s processing job: %v", label, err)
	}
	return createdJob
}

func failClaimedProcessingJobForCleanup(
	t *testing.T,
	ctx context.Context,
	jobRepository *postgresrepository.ProcessingJobRepository,
	job documentdomain.ProcessingJob,
) {
	t.Helper()

	if err := jobRepository.MarkProcessingJobFailed(
		ctx,
		job.ID,
		documentdomain.ProcessingFailure{
			Message: "owner scheduling integration test cleanup",
			Metrics: documentdomain.ProcessingExecutionMetrics{
				FileBytes: job.DocumentID,
			},
		},
	); err != nil {
		t.Fatalf("finalize claimed processing job %d: %v", job.ID, err)
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
		t.Fatalf("claimed job %d must contain started_at", job.ID)
	}
	if job.CompletedAt != nil {
		t.Fatalf("claimed job %d completed_at must be nil", job.ID)
	}
}
