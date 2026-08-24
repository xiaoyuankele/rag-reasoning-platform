package postgres_test

import (
	"context"
	"errors"
	"os"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"

	"rag-reasoning-platform/backend/internal/config"
	embeddingdomain "rag-reasoning-platform/backend/internal/domain/embedding"
	"rag-reasoning-platform/backend/internal/infrastructure/database"
	postgresrepository "rag-reasoning-platform/backend/internal/infrastructure/postgres"
	"rag-reasoning-platform/backend/migrations"
)

// TestEmbeddingJobRepositoryUsesOwnerFairScheduling 使用真实 PostgreSQL
// 验证向量任务的基础公平、空闲借用、借用上限、槽位释放和防饥饿规则。
// 整个测试只操作数据库，不会创建 Embedder，也不会调用远程模型。
func TestEmbeddingJobRepositoryUsesOwnerFairScheduling(t *testing.T) {
	ctx, pool := openEmbeddingFairClaimTestDatabase(t)
	refuseToClaimDeveloperEmbeddingJobs(t, ctx, pool)

	policy := embeddingdomain.JobSchedulingPolicy{
		MaxInFlightPerOwner:         1,
		MaxBorrowedInFlightPerOwner: 2,
		StarvationThreshold:         2 * time.Minute,
	}
	jobRepository :=
		postgresrepository.NewEmbeddingJobRepositoryWithSchedulingPolicy(
			pool,
			policy,
		)
	chunkRepository := postgresrepository.NewChunkRepository(pool)

	t.Run("different owners receive base capacity before borrowing", func(t *testing.T) {
		ownerA := newOwnedDocumentFixture(t, ctx, pool)
		ownerB := newOwnedDocumentFixture(t, ctx, pool)

		ownerAFirst := createEmbeddingWorkerFixture(
			t, ctx, pool, ownerA, chunkRepository, jobRepository,
			"fair-owner-a-first", []string{"owner A first chunk"},
		)
		ownerASecond := createEmbeddingWorkerFixture(
			t, ctx, pool, ownerA, chunkRepository, jobRepository,
			"fair-owner-a-second", []string{"owner A second chunk"},
		)
		ownerBFirst := createEmbeddingWorkerFixture(
			t, ctx, pool, ownerB, chunkRepository, jobRepository,
			"fair-owner-b-first", []string{"owner B first chunk"},
		)

		setEmbeddingJobEligibility(
			t, ctx, pool, ownerAFirst.job.ID, 30*time.Second,
		)
		setEmbeddingJobEligibility(
			t, ctx, pool, ownerASecond.job.ID, 20*time.Second,
		)
		setEmbeddingJobEligibility(
			t, ctx, pool, ownerBFirst.job.ID, 10*time.Second,
		)

		firstClaim := mustClaimEmbeddingJob(t, ctx, jobRepository)
		if firstClaim.ID != ownerAFirst.job.ID {
			t.Fatalf(
				"first claimed job ID = %d, want oldest owner A job %d",
				firstClaim.ID,
				ownerAFirst.job.ID,
			)
		}

		// Owner A 已经使用基础槽位，因此第二次必须先服务 Owner B，
		// 不能因为 A 的第二条任务更早就让 A 连续占满两个 Worker。
		secondClaim := mustClaimEmbeddingJob(t, ctx, jobRepository)
		if secondClaim.ID != ownerBFirst.job.ID {
			t.Fatalf(
				"second claimed job ID = %d, want owner B job %d",
				secondClaim.ID,
				ownerBFirst.job.ID,
			)
		}

		// 已经没有其他 Owner 等待基础槽位，A 才可以借用第二个槽位。
		thirdClaim := mustClaimEmbeddingJob(t, ctx, jobRepository)
		if thirdClaim.ID != ownerASecond.job.ID {
			t.Fatalf(
				"third claimed job ID = %d, want owner A second job %d",
				thirdClaim.ID,
				ownerASecond.job.ID,
			)
		}

		for _, job := range []embeddingdomain.Job{
			firstClaim,
			secondClaim,
			thirdClaim,
		} {
			failClaimedEmbeddingJobForCleanup(t, ctx, jobRepository, job)
		}
	})

	t.Run("borrow limit blocks then released slot resumes queue", func(t *testing.T) {
		owner := newOwnedDocumentFixture(t, ctx, pool)
		fixtures := make([]embeddingWorkerFixture, 0, 3)
		for index := 0; index < 3; index++ {
			fixture := createEmbeddingWorkerFixture(
				t,
				ctx,
				pool,
				owner,
				chunkRepository,
				jobRepository,
				[]string{
					"borrow-owner-first",
					"borrow-owner-second",
					"borrow-owner-third",
				}[index],
				[]string{"borrow limit chunk"},
			)
			fixtures = append(fixtures, fixture)
			setEmbeddingJobEligibility(
				t,
				ctx,
				pool,
				fixture.job.ID,
				time.Duration(30-index*10)*time.Second,
			)
		}

		firstClaim := mustClaimEmbeddingJob(t, ctx, jobRepository)
		secondClaim := mustClaimEmbeddingJob(t, ctx, jobRepository)
		if firstClaim.ID != fixtures[0].job.ID ||
			secondClaim.ID != fixtures[1].job.ID {
			t.Fatalf(
				"claimed IDs = [%d %d], want [%d %d]",
				firstClaim.ID,
				secondClaim.ID,
				fixtures[0].job.ID,
				fixtures[1].job.ID,
			)
		}

		_, err := jobRepository.ClaimNextEmbeddingJob(ctx)
		if !errors.Is(err, embeddingdomain.ErrNoQueuedJob) {
			t.Fatalf(
				"claim above borrowed limit error = %v, want ErrNoQueuedJob",
				err,
			)
		}

		// 一条 processing 任务结束后，实时 processing 计数下降，第三条
		// queued 任务即可借用刚释放的槽位，不需要维护额外计数列。
		failClaimedEmbeddingJobForCleanup(
			t, ctx, jobRepository, firstClaim,
		)
		thirdClaim := mustClaimEmbeddingJob(t, ctx, jobRepository)
		if thirdClaim.ID != fixtures[2].job.ID {
			t.Fatalf(
				"claim after release ID = %d, want %d",
				thirdClaim.ID,
				fixtures[2].job.ID,
			)
		}

		failClaimedEmbeddingJobForCleanup(
			t, ctx, jobRepository, secondClaim,
		)
		failClaimedEmbeddingJobForCleanup(
			t, ctx, jobRepository, thirdClaim,
		)
	})

	t.Run("starved owner beats recent retry", func(t *testing.T) {
		starvedOwner := newOwnedDocumentFixture(t, ctx, pool)
		retryOwner := newOwnedDocumentFixture(t, ctx, pool)

		starvedFixture := createEmbeddingWorkerFixture(
			t, ctx, pool, starvedOwner, chunkRepository, jobRepository,
			"starved-embedding-owner", []string{"starved owner chunk"},
		)
		retryFixture := createEmbeddingWorkerFixture(
			t, ctx, pool, retryOwner, chunkRepository, jobRepository,
			"recent-retry-owner", []string{"recent retry chunk"},
		)

		// B 的任务虽然创建得更早，但刚结束 next_attempt_at 退避；A 已经
		// 真正可执行并等待三分钟。公平等待必须从“可执行时间”计算。
		if _, err := pool.Exec(
			ctx,
			`UPDATE embedding_jobs
			 SET created_at = CURRENT_TIMESTAMP - INTERVAL '3 minutes',
			     next_attempt_at = CURRENT_TIMESTAMP - INTERVAL '3 minutes'
			 WHERE id = $1`,
			starvedFixture.job.ID,
		); err != nil {
			t.Fatalf("age starved embedding job: %v", err)
		}
		if _, err := pool.Exec(
			ctx,
			`UPDATE embedding_jobs
			 SET created_at = CURRENT_TIMESTAMP - INTERVAL '10 minutes',
			     next_attempt_at = CURRENT_TIMESTAMP
			 WHERE id = $1`,
			retryFixture.job.ID,
		); err != nil {
			t.Fatalf("set recently eligible retry: %v", err)
		}
		if _, err := pool.Exec(
			ctx,
			`UPDATE embedding_owner_schedules
			 SET last_dispatched_at = CURRENT_TIMESTAMP,
			     updated_at = CURRENT_TIMESTAMP
			 WHERE owner_user_id = $1`,
			starvedOwner.scope.OwnerUserID(),
		); err != nil {
			t.Fatalf("set starved owner dispatch cursor: %v", err)
		}

		firstClaim := mustClaimEmbeddingJob(t, ctx, jobRepository)
		if firstClaim.ID != starvedFixture.job.ID {
			t.Fatalf(
				"first claimed job ID = %d, want starved job %d",
				firstClaim.ID,
				starvedFixture.job.ID,
			)
		}
		failClaimedEmbeddingJobForCleanup(
			t, ctx, jobRepository, firstClaim,
		)

		secondClaim := mustClaimEmbeddingJob(t, ctx, jobRepository)
		if secondClaim.ID != retryFixture.job.ID {
			t.Fatalf(
				"second claimed job ID = %d, want retry job %d",
				secondClaim.ID,
				retryFixture.job.ID,
			)
		}
		failClaimedEmbeddingJobForCleanup(
			t, ctx, jobRepository, secondClaim,
		)
	})
}

// TestEmbeddingJobRepositoryConcurrentClaimsUseDifferentOwners 验证两个
// Embedding Worker 同时领取时会锁定不同 Owner，且不会重复领取同一任务。
func TestEmbeddingJobRepositoryConcurrentClaimsUseDifferentOwners(
	t *testing.T,
) {
	ctx, pool := openEmbeddingFairClaimTestDatabase(t)
	refuseToClaimDeveloperEmbeddingJobs(t, ctx, pool)

	jobRepository := postgresrepository.NewEmbeddingJobRepository(pool)
	chunkRepository := postgresrepository.NewChunkRepository(pool)
	ownerA := newOwnedDocumentFixture(t, ctx, pool)
	ownerB := newOwnedDocumentFixture(t, ctx, pool)
	fixtureA := createEmbeddingWorkerFixture(
		t, ctx, pool, ownerA, chunkRepository, jobRepository,
		"concurrent-fair-owner-a", []string{"concurrent A chunk"},
	)
	fixtureB := createEmbeddingWorkerFixture(
		t, ctx, pool, ownerB, chunkRepository, jobRepository,
		"concurrent-fair-owner-b", []string{"concurrent B chunk"},
	)

	type claimResult struct {
		job embeddingdomain.Job
		err error
	}
	start := make(chan struct{})
	results := make(chan claimResult, 2)
	for workerIndex := 0; workerIndex < 2; workerIndex++ {
		go func() {
			<-start
			job, err := jobRepository.ClaimNextEmbeddingJob(ctx)
			results <- claimResult{job: job, err: err}
		}()
	}
	close(start)

	claimedByID := make(map[int64]embeddingdomain.Job, 2)
	for resultIndex := 0; resultIndex < 2; resultIndex++ {
		result := <-results
		if result.err != nil {
			t.Fatalf("concurrent embedding claim error: %v", result.err)
		}
		claimedByID[result.job.ID] = result.job
	}

	if len(claimedByID) != 2 {
		t.Fatalf(
			"concurrent claims returned %d distinct jobs, want 2",
			len(claimedByID),
		)
	}
	for _, expectedJob := range []embeddingdomain.Job{
		fixtureA.job,
		fixtureB.job,
	} {
		claimedJob, exists := claimedByID[expectedJob.ID]
		if !exists {
			t.Fatalf(
				"concurrent claims did not return expected job %d",
				expectedJob.ID,
			)
		}
		failClaimedEmbeddingJobForCleanup(
			t, ctx, jobRepository, claimedJob,
		)
	}
}

func TestEmbeddingJobRepositoryRejectsInvalidSchedulingPolicy(t *testing.T) {
	repository :=
		postgresrepository.NewEmbeddingJobRepositoryWithSchedulingPolicy(
			nil,
			embeddingdomain.JobSchedulingPolicy{
				MaxInFlightPerOwner:         2,
				MaxBorrowedInFlightPerOwner: 1,
				StarvationThreshold:         time.Minute,
			},
		)

	_, err := repository.ClaimNextEmbeddingJob(context.Background())
	if !errors.Is(err, embeddingdomain.ErrInvalidJobSchedulingPolicy) {
		t.Fatalf(
			"ClaimNextEmbeddingJob() error = %v, want ErrInvalidJobSchedulingPolicy",
			err,
		)
	}
}

func openEmbeddingFairClaimTestDatabase(
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
	if err := database.RefreshVectorTypes(ctx, pool); err != nil {
		t.Fatalf("refresh pgvector types: %v", err)
	}
	return ctx, pool
}

func refuseToClaimDeveloperEmbeddingJobs(
	t *testing.T,
	ctx context.Context,
	pool *pgxpool.Pool,
) {
	t.Helper()

	var dueQueuedJobs int
	if err := pool.QueryRow(
		ctx,
		`SELECT COUNT(*)
		 FROM embedding_jobs AS job
		 JOIN documents AS source_document
		   ON source_document.id = job.document_id
		 WHERE job.status = 'queued'
		   AND job.next_attempt_at <= CURRENT_TIMESTAMP
		   AND source_document.status = 'ready'`,
	).Scan(&dueQueuedJobs); err != nil {
		t.Fatalf("count existing due embedding jobs: %v", err)
	}
	if dueQueuedJobs != 0 {
		t.Skipf(
			"database contains %d due embedding jobs; refusing to claim developer data",
			dueQueuedJobs,
		)
	}
}

func setEmbeddingJobEligibility(
	t *testing.T,
	ctx context.Context,
	pool *pgxpool.Pool,
	jobID int64,
	age time.Duration,
) {
	t.Helper()

	eligibleAt := time.Now().Add(-age)
	if _, err := pool.Exec(
		ctx,
		`UPDATE embedding_jobs
		 SET created_at = $2,
		     next_attempt_at = $2
		 WHERE id = $1`,
		jobID,
		eligibleAt,
	); err != nil {
		t.Fatalf("set embedding job %d eligibility: %v", jobID, err)
	}
}

func mustClaimEmbeddingJob(
	t *testing.T,
	ctx context.Context,
	repository *postgresrepository.EmbeddingJobRepository,
) embeddingdomain.Job {
	t.Helper()

	job, err := repository.ClaimNextEmbeddingJob(ctx)
	if err != nil {
		t.Fatalf("claim embedding job: %v", err)
	}
	if job.Status != embeddingdomain.JobStatusProcessing {
		t.Fatalf(
			"claimed job %d status = %q, want %q",
			job.ID,
			job.Status,
			embeddingdomain.JobStatusProcessing,
		)
	}
	if job.StartedAt == nil || job.StartedAt.IsZero() {
		t.Fatalf("claimed job %d must contain started_at", job.ID)
	}
	return job
}

func failClaimedEmbeddingJobForCleanup(
	t *testing.T,
	ctx context.Context,
	repository *postgresrepository.EmbeddingJobRepository,
	job embeddingdomain.Job,
) {
	t.Helper()

	if err := repository.MarkEmbeddingJobFailed(
		ctx,
		job.ID,
		"owner-fair embedding scheduling integration test cleanup",
	); err != nil {
		t.Fatalf("finalize claimed embedding job %d: %v", job.ID, err)
	}
}
