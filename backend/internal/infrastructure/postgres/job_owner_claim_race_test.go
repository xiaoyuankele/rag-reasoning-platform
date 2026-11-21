package postgres_test

import (
	"context"
	"errors"
	"fmt"
	"os"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"

	answer "rag-reasoning-platform/backend/internal/application/answer"
	document "rag-reasoning-platform/backend/internal/domain/document"
	embedding "rag-reasoning-platform/backend/internal/domain/embedding"
	repo "rag-reasoning-platform/backend/internal/infrastructure/postgres"
)

type claimJob func(*pgxpool.Pool) (int64, error)
type seedJob func(*ownedDocumentFixture, string) int64

func TestJobQueuesRecheckLockedOwnerCapacity(t *testing.T) {
	if os.Getenv("RUN_DATABASE_TESTS") != "1" {
		t.Skip("requires PostgreSQL")
	}
	for _, queue := range []struct {
		table, schedules string
		empty            error
		fixture          func(*testing.T, context.Context, *pgxpool.Pool, int) (seedJob, claimJob)
	}{
		{"answer_jobs", "answer_owner_schedules", answer.ErrNoQueuedAnswerJob, answerClaimFixture},
		{"document_jobs", "document_processing_owner_schedules", document.ErrNoQueuedProcessingJob, processingClaimFixture},
		{"embedding_jobs", "embedding_owner_schedules", embedding.ErrNoQueuedJob, embeddingClaimFixture},
	} {
		for _, borrow := range []int{1, 2} {
			t.Run(fmt.Sprintf("%s/base1-borrow%d", queue.table, borrow), func(t *testing.T) {
				ctx, cancel := context.WithTimeout(t.Context(), 20*time.Second)
				defer cancel()
				pool := openIsolatedDocumentTestPool(t, ctx)
				seed, claim := queue.fixture(t, ctx, pool, borrow)
				ownerA := newOwnedDocumentFixture(t, ctx, pool)
				first, second := seed(ownerA, "a1"), seed(ownerA, "a2")
				var other int64
				if borrow == 2 {
					seed(ownerA, "a3")
					other = seed(newOwnedDocumentFixture(t, ctx, pool), "b1")
				}

				late, lateErr := overlapJobClaims(t, ctx, pool, queue.schedules, claim, first)
				var processing int
				if err := pool.QueryRow(ctx, "SELECT count(*) FROM "+queue.table+" WHERE id IN ($1,$2) AND status='processing'", first, second).Scan(&processing); err != nil {
					t.Fatal(err)
				}
				if processing != 1 {
					t.Fatalf("owner A has %d committed processing jobs; want 1 (late claim id=%d err=%v)", processing, late, lateErr)
				}
				if borrow == 1 {
					if !errors.Is(lateErr, queue.empty) {
						t.Fatalf("late claim: got %v, want %v", lateErr, queue.empty)
					}
					return
				}
				if lateErr != nil || late != other {
					t.Fatalf("late claim=(%d, %v), want owner B's base slot for job %d", late, lateErr, other)
				}
				if id, err := claim(pool); err != nil || id != second {
					t.Fatalf("borrowed claim=(%d, %v), want owner A's job %d", id, err, second)
				}
				if _, err := claim(pool); !errors.Is(err, queue.empty) {
					t.Fatalf("claim beyond borrowed cap: got %v, want %v", err, queue.empty)
				}
			})
		}
	}
}

// A session-local view pauses the late claimer after its statement snapshot
// exists but before it locks a schedule. The other real claim commits first.
func overlapJobClaims(t *testing.T, ctx context.Context, pool *pgxpool.Pool, schedule string, claim claimJob, first int64) (int64, error) {
	t.Helper()
	cfg := pool.Config()
	cfg.MaxConns = 1
	latePool, err := pgxpool.NewWithConfig(ctx, cfg)
	if err != nil {
		t.Fatal(err)
	}
	defer latePool.Close()
	barrier, err := pool.Begin(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer barrier.Rollback(context.Background())
	var key int64
	if err := barrier.QueryRow(ctx, "SELECT $1::regclass::oid::bigint", schedule).Scan(&key); err != nil {
		t.Fatal(err)
	}
	if _, err := barrier.Exec(ctx, "SELECT pg_advisory_xact_lock($1)", key); err != nil {
		t.Fatal(err)
	}
	view := fmt.Sprintf("CREATE TEMP VIEW %s AS SELECT * FROM %s WHERE EXISTS (SELECT FROM pg_advisory_xact_lock(%d::bigint))", schedule, schedule, key)
	if _, err := latePool.Exec(ctx, view); err != nil {
		t.Fatal(err)
	}
	type result struct {
		id  int64
		err error
	}
	done := make(chan result, 1)
	go func() {
		id, err := claim(latePool)
		done <- result{id, err}
	}()
	for {
		var blocked bool
		err := barrier.QueryRow(ctx, `SELECT EXISTS (SELECT FROM pg_locks
			WHERE locktype='advisory' AND objid=$1::bigint::oid AND NOT granted)`, key).Scan(&blocked)
		if err != nil {
			t.Fatal(err)
		}
		if blocked {
			break
		}
		select {
		case early := <-done:
			t.Fatalf("claim returned before barrier: %v", early)
		case <-time.After(time.Millisecond):
		}
	}
	if id, err := claim(pool); err != nil || id != first {
		t.Fatalf("first claim=(%d, %v), want %d", id, err, first)
	}
	if err := barrier.Commit(ctx); err != nil {
		t.Fatal(err)
	}
	late := <-done
	return late.id, late.err
}

func answerClaimFixture(t *testing.T, ctx context.Context, pool *pgxpool.Pool, borrow int) (seedJob, claimJob) {
	newRepo := func(p *pgxpool.Pool) *repo.AnswerJobRepository {
		return repo.NewAnswerJobRepository(p, answer.JobAdmissionLimits{MaxQueuedJobsPerOwner: 3, MaxQueuedJobsGlobal: 4},
			answer.JobSchedulingPolicy{MaxInFlightPerOwner: 1, MaxBorrowedInFlightPerOwner: borrow, StarvationThreshold: time.Minute})
	}
	seed := func(owner *ownedDocumentFixture, name string) int64 {
		job, err := newRepo(pool).CreateAnswerJob(ctx, owner.scope, answer.Input{Query: name, TopK: 1, ResponseLanguage: answer.ResponseLanguageAuto})
		if err != nil {
			t.Fatal(err)
		}
		return job.ID
	}
	claim := func(p *pgxpool.Pool) (int64, error) {
		job, err := newRepo(p).ClaimNextAnswerJob(ctx)
		return job.ID, err
	}
	return seed, claim
}

func processingClaimFixture(t *testing.T, ctx context.Context, pool *pgxpool.Pool, borrow int) (seedJob, claimJob) {
	newRepo := func(p *pgxpool.Pool) *repo.ProcessingJobRepository {
		return repo.NewProcessingJobRepositoryWithSchedulingPolicy(p, document.ProcessingJobSchedulingPolicy{
			MaxInFlightPerOwner: 1, MaxBorrowedInFlightPerOwner: borrow, StarvationThreshold: time.Minute})
	}
	seed := func(owner *ownedDocumentFixture, name string) int64 {
		return createQueuedProcessingJob(t, ctx, owner, newRepo(pool), name, 1).ID
	}
	claim := func(p *pgxpool.Pool) (int64, error) {
		job, err := newRepo(p).ClaimNextProcessingJob(ctx)
		return job.ID, err
	}
	return seed, claim
}

func embeddingClaimFixture(t *testing.T, ctx context.Context, pool *pgxpool.Pool, borrow int) (seedJob, claimJob) {
	newRepo := func(p *pgxpool.Pool) *repo.EmbeddingJobRepository {
		return repo.NewEmbeddingJobRepositoryWithSchedulingPolicy(p, embedding.JobSchedulingPolicy{
			MaxInFlightPerOwner: 1, MaxBorrowedInFlightPerOwner: borrow, StarvationThreshold: time.Minute})
	}
	seed := func(owner *ownedDocumentFixture, name string) int64 {
		return createEmbeddingWorkerFixture(t, ctx, pool, owner, repo.NewChunkRepository(pool), newRepo(pool), name, []string{"sample"}).job.ID
	}
	claim := func(p *pgxpool.Pool) (int64, error) {
		job, err := newRepo(p).ClaimNextEmbeddingJob(ctx)
		return job.ID, err
	}
	return seed, claim
}
