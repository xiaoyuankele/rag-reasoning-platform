package postgres_test

import (
	"context"
	"errors"
	"os"
	"testing"
	"time"

	documentdomain "rag-reasoning-platform/backend/internal/domain/document"
	postgresrepository "rag-reasoning-platform/backend/internal/infrastructure/postgres"
)

// TestScopedProcessingJobRepositoryCancellation 验证取消状态边界、Owner 隔离、
// 幂等性，以及取消与 Worker 领取并发竞争时只产生一个合法结果。
func TestScopedProcessingJobRepositoryCancellation(t *testing.T) {
	if os.Getenv("RUN_DATABASE_TESTS") != "1" {
		t.Skip("set RUN_DATABASE_TESTS=1 to run PostgreSQL integration tests")
	}

	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	pool := openIsolatedDocumentTestPool(t, ctx)
	documents := newOwnedDocumentFixture(t, ctx, pool)
	jobs := postgresrepository.NewScopedProcessingJobRepository(
		pool,
		testProcessingAdmissionLimits(),
	)

	t.Run("queued job can be canceled idempotently", func(t *testing.T) {
		createdDocument, err := documents.Create(ctx, scopedDocumentInput(
			"cancel-processing-queued.pdf",
			"processing-cancel/queued.pdf",
			"a",
		))
		if err != nil {
			t.Fatalf("create queued document: %v", err)
		}
		createdJob, err := jobs.CreateProcessingJob(
			ctx,
			documents.scope,
			createdDocument.ID,
		)
		if err != nil {
			t.Fatalf("create queued processing job: %v", err)
		}

		canceledJob, err := jobs.CancelProcessingJob(
			ctx,
			documents.scope,
			createdJob.ID,
		)
		if err != nil {
			t.Fatalf("cancel queued processing job: %v", err)
		}
		if canceledJob.Status != documentdomain.ProcessingJobStatusCanceled ||
			canceledJob.CompletedAt == nil {
			t.Fatalf("canceled job = %+v, want canceled with completed_at", canceledJob)
		}
		if canceledJob.StartedAt != nil || canceledJob.ErrorMessage != nil {
			t.Fatalf("canceled queued job retained execution state: %+v", canceledJob)
		}

		repeatedJob, err := jobs.CancelProcessingJob(
			ctx,
			documents.scope,
			createdJob.ID,
		)
		if err != nil {
			t.Fatalf("repeat processing cancellation: %v", err)
		}
		if repeatedJob.ID != canceledJob.ID ||
			repeatedJob.Status != documentdomain.ProcessingJobStatusCanceled {
			t.Fatalf("repeat cancellation = %+v, want original canceled job", repeatedJob)
		}

		foundDocument, err := documents.GetByID(ctx, createdDocument.ID)
		if err != nil {
			t.Fatalf("get document after cancellation: %v", err)
		}
		if foundDocument.Status != documentdomain.StatusUploaded {
			t.Fatalf("document status = %q, want uploaded", foundDocument.Status)
		}
	})

	t.Run("processing and terminal jobs cannot be canceled", func(t *testing.T) {
		statuses := []struct {
			name    string
			status  documentdomain.ProcessingJobStatus
			wantErr error
		}{
			{name: "processing", status: documentdomain.ProcessingJobStatusProcessing, wantErr: documentdomain.ErrProcessingJobProcessingCannotCancel},
			{name: "succeeded", status: documentdomain.ProcessingJobStatusSucceeded, wantErr: documentdomain.ErrProcessingJobTerminalCannotCancel},
			{name: "failed", status: documentdomain.ProcessingJobStatusFailed, wantErr: documentdomain.ErrProcessingJobTerminalCannotCancel},
		}

		for index, test := range statuses {
			t.Run(test.name, func(t *testing.T) {
				createdDocument, err := documents.Create(ctx, scopedDocumentInput(
					"cancel-processing-"+test.name+".pdf",
					"processing-cancel/"+test.name+".pdf",
					string(rune('b'+index)),
				))
				if err != nil {
					t.Fatalf("create document: %v", err)
				}
				createdJob, err := jobs.CreateProcessingJob(ctx, documents.scope, createdDocument.ID)
				if err != nil {
					t.Fatalf("create processing job: %v", err)
				}
				if _, err := pool.Exec(
					ctx,
					`UPDATE document_jobs
					 SET status = $2,
					     started_at = CASE WHEN $2 = 'processing' THEN CURRENT_TIMESTAMP ELSE NULL END,
					     completed_at = CASE WHEN $2 IN ('succeeded', 'failed') THEN CURRENT_TIMESTAMP ELSE NULL END
					 WHERE id = $1`,
					createdJob.ID,
					test.status,
				); err != nil {
					t.Fatalf("arrange job status %s: %v", test.status, err)
				}

				_, err = jobs.CancelProcessingJob(ctx, documents.scope, createdJob.ID)
				if !errors.Is(err, test.wantErr) {
					t.Fatalf("CancelProcessingJob(%s) error = %v, want %v", test.status, err, test.wantErr)
				}
			})
		}
	})

	t.Run("another owner sees not found", func(t *testing.T) {
		otherDocuments := newOwnedDocumentFixture(t, ctx, pool)
		createdDocument, err := documents.Create(ctx, scopedDocumentInput(
			"cancel-processing-owner.pdf",
			"processing-cancel/owner.pdf",
			"e",
		))
		if err != nil {
			t.Fatalf("create owner document: %v", err)
		}
		createdJob, err := jobs.CreateProcessingJob(ctx, documents.scope, createdDocument.ID)
		if err != nil {
			t.Fatalf("create owner processing job: %v", err)
		}

		_, err = jobs.CancelProcessingJob(ctx, otherDocuments.scope, createdJob.ID)
		if !errors.Is(err, documentdomain.ErrProcessingJobNotFound) {
			t.Fatalf("other owner cancellation error = %v, want not found", err)
		}
		if _, err := jobs.CancelProcessingJob(
			ctx,
			documents.scope,
			createdJob.ID,
		); err != nil {
			t.Fatalf("cancel cross-owner fixture with real owner: %v", err)
		}
	})

	t.Run("cancel and worker claim have one winner", func(t *testing.T) {
		createdDocument, err := documents.Create(ctx, scopedDocumentInput(
			"cancel-processing-race.pdf",
			"processing-cancel/race.pdf",
			"f",
		))
		if err != nil {
			t.Fatalf("create race document: %v", err)
		}
		createdJob, err := jobs.CreateProcessingJob(ctx, documents.scope, createdDocument.ID)
		if err != nil {
			t.Fatalf("create race processing job: %v", err)
		}

		workerRepository := postgresrepository.NewProcessingJobRepository(pool)
		start := make(chan struct{})
		type operationResult struct {
			job documentdomain.ProcessingJob
			err error
		}
		cancelResult := make(chan operationResult, 1)
		claimResult := make(chan operationResult, 1)
		go func() {
			<-start
			job, cancelErr := jobs.CancelProcessingJob(ctx, documents.scope, createdJob.ID)
			cancelResult <- operationResult{job: job, err: cancelErr}
		}()
		go func() {
			<-start
			job, claimErr := workerRepository.ClaimNextProcessingJob(ctx)
			claimResult <- operationResult{job: job, err: claimErr}
		}()
		close(start)

		cancellation := <-cancelResult
		claim := <-claimResult
		switch {
		case cancellation.err == nil:
			if cancellation.job.ID != createdJob.ID {
				t.Fatalf("canceled job ID = %d, want %d", cancellation.job.ID, createdJob.ID)
			}
			if !errors.Is(claim.err, documentdomain.ErrNoQueuedProcessingJob) {
				t.Fatalf("cancel won but claim = (%+v, %v), want no queued job", claim.job, claim.err)
			}
		case claim.err == nil:
			if claim.job.ID != createdJob.ID {
				t.Fatalf("claimed job ID = %d, want race job %d", claim.job.ID, createdJob.ID)
			}
			if !errors.Is(cancellation.err, documentdomain.ErrProcessingJobProcessingCannotCancel) {
				t.Fatalf("claim won but cancel error = %v, want processing conflict", cancellation.err)
			}
		default:
			t.Fatalf("race has no winner: cancel error=%v claim error=%v", cancellation.err, claim.err)
		}

		foundJob, err := jobs.GetProcessingJobByID(ctx, documents.scope, createdJob.ID)
		if err != nil {
			t.Fatalf("get race job: %v", err)
		}
		if foundJob.Status != documentdomain.ProcessingJobStatusCanceled &&
			foundJob.Status != documentdomain.ProcessingJobStatusProcessing {
			t.Fatalf("race job status = %q, want canceled or processing", foundJob.Status)
		}
	})
}

func TestScopedProcessingJobRepositoryFindsLatestJobsByOwner(t *testing.T) {
	if os.Getenv("RUN_DATABASE_TESTS") != "1" {
		t.Skip("set RUN_DATABASE_TESTS=1 to run PostgreSQL integration tests")
	}

	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	pool := openIsolatedDocumentTestPool(t, ctx)
	documents := newOwnedDocumentFixture(t, ctx, pool)
	otherDocuments := newOwnedDocumentFixture(t, ctx, pool)
	jobs := postgresrepository.NewScopedProcessingJobRepository(
		pool,
		testProcessingAdmissionLimits(),
	)

	firstDocument, err := documents.Create(ctx, scopedDocumentInput(
		"processing-latest-first.pdf", "processing-latest/first.pdf", "1",
	))
	if err != nil {
		t.Fatalf("create first document: %v", err)
	}
	secondDocument, err := documents.Create(ctx, scopedDocumentInput(
		"processing-latest-second.pdf", "processing-latest/second.pdf", "2",
	))
	if err != nil {
		t.Fatalf("create second document: %v", err)
	}
	otherDocument, err := otherDocuments.Create(ctx, scopedDocumentInput(
		"processing-latest-other.pdf", "processing-latest/other.pdf", "3",
	))
	if err != nil {
		t.Fatalf("create other owner document: %v", err)
	}

	oldJob, err := jobs.CreateProcessingJob(ctx, documents.scope, firstDocument.ID)
	if err != nil {
		t.Fatalf("create old first job: %v", err)
	}
	if _, err := jobs.CancelProcessingJob(ctx, documents.scope, oldJob.ID); err != nil {
		t.Fatalf("cancel old first job: %v", err)
	}
	latestFirstJob, err := jobs.CreateProcessingJob(ctx, documents.scope, firstDocument.ID)
	if err != nil {
		t.Fatalf("create latest first job: %v", err)
	}
	secondJob, err := jobs.CreateProcessingJob(ctx, documents.scope, secondDocument.ID)
	if err != nil {
		t.Fatalf("create second job: %v", err)
	}
	otherJob, err := jobs.CreateProcessingJob(ctx, otherDocuments.scope, otherDocument.ID)
	if err != nil {
		t.Fatalf("create other owner job: %v", err)
	}

	foundJobs, err := jobs.FindLatestProcessingJobsByDocumentIDs(
		ctx,
		documents.scope,
		[]int64{firstDocument.ID, secondDocument.ID, otherDocument.ID},
	)
	if err != nil {
		t.Fatalf("find latest processing jobs: %v", err)
	}
	if len(foundJobs) != 2 {
		t.Fatalf("found jobs = %d, want 2", len(foundJobs))
	}
	foundByDocument := make(map[int64]documentdomain.ProcessingJob, len(foundJobs))
	for _, job := range foundJobs {
		foundByDocument[job.DocumentID] = job
	}
	if foundByDocument[firstDocument.ID].ID != latestFirstJob.ID {
		t.Fatalf("first latest job = %+v, want ID %d", foundByDocument[firstDocument.ID], latestFirstJob.ID)
	}
	if foundByDocument[secondDocument.ID].ID != secondJob.ID {
		t.Fatalf("second latest job = %+v, want ID %d", foundByDocument[secondDocument.ID], secondJob.ID)
	}
	if _, leaked := foundByDocument[otherDocument.ID]; leaked {
		t.Fatalf("other owner job %d leaked into result", otherJob.ID)
	}
}
