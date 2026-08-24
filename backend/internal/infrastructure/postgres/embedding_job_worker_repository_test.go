package postgres_test

import (
	"context"
	"crypto/sha256"
	"errors"
	"fmt"
	"os"
	"sync"
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

// TestEmbeddingJobWorkerRepository 使用真实 PostgreSQL 验证向量任务的领取和收尾约束。
//
// 这些行为依赖行锁、状态条件和事务回滚，Fake 只能验证调用关系，不能代替数据库验证。
func TestEmbeddingJobWorkerRepository(t *testing.T) {
	if os.Getenv("RUN_DATABASE_TESTS") != "1" {
		t.Skip("set RUN_DATABASE_TESTS=1 to run PostgreSQL integration tests")
	}

	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
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
	if err := database.RefreshVectorTypes(ctx, pool); err != nil {
		t.Fatalf("refresh pgvector types: %v", err)
	}

	documentRepository := newOwnedDocumentFixture(t, ctx, pool)
	chunkRepository := postgresrepository.NewChunkRepository(pool)
	jobRepository := postgresrepository.NewEmbeddingJobRepository(pool)

	t.Run("claims the earliest due job", func(t *testing.T) {
		fixture := createEmbeddingWorkerFixture(
			t,
			ctx,
			pool,
			documentRepository,
			chunkRepository,
			jobRepository,
			"claim",
			[]string{"claim test chunk"},
		)

		// 使用明显早于正常业务任务的时间，确保共享开发库中本测试任务最先被领取。
		if _, err := pool.Exec(
			ctx,
			"UPDATE embedding_jobs SET next_attempt_at = $2 WHERE id = $1",
			fixture.job.ID,
			time.Date(2000, 1, 1, 0, 0, 0, 0, time.UTC),
		); err != nil {
			t.Fatalf("make embedding job due: %v", err)
		}

		claimedJob, err := jobRepository.ClaimNextEmbeddingJob(ctx)
		if err != nil {
			t.Fatalf("claim embedding job: %v", err)
		}
		if claimedJob.ID != fixture.job.ID {
			t.Fatalf("claimed job ID = %d, want %d", claimedJob.ID, fixture.job.ID)
		}
		if claimedJob.Status != embeddingdomain.JobStatusProcessing ||
			claimedJob.AttemptCount != 1 || claimedJob.StartedAt == nil {
			t.Fatalf("claimed job = %+v, want first processing attempt", claimedJob)
		}
		if err := jobRepository.MarkEmbeddingJobFailed(
			ctx,
			claimedJob.ID,
			"embedding claim integration test cleanup",
		); err != nil {
			t.Fatalf("finalize claimed embedding job: %v", err)
		}
	})

	t.Run("concurrent claimers receive different jobs", func(t *testing.T) {
		ownerA := newOwnedDocumentFixture(t, ctx, pool)
		ownerB := newOwnedDocumentFixture(t, ctx, pool)
		fixtures := []embeddingWorkerFixture{
			createEmbeddingWorkerFixture(
				t,
				ctx,
				pool,
				ownerA,
				chunkRepository,
				jobRepository,
				"concurrent-claim-a",
				[]string{"first concurrent claim chunk"},
			),
			createEmbeddingWorkerFixture(
				t,
				ctx,
				pool,
				ownerB,
				chunkRepository,
				jobRepository,
				"concurrent-claim-b",
				[]string{"second concurrent claim chunk"},
			),
		}

		for index, fixture := range fixtures {
			if _, err := pool.Exec(
				ctx,
				"UPDATE embedding_jobs SET next_attempt_at = $2 WHERE id = $1",
				fixture.job.ID,
				time.Date(1900+index, 1, 1, 0, 0, 0, 0, time.UTC),
			); err != nil {
				t.Fatalf("make concurrent embedding job due: %v", err)
			}
		}

		type claimResult struct {
			job embeddingdomain.Job
			err error
		}

		start := make(chan struct{})
		results := make(chan claimResult, len(fixtures))
		var group sync.WaitGroup
		group.Add(len(fixtures))

		for range fixtures {
			go func() {
				defer group.Done()
				<-start
				job, err := jobRepository.ClaimNextEmbeddingJob(ctx)
				results <- claimResult{job: job, err: err}
			}()
		}

		close(start)
		group.Wait()
		close(results)

		wantedIDs := map[int64]struct{}{
			fixtures[0].job.ID: {},
			fixtures[1].job.ID: {},
		}
		claimedIDs := make(map[int64]struct{}, len(fixtures))
		for result := range results {
			if result.err != nil {
				t.Fatalf("claim embedding job concurrently: %v", result.err)
			}
			if _, wanted := wantedIDs[result.job.ID]; !wanted {
				t.Fatalf(
					"concurrent claimer received job %d outside fixture set",
					result.job.ID,
				)
			}
			if _, duplicate := claimedIDs[result.job.ID]; duplicate {
				t.Fatalf("embedding job %d was claimed twice", result.job.ID)
			}
			claimedIDs[result.job.ID] = struct{}{}
		}

		if len(claimedIDs) != len(fixtures) {
			t.Fatalf(
				"claimed %d distinct jobs, want %d",
				len(claimedIDs),
				len(fixtures),
			)
		}
		for claimedID := range claimedIDs {
			if err := jobRepository.MarkEmbeddingJobFailed(
				ctx,
				claimedID,
				"concurrent embedding claim integration test cleanup",
			); err != nil {
				t.Fatalf("finalize concurrently claimed job %d: %v", claimedID, err)
			}
		}
	})

	t.Run("success saves all vectors and finalizes the job", func(t *testing.T) {
		fixture := createEmbeddingWorkerFixture(
			t,
			ctx,
			pool,
			documentRepository,
			chunkRepository,
			jobRepository,
			"success",
			[]string{"first semantic chunk", "second semantic chunk"},
		)
		markEmbeddingJobProcessing(t, ctx, pool, fixture.job.ID)

		completion := embeddingdomain.JobCompletion{
			Vectors: []embeddingdomain.ChunkVector{
				{ChunkID: fixture.chunks[0].ID, Values: embeddingVector(1536, 0.1)},
				{ChunkID: fixture.chunks[1].ID, Values: embeddingVector(1536, 0.2)},
			},
			PromptTokens: 10,
			TotalTokens:  10,
		}
		if err := jobRepository.MarkEmbeddingJobSucceeded(
			ctx,
			fixture.job.ID,
			completion,
		); err != nil {
			t.Fatalf("mark embedding job succeeded: %v", err)
		}

		assertEmbeddingJobState(
			t,
			ctx,
			pool,
			fixture.job.ID,
			embeddingdomain.JobStatusSucceeded,
			2,
		)

		// 再创建一次任务，故意让第二个向量维度错误。
		// 新写入和旧向量删除必须全部回滚，上一批有效向量仍应保留。
		replacementJob, err := jobRepository.CreateEmbeddingJob(
			ctx,
			fixture.document.ID,
			"text-embedding-3-small",
			1536,
		)
		if err != nil {
			t.Fatalf("create replacement embedding job: %v", err)
		}
		markEmbeddingJobProcessing(t, ctx, pool, replacementJob.ID)

		err = jobRepository.MarkEmbeddingJobSucceeded(
			ctx,
			replacementJob.ID,
			embeddingdomain.JobCompletion{
				Vectors: []embeddingdomain.ChunkVector{
					{ChunkID: fixture.chunks[0].ID, Values: embeddingVector(1536, 0.3)},
					{ChunkID: fixture.chunks[1].ID, Values: embeddingVector(2, 0.4)},
				},
				PromptTokens: 10,
				TotalTokens:  10,
			},
		)
		if err == nil {
			t.Fatal("invalid replacement vectors unexpectedly succeeded")
		}

		assertEmbeddingJobState(
			t,
			ctx,
			pool,
			replacementJob.ID,
			embeddingdomain.JobStatusProcessing,
			0,
		)
		assertEmbeddingJobState(
			t,
			ctx,
			pool,
			fixture.job.ID,
			embeddingdomain.JobStatusSucceeded,
			2,
		)
	})

	t.Run("permanent failure finalizes only a processing job", func(t *testing.T) {
		fixture := createEmbeddingWorkerFixture(
			t,
			ctx,
			pool,
			documentRepository,
			chunkRepository,
			jobRepository,
			"failed",
			[]string{"failed job chunk"},
		)
		markEmbeddingJobProcessing(t, ctx, pool, fixture.job.ID)

		const safeMessage = "embedding generation failed"
		if err := jobRepository.MarkEmbeddingJobFailed(
			ctx,
			fixture.job.ID,
			safeMessage,
		); err != nil {
			t.Fatalf("mark embedding job failed: %v", err)
		}

		var (
			status       embeddingdomain.JobStatus
			errorMessage *string
			completedAt  *time.Time
		)
		if err := pool.QueryRow(
			ctx,
			`SELECT status, error_message, completed_at
			FROM embedding_jobs
			WHERE id = $1`,
			fixture.job.ID,
		).Scan(&status, &errorMessage, &completedAt); err != nil {
			t.Fatalf("read failed embedding job: %v", err)
		}
		if status != embeddingdomain.JobStatusFailed ||
			errorMessage == nil || *errorMessage != safeMessage ||
			completedAt == nil {
			t.Fatalf("failed embedding job state = (%q, %v, %v)", status, errorMessage, completedAt)
		}

		err := jobRepository.MarkEmbeddingJobFailed(ctx, fixture.job.ID, safeMessage)
		if !errors.Is(err, embeddingdomain.ErrJobNotProcessing) {
			t.Fatalf("second failure finalization error = %v, want ErrJobNotProcessing", err)
		}
	})
}

type embeddingWorkerFixture struct {
	document documentdomain.Document
	chunks   []documentdomain.TextChunk
	job      embeddingdomain.Job
}

func createEmbeddingWorkerFixture(
	t *testing.T,
	ctx context.Context,
	pool *pgxpool.Pool,
	documentRepository *ownedDocumentFixture,
	chunkRepository *postgresrepository.ChunkRepository,
	jobRepository *postgresrepository.EmbeddingJobRepository,
	suffix string,
	contents []string,
) embeddingWorkerFixture {
	t.Helper()

	uniqueValue := time.Now().UnixNano()
	storagePath := fmt.Sprintf(
		"integration-tests/embedding-worker-%s-%d.pdf",
		suffix,
		uniqueValue,
	)
	contentHash := sha256.Sum256([]byte(storagePath))
	createdDocument, err := documentRepository.Create(
		ctx,
		documentdomain.CreateInput{
			OriginalName: fmt.Sprintf("embedding-worker-%s.pdf", suffix),
			StoragePath:  storagePath,
			MIMEType:     "application/pdf",
			SizeBytes:    4096,
			SHA256:       fmt.Sprintf("%x", contentHash),
		},
	)
	if err != nil {
		t.Fatalf("create embedding worker document: %v", err)
	}
	t.Cleanup(func() {
		cleanupContext, cleanupCancel := context.WithTimeout(
			context.Background(),
			5*time.Second,
		)
		defer cleanupCancel()
		if _, cleanupErr := pool.Exec(
			cleanupContext,
			"DELETE FROM documents WHERE id = $1",
			createdDocument.ID,
		); cleanupErr != nil {
			t.Errorf("clean up embedding worker document: %v", cleanupErr)
		}
	})

	chunkInputs := make([]documentdomain.ChunkInput, 0, len(contents))
	for index, content := range contents {
		chunkInputs = append(chunkInputs, documentdomain.ChunkInput{
			Index:   index,
			Content: content,
		})
	}
	if err := chunkRepository.ReplaceForDocument(
		ctx,
		createdDocument.ID,
		chunkInputs,
	); err != nil {
		t.Fatalf("create embedding worker chunks: %v", err)
	}

	if _, err := pool.Exec(
		ctx,
		"UPDATE documents SET status = 'ready' WHERE id = $1",
		createdDocument.ID,
	); err != nil {
		t.Fatalf("mark embedding worker document ready: %v", err)
	}

	chunks, err := chunkRepository.ListByDocumentID(ctx, createdDocument.ID)
	if err != nil {
		t.Fatalf("list embedding worker chunks: %v", err)
	}

	createdJob, err := jobRepository.CreateEmbeddingJob(
		ctx,
		createdDocument.ID,
		"text-embedding-3-small",
		1536,
	)
	if err != nil {
		t.Fatalf("create embedding worker job: %v", err)
	}

	return embeddingWorkerFixture{
		document: createdDocument,
		chunks:   chunks,
		job:      createdJob,
	}
}

func markEmbeddingJobProcessing(
	t *testing.T,
	ctx context.Context,
	pool *pgxpool.Pool,
	jobID int64,
) {
	t.Helper()

	if _, err := pool.Exec(
		ctx,
		`UPDATE embedding_jobs
		SET
			status = 'processing',
			attempt_count = attempt_count + 1,
			started_at = CURRENT_TIMESTAMP,
			updated_at = CURRENT_TIMESTAMP
		WHERE id = $1`,
		jobID,
	); err != nil {
		t.Fatalf("mark embedding job processing: %v", err)
	}
}

func assertEmbeddingJobState(
	t *testing.T,
	ctx context.Context,
	pool *pgxpool.Pool,
	jobID int64,
	wantStatus embeddingdomain.JobStatus,
	wantVectorCount int,
) {
	t.Helper()

	var status embeddingdomain.JobStatus
	if err := pool.QueryRow(
		ctx,
		"SELECT status FROM embedding_jobs WHERE id = $1",
		jobID,
	).Scan(&status); err != nil {
		t.Fatalf("read embedding job status: %v", err)
	}
	if status != wantStatus {
		t.Fatalf("embedding job status = %q, want %q", status, wantStatus)
	}

	var vectorCount int
	if err := pool.QueryRow(
		ctx,
		"SELECT COUNT(*) FROM chunk_embeddings WHERE embedding_job_id = $1",
		jobID,
	).Scan(&vectorCount); err != nil {
		t.Fatalf("count embedding job vectors: %v", err)
	}
	if vectorCount != wantVectorCount {
		t.Fatalf("embedding vector count = %d, want %d", vectorCount, wantVectorCount)
	}
}

func embeddingVector(dimensions int, value float32) []float32 {
	vector := make([]float32, dimensions)
	for index := range vector {
		vector[index] = value
	}

	return vector
}
