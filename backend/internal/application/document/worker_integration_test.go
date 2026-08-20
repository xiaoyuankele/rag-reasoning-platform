package document

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
	accessdomain "rag-reasoning-platform/backend/internal/domain/access"
	documentdomain "rag-reasoning-platform/backend/internal/domain/document"
	"rag-reasoning-platform/backend/internal/infrastructure/database"
	postgresrepository "rag-reasoning-platform/backend/internal/infrastructure/postgres"
	"rag-reasoning-platform/backend/migrations"
)

type integrationDocumentProcessor struct {
	processFunc func(
		context.Context,
		documentdomain.Document,
	) (ProcessingResult, error)
}

func (p *integrationDocumentProcessor) Process(
	ctx context.Context,
	document documentdomain.Document,
) (ProcessingResult, error) {
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
	documentCreator := postgresrepository.NewScopedDocumentRepository(pool)
	var ownerID int64
	if err := pool.QueryRow(
		ctx,
		`
			INSERT INTO users (
				email, email_verified_at, display_name, password_hash
			)
			VALUES ($1, CURRENT_TIMESTAMP, 'Worker Test Owner', '$argon2id$test-hash')
			RETURNING id
		`,
		fmt.Sprintf("worker-integration-%d@example.com", time.Now().UnixNano()),
	).Scan(&ownerID); err != nil {
		t.Fatalf("create Worker integration owner: %v", err)
	}
	ownerScope, err := accessdomain.NewOwnerScope(ownerID)
	if err != nil {
		t.Fatalf("create Worker integration owner scope: %v", err)
	}
	jobRepository := postgresrepository.NewProcessingJobRepository(pool)
	chunkRepository := postgresrepository.NewChunkRepository(pool)
	createdDocumentIDs := make([]int64, 0, 4)
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
		if _, cleanupErr := pool.Exec(
			cleanupContext,
			"DELETE FROM users WHERE id = $1",
			ownerID,
		); cleanupErr != nil {
			t.Errorf("clean up Worker integration owner: %v", cleanupErr)
		}
	}()

	t.Run("successful processor completes job", func(t *testing.T) {
		createdDocument, createdJob :=
			createWorkerIntegrationJob(
				t,
				ctx,
				documentCreator,
				ownerScope,
				jobRepository,
				"success",
				"a",
			)
		createdDocumentIDs = append(
			createdDocumentIDs,
			createdDocument.ID,
		)
		expectedChunks := []documentdomain.ChunkInput{
			{Index: 0, Content: "first integration chunk"},
			{Index: 1, Content: "second integration chunk"},
		}

		processor := &integrationDocumentProcessor{
			processFunc: func(
				_ context.Context,
				document documentdomain.Document,
			) (ProcessingResult, error) {
				if document.ID != createdDocument.ID {
					t.Fatalf(
						"processor document ID = %d, want %d",
						document.ID,
						createdDocument.ID,
					)
				}
				return ProcessingResult{
					Chunks: expectedChunks,
				}, nil
			},
		}
		worker := NewWorker(
			jobRepository,
			documentRepository,
			processor,
			chunkRepository,
			newRecordingProcessingJobEventObserver(),
			testWorkerProcessingTimeout,
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

		foundChunks, err := chunkRepository.ListByDocumentID(
			ctx,
			createdDocument.ID,
		)
		if err != nil {
			t.Fatalf("list successful Worker chunks: %v", err)
		}
		if len(foundChunks) != len(expectedChunks) {
			t.Fatalf(
				"stored chunk count = %d, want %d",
				len(foundChunks),
				len(expectedChunks),
			)
		}
		for index, expectedChunk := range expectedChunks {
			foundChunk := foundChunks[index]
			if foundChunk.Index != expectedChunk.Index ||
				foundChunk.Content != expectedChunk.Content {
				t.Fatalf(
					"stored chunk %d = %+v, want %+v",
					index,
					foundChunk,
					expectedChunk,
				)
			}
		}
	})

	t.Run("failed processor fails job safely", func(t *testing.T) {
		createdDocument, createdJob :=
			createWorkerIntegrationJob(
				t,
				ctx,
				documentCreator,
				ownerScope,
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
			) (ProcessingResult, error) {
				return ProcessingResult{}, processingError
			},
		}
		worker := NewWorker(
			jobRepository,
			documentRepository,
			processor,
			chunkRepository,
			newRecordingProcessingJobEventObserver(),
			testWorkerProcessingTimeout,
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

		foundChunks, err := chunkRepository.ListByDocumentID(
			ctx,
			createdDocument.ID,
		)
		if err != nil {
			t.Fatalf("list failed Worker chunks: %v", err)
		}
		if !reflect.DeepEqual(
			foundChunks,
			[]documentdomain.TextChunk{},
		) {
			t.Fatalf(
				"failed Worker chunks = %+v, want empty slice",
				foundChunks,
			)
		}
	})

	t.Run("worker pool processes two different jobs concurrently", func(t *testing.T) {
		createdDocuments := make([]documentdomain.Document, 0, 2)
		createdJobs := make([]documentdomain.ProcessingJob, 0, 2)
		for index, hashCharacter := range []string{"c", "d"} {
			createdDocument, createdJob := createWorkerIntegrationJob(
				t,
				ctx,
				documentCreator,
				ownerScope,
				jobRepository,
				fmt.Sprintf("pool-%d", index+1),
				hashCharacter,
			)
			createdDocuments = append(createdDocuments, createdDocument)
			createdJobs = append(createdJobs, createdJob)
			createdDocumentIDs = append(createdDocumentIDs, createdDocument.ID)
		}

		processorStarted := make(chan int64, 2)
		releaseProcessors := make(chan struct{})
		processor := &integrationDocumentProcessor{
			processFunc: func(
				processContext context.Context,
				document documentdomain.Document,
			) (ProcessingResult, error) {
				processorStarted <- document.ID
				select {
				case <-releaseProcessors:
					return ProcessingResult{
						Chunks: []documentdomain.ChunkInput{
							{
								Index:   0,
								Content: fmt.Sprintf("pool chunk for %d", document.ID),
							},
						},
					}, nil
				case <-processContext.Done():
					return ProcessingResult{}, processContext.Err()
				}
			},
		}
		worker := NewWorker(
			jobRepository,
			documentRepository,
			processor,
			chunkRepository,
			newRecordingProcessingJobEventObserver(),
			testWorkerProcessingTimeout,
		)
		loop, err := NewWorkerLoop(
			worker,
			10*time.Millisecond,
			func(workerErr error) {
				t.Errorf("worker pool iteration error: %v", workerErr)
			},
		)
		if err != nil {
			t.Fatalf("NewWorkerLoop() error = %v, want nil", err)
		}
		workerPool, err := NewWorkerPool(loop, 2)
		if err != nil {
			t.Fatalf("NewWorkerPool() error = %v, want nil", err)
		}

		poolContext, cancelPool := context.WithCancel(ctx)
		poolStopped := make(chan struct{})
		go func() {
			workerPool.Run(poolContext)
			close(poolStopped)
		}()
		defer func() {
			cancelPool()
			select {
			case <-poolStopped:
			case <-time.After(2 * time.Second):
				t.Error("WorkerPool did not stop during test cleanup")
			}
		}()

		startedDocumentIDs := make(map[int64]struct{}, 2)
		for range 2 {
			select {
			case documentID := <-processorStarted:
				startedDocumentIDs[documentID] = struct{}{}
			case <-time.After(2 * time.Second):
				t.Fatal("two document processors did not run concurrently")
			}
		}
		if len(startedDocumentIDs) != 2 {
			t.Fatalf(
				"concurrent processor document IDs = %v, want two distinct IDs",
				startedDocumentIDs,
			)
		}
		for _, createdDocument := range createdDocuments {
			if _, exists := startedDocumentIDs[createdDocument.ID]; !exists {
				t.Fatalf(
					"document %d was not processed by the pool",
					createdDocument.ID,
				)
			}
		}

		close(releaseProcessors)
		waitForProcessingJobsSucceeded(
			t,
			ctx,
			jobRepository,
			createdJobs,
		)

		cancelPool()
		select {
		case <-poolStopped:
		case <-time.After(2 * time.Second):
			t.Fatal("WorkerPool did not stop after cancellation")
		}
	})
}

func waitForProcessingJobsSucceeded(
	t *testing.T,
	ctx context.Context,
	jobRepository *postgresrepository.ProcessingJobRepository,
	jobs []documentdomain.ProcessingJob,
) {
	t.Helper()

	deadline := time.NewTimer(5 * time.Second)
	defer deadline.Stop()
	ticker := time.NewTicker(10 * time.Millisecond)
	defer ticker.Stop()

	for {
		allSucceeded := true
		for _, job := range jobs {
			foundJob, err := jobRepository.GetProcessingJobByID(ctx, job.ID)
			if err != nil {
				t.Fatalf("get pool processing job %d: %v", job.ID, err)
			}
			if foundJob.Status != documentdomain.ProcessingJobStatusSucceeded {
				allSucceeded = false
			}
		}
		if allSucceeded {
			return
		}

		select {
		case <-deadline.C:
			t.Fatal("WorkerPool jobs did not reach succeeded")
		case <-ticker.C:
		case <-ctx.Done():
			t.Fatalf("WorkerPool test context ended: %v", ctx.Err())
		}
	}
}

func createWorkerIntegrationJob(
	t *testing.T,
	ctx context.Context,
	documentCreator documentdomain.ScopedCreator,
	ownerScope accessdomain.OwnerScope,
	jobRepository *postgresrepository.ProcessingJobRepository,
	suffix string,
	hashCharacter string,
) (
	documentdomain.Document,
	documentdomain.ProcessingJob,
) {
	t.Helper()

	uniqueValue := time.Now().UnixNano()
	createdDocument, err := documentCreator.Create(
		ctx,
		ownerScope,
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
