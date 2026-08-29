package integration_test

import (
	"context"
	"fmt"
	"os"
	"sync"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"

	answerapplication "rag-reasoning-platform/backend/internal/application/answer"
	documentapplication "rag-reasoning-platform/backend/internal/application/document"
	accessdomain "rag-reasoning-platform/backend/internal/domain/access"
	postgresrepository "rag-reasoning-platform/backend/internal/infrastructure/postgres"
)

const (
	answerJobConcurrencyUsers         = 100
	answerJobConcurrencyJobsPerUser   = 2
	answerJobConcurrencyWorkers       = 10
	answerJobConcurrencyOwnerBorrowed = 2
)

// recordingConcurrentAnswerer 是零远程费用 Fake：它只模拟一次短暂问答执行，
// 同时记录全局、单用户并发和每个唯一问题的执行次数。
type recordingConcurrentAnswerer struct {
	mu             sync.Mutex
	active         int
	maximum        int
	ownerActive    map[int64]int
	ownerMaximum   map[int64]int
	queryCallCount map[string]int
	delay          time.Duration
}

func newRecordingConcurrentAnswerer(delay time.Duration) *recordingConcurrentAnswerer {
	return &recordingConcurrentAnswerer{
		ownerActive:    make(map[int64]int),
		ownerMaximum:   make(map[int64]int),
		queryCallCount: make(map[string]int),
		delay:          delay,
	}
}

func (a *recordingConcurrentAnswerer) Answer(
	ctx context.Context,
	scope accessdomain.OwnerScope,
	input answerapplication.Input,
) (answerapplication.Output, error) {
	ownerID := scope.OwnerUserID()
	a.mu.Lock()
	a.active++
	a.ownerActive[ownerID]++
	a.queryCallCount[input.Query]++
	a.maximum = max(a.maximum, a.active)
	a.ownerMaximum[ownerID] = max(
		a.ownerMaximum[ownerID],
		a.ownerActive[ownerID],
	)
	a.mu.Unlock()

	defer func() {
		a.mu.Lock()
		a.active--
		a.ownerActive[ownerID]--
		a.mu.Unlock()
	}()

	timer := time.NewTimer(a.delay)
	defer timer.Stop()
	select {
	case <-ctx.Done():
		return answerapplication.Output{}, ctx.Err()
	case <-timer.C:
		return answerapplication.Output{
			Query:            input.Query,
			Answer:           "zero-cost concurrency answer",
			ResponseLanguage: answerapplication.ResponseLanguageEnglish,
			Sources:          []answerapplication.Source{},
			PromptTokens:     3,
			CompletionTokens: 2,
			TotalTokens:      5,
		}, nil
	}
}

func (a *recordingConcurrentAnswerer) snapshot() (
	maximum int,
	ownerMaximum map[int64]int,
	queryCallCount map[string]int,
) {
	a.mu.Lock()
	defer a.mu.Unlock()
	ownerMaximum = make(map[int64]int, len(a.ownerMaximum))
	for ownerID, value := range a.ownerMaximum {
		ownerMaximum[ownerID] = value
	}
	queryCallCount = make(map[string]int, len(a.queryCallCount))
	for query, value := range a.queryCallCount {
		queryCallCount[query] = value
	}
	return a.maximum, ownerMaximum, queryCallCount
}

type blockingAnswerer struct {
	started chan struct{}
	once    sync.Once
}

func (a *blockingAnswerer) Answer(
	ctx context.Context,
	_ accessdomain.OwnerScope,
	_ answerapplication.Input,
) (answerapplication.Output, error) {
	a.once.Do(func() { close(a.started) })
	<-ctx.Done()
	return answerapplication.Output{}, ctx.Err()
}

type discardAnswerJobObserver struct{}

func (discardAnswerJobObserver) ObserveAnswerJobEvent(
	context.Context,
	answerapplication.JobEvent,
) {
}

// TestAnswerJobWorkerPoolHandlesOneHundredOwnersWithoutRemoteCalls 验证真实
// PostgreSQL Owner 公平队列和真实 WorkerPool 在 100 用户场景下的并发边界。
func TestAnswerJobWorkerPoolHandlesOneHundredOwnersWithoutRemoteCalls(t *testing.T) {
	if os.Getenv("RUN_DATABASE_TESTS") != "1" {
		t.Skip("set RUN_DATABASE_TESTS=1 to run PostgreSQL integration tests")
	}

	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()
	pool := openDocumentOwnerIntegrationPool(t, ctx)
	ownerIDs := insertAnswerJobConcurrencyUsers(
		t,
		ctx,
		pool,
		answerJobConcurrencyUsers,
	)
	cleanupAnswerJobConcurrencyUsers(t, pool, ownerIDs)

	repository := newAnswerJobConcurrencyRepository(pool)
	jobIDs := make([]int64, 0, answerJobConcurrencyUsers*answerJobConcurrencyJobsPerUser)
	for ownerIndex, ownerID := range ownerIDs {
		scope, err := accessdomain.NewOwnerScope(ownerID)
		if err != nil {
			t.Fatalf("create owner scope %d: %v", ownerID, err)
		}
		for jobIndex := range answerJobConcurrencyJobsPerUser {
			job, err := repository.CreateAnswerJob(
				ctx,
				scope,
				answerapplication.Input{
					Query: fmt.Sprintf(
						"zero-cost-owner-%d-job-%d",
						ownerIndex,
						jobIndex,
					),
					TopK:             5,
					ResponseLanguage: answerapplication.ResponseLanguageEnglish,
				},
			)
			if err != nil {
				t.Fatalf("create owner %d job %d: %v", ownerIndex, jobIndex, err)
			}
			jobIDs = append(jobIDs, job.ID)
		}
	}

	answerer := newRecordingConcurrentAnswerer(20 * time.Millisecond)
	worker := newZeroCostAnswerJobWorker(t, repository, answerer)
	workerErrors := make(chan error, 1)
	loop, err := documentapplication.NewWorkerLoop(
		worker,
		5*time.Millisecond,
		func(err error) {
			select {
			case workerErrors <- err:
			default:
			}
		},
	)
	if err != nil {
		t.Fatalf("NewWorkerLoop() error = %v", err)
	}
	workerPool, err := documentapplication.NewWorkerPool(
		loop,
		answerJobConcurrencyWorkers,
	)
	if err != nil {
		t.Fatalf("NewWorkerPool() error = %v", err)
	}

	workerContext, cancelWorkers := context.WithCancel(ctx)
	workerPoolReturned := make(chan struct{})
	go func() {
		workerPool.Run(workerContext)
		close(workerPoolReturned)
	}()
	waitForAnswerJobQueueToDrain(t, ctx, repository)
	cancelWorkers()
	select {
	case <-workerPoolReturned:
	case <-ctx.Done():
		t.Fatalf("worker pool did not stop: %v", ctx.Err())
	}
	select {
	case err := <-workerErrors:
		t.Fatalf("worker loop reported error: %v", err)
	default:
	}

	maximum, ownerMaximum, queryCallCount := answerer.snapshot()
	if maximum < 2 || maximum > answerJobConcurrencyWorkers {
		t.Fatalf(
			"maximum global answer concurrency = %d, want 2..%d",
			maximum,
			answerJobConcurrencyWorkers,
		)
	}
	maximumOwnerConcurrency := 0
	for ownerID, value := range ownerMaximum {
		maximumOwnerConcurrency = max(maximumOwnerConcurrency, value)
		if value > answerJobConcurrencyOwnerBorrowed {
			t.Fatalf(
				"owner %d maximum concurrency = %d, limit %d",
				ownerID,
				value,
				answerJobConcurrencyOwnerBorrowed,
			)
		}
	}
	t.Logf(
		"zero-cost answer regression: owners=%d jobs=%d workers=%d max_global=%d max_owner=%d",
		answerJobConcurrencyUsers,
		len(jobIDs),
		answerJobConcurrencyWorkers,
		maximum,
		maximumOwnerConcurrency,
	)
	expectedJobCount := answerJobConcurrencyUsers * answerJobConcurrencyJobsPerUser
	if len(queryCallCount) != expectedJobCount {
		t.Fatalf("unique executed queries = %d, want %d", len(queryCallCount), expectedJobCount)
	}
	for query, count := range queryCallCount {
		if count != 1 {
			t.Fatalf("query %q executed %d times, want exactly once", query, count)
		}
	}
	assertAnswerJobTerminalCounts(t, ctx, pool, jobIDs, expectedJobCount)
}

// TestAnswerJobWorkerShutdownLeavesRecoverableJob 验证 shutdown 不会把中断
// 伪装成业务失败；启动恢复服务随后把遗留 processing 放回 queued。
func TestAnswerJobWorkerShutdownLeavesRecoverableJob(t *testing.T) {
	if os.Getenv("RUN_DATABASE_TESTS") != "1" {
		t.Skip("set RUN_DATABASE_TESTS=1 to run PostgreSQL integration tests")
	}

	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	pool := openDocumentOwnerIntegrationPool(t, ctx)
	ownerIDs := insertAnswerJobConcurrencyUsers(t, ctx, pool, 1)
	cleanupAnswerJobConcurrencyUsers(t, pool, ownerIDs)
	scope, err := accessdomain.NewOwnerScope(ownerIDs[0])
	if err != nil {
		t.Fatalf("create owner scope: %v", err)
	}
	repository := newAnswerJobConcurrencyRepository(pool)
	job, err := repository.CreateAnswerJob(
		ctx,
		scope,
		answerapplication.Input{
			Query:            "shutdown recovery question",
			TopK:             5,
			ResponseLanguage: answerapplication.ResponseLanguageEnglish,
		},
	)
	if err != nil {
		t.Fatalf("create shutdown fixture: %v", err)
	}

	answerer := &blockingAnswerer{started: make(chan struct{})}
	worker := newZeroCostAnswerJobWorker(t, repository, answerer)
	workerContext, cancelWorker := context.WithCancel(ctx)
	workerReturned := make(chan struct{})
	go func() {
		_, _ = worker.RunOnce(workerContext)
		close(workerReturned)
	}()
	select {
	case <-answerer.started:
	case <-ctx.Done():
		t.Fatalf("answer worker did not start: %v", ctx.Err())
	}
	cancelWorker()
	select {
	case <-workerReturned:
	case <-ctx.Done():
		t.Fatalf("answer worker did not stop: %v", ctx.Err())
	}

	stored, err := repository.GetAnswerJobByID(ctx, scope, job.ID)
	if err != nil || stored.Status != answerapplication.JobStatusProcessing {
		t.Fatalf("interrupted job = %+v, %v, want processing", stored, err)
	}
	// 正常 shutdown 后不能立即抢走任务；只有租约真正到期才允许恢复。
	if _, err := pool.Exec(
		ctx,
		`UPDATE answer_jobs
		 SET lease_expires_at = CURRENT_TIMESTAMP - INTERVAL '1 second'
		 WHERE id = $1`,
		job.ID,
	); err != nil {
		t.Fatalf("expire interrupted answer job lease: %v", err)
	}
	recovery, err := answerapplication.NewExpiredJobRecoveryService(repository)
	if err != nil {
		t.Fatalf("NewExpiredJobRecoveryService() error = %v", err)
	}
	recoveredCount, err := recovery.Recover(ctx)
	if err != nil || recoveredCount != 1 {
		t.Fatalf("Recover() = %d, %v, want 1 nil", recoveredCount, err)
	}
	stored, err = repository.GetAnswerJobByID(ctx, scope, job.ID)
	if err != nil || stored.Status != answerapplication.JobStatusQueued {
		t.Fatalf("recovered job = %+v, %v, want queued", stored, err)
	}
	if _, err := repository.CancelAnswerJob(ctx, scope, job.ID); err != nil {
		t.Fatalf("cancel recovered fixture: %v", err)
	}
}

func newAnswerJobConcurrencyRepository(
	pool *pgxpool.Pool,
) *postgresrepository.AnswerJobRepository {
	return postgresrepository.NewAnswerJobRepository(
		pool,
		answerapplication.JobAdmissionLimits{
			MaxQueuedJobsPerOwner: 5,
			MaxQueuedJobsGlobal:   500,
		},
		answerapplication.JobSchedulingPolicy{
			MaxInFlightPerOwner:         1,
			MaxBorrowedInFlightPerOwner: answerJobConcurrencyOwnerBorrowed,
			StarvationThreshold:         30 * time.Second,
		},
	)
}

func newZeroCostAnswerJobWorker(
	t *testing.T,
	repository *postgresrepository.AnswerJobRepository,
	answerer interface {
		Answer(
			context.Context,
			accessdomain.OwnerScope,
			answerapplication.Input,
		) (answerapplication.Output, error)
	},
) *answerapplication.JobWorker {
	t.Helper()
	retryPolicy, err := answerapplication.NewJobRetryPolicy(
		3,
		time.Second,
		10*time.Second,
	)
	if err != nil {
		t.Fatalf("NewJobRetryPolicy() error = %v", err)
	}
	worker, err := answerapplication.NewJobWorker(
		repository,
		answerer,
		discardAnswerJobObserver{},
		5*time.Second,
		retryPolicy,
	)
	if err != nil {
		t.Fatalf("NewJobWorker() error = %v", err)
	}
	return worker
}

func insertAnswerJobConcurrencyUsers(
	t *testing.T,
	ctx context.Context,
	pool *pgxpool.Pool,
	count int,
) []int64 {
	t.Helper()
	suffix := fmt.Sprint(time.Now().UnixNano())
	rows, err := pool.Query(
		ctx,
		`
			INSERT INTO users (
				email, email_verified_at, display_name, password_hash
			)
			SELECT
				'answer-concurrency-' || $1 || '-' || series || '@example.com',
				CURRENT_TIMESTAMP,
				'Answer Concurrency User',
				'$argon2id$integration-test'
			FROM generate_series(1, $2) AS series
			RETURNING id
		`,
		suffix,
		count,
	)
	if err != nil {
		t.Fatalf("insert answer concurrency users: %v", err)
	}
	defer rows.Close()
	ownerIDs := make([]int64, 0, count)
	for rows.Next() {
		var ownerID int64
		if err := rows.Scan(&ownerID); err != nil {
			t.Fatalf("scan answer concurrency owner: %v", err)
		}
		ownerIDs = append(ownerIDs, ownerID)
	}
	if err := rows.Err(); err != nil {
		t.Fatalf("iterate answer concurrency owners: %v", err)
	}
	if len(ownerIDs) != count {
		t.Fatalf("inserted owners = %d, want %d", len(ownerIDs), count)
	}
	return ownerIDs
}

func cleanupAnswerJobConcurrencyUsers(
	t *testing.T,
	pool *pgxpool.Pool,
	ownerIDs []int64,
) {
	t.Helper()
	t.Cleanup(func() {
		cleanupContext, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()
		if _, err := pool.Exec(
			cleanupContext,
			"DELETE FROM users WHERE id = ANY($1::bigint[])",
			ownerIDs,
		); err != nil {
			t.Errorf("clean up answer concurrency users: %v", err)
		}
	})
}

func waitForAnswerJobQueueToDrain(
	t *testing.T,
	ctx context.Context,
	repository *postgresrepository.AnswerJobRepository,
) {
	t.Helper()
	ticker := time.NewTicker(10 * time.Millisecond)
	defer ticker.Stop()
	for {
		stats, err := repository.GetAnswerJobQueueStats(ctx)
		if err != nil {
			t.Fatalf("get answer queue stats: %v", err)
		}
		if stats.QueuedCount == 0 && stats.ProcessingCount == 0 {
			return
		}
		select {
		case <-ctx.Done():
			t.Fatalf("answer job queue did not drain: stats=%+v error=%v", stats, ctx.Err())
		case <-ticker.C:
		}
	}
}

func assertAnswerJobTerminalCounts(
	t *testing.T,
	ctx context.Context,
	pool *pgxpool.Pool,
	jobIDs []int64,
	want int,
) {
	t.Helper()
	var succeededCount int
	var distinctCount int
	var attemptCount int
	if err := pool.QueryRow(
		ctx,
		`
			SELECT
				COUNT(*) FILTER (WHERE status = 'succeeded'),
				COUNT(DISTINCT id),
				COUNT(*) FILTER (WHERE attempt_count = 1)
			FROM answer_jobs
			WHERE id = ANY($1::bigint[])
		`,
		jobIDs,
	).Scan(&succeededCount, &distinctCount, &attemptCount); err != nil {
		t.Fatalf("count answer job terminals: %v", err)
	}
	if succeededCount != want || distinctCount != want || attemptCount != want {
		t.Fatalf(
			"answer terminals succeeded/distinct/first-attempt = %d/%d/%d, want %d/%d/%d",
			succeededCount,
			distinctCount,
			attemptCount,
			want,
			want,
			want,
		)
	}
}
