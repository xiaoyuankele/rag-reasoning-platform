package postgres_test

import (
	"context"
	"fmt"
	"os"
	"testing"
	"time"

	documentdomain "rag-reasoning-platform/backend/internal/domain/document"
	embeddingdomain "rag-reasoning-platform/backend/internal/domain/embedding"
	postgresrepository "rag-reasoning-platform/backend/internal/infrastructure/postgres"
)

// TestEmbeddingRequestAndProcessingSuccessDoNotStrandWaitingJob 验证申请向量化
// 与解析成功并发发生时，无论谁先拿到 document 行锁，最终任务都进入 queued。
//
// 这条测试保护的是跨用例时序，而不是单个函数：如果两条事务没有锁定同一
// document 行，就可能出现文档已经 ready、向量任务却永久 waiting 的状态。
func TestEmbeddingRequestAndProcessingSuccessDoNotStrandWaitingJob(t *testing.T) {
	if os.Getenv("RUN_DATABASE_TESTS") != "1" {
		t.Skip("set RUN_DATABASE_TESTS=1 to run PostgreSQL integration tests")
	}

	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	pool := openIsolatedDocumentTestPool(t, ctx)
	documents := newOwnedDocumentFixture(t, ctx, pool)
	processingJobs := postgresrepository.NewProcessingJobRepository(pool)
	embeddingJobs := postgresrepository.NewScopedEmbeddingJobRepository(pool, testEmbeddingAdmissionLimits())

	type embeddingRequestResult struct {
		job embeddingdomain.Job
		err error
	}

	// 重复运行几轮，让 PostgreSQL 调度器有机会覆盖两种加锁先后顺序。
	for iteration := 0; iteration < 8; iteration++ {
		t.Run(fmt.Sprintf("iteration_%d", iteration), func(t *testing.T) {
			createdDocument, processingJob := createProcessingJobForFinalization(
				t,
				ctx,
				pool,
				documents,
				processingJobs,
				fmt.Sprintf("embedding-request-race-%d-%d", iteration, time.Now().UnixNano()),
			)

			start := make(chan struct{})
			requestResult := make(chan embeddingRequestResult, 1)
			finalizeResult := make(chan error, 1)

			go func() {
				<-start
				result, err := embeddingJobs.RequestEmbeddingJob(
					ctx,
					documents.scope,
					createdDocument.ID,
					"test-embedding-model",
					8,
				)
				requestResult <- embeddingRequestResult{job: result.Job, err: err}
			}()

			go func() {
				<-start
				finalizeResult <- processingJobs.MarkProcessingJobSucceeded(
					ctx,
					processingJob.ID,
					processingJob.LeaseToken,
					documentdomain.ProcessingCompletion{},
				)
			}()

			close(start)
			requested := <-requestResult
			if requested.err != nil {
				t.Fatalf("request embedding job concurrently: %v", requested.err)
			}
			if err := <-finalizeResult; err != nil {
				t.Fatalf("finalize processing job concurrently: %v", err)
			}

			foundJob, err := embeddingJobs.GetEmbeddingJobByID(
				ctx,
				documents.scope,
				requested.job.ID,
			)
			if err != nil {
				t.Fatalf("get embedding job after concurrent operations: %v", err)
			}
			if foundJob.Status != embeddingdomain.JobStatusQueued {
				t.Fatalf(
					"embedding job final status = %q, want %q",
					foundJob.Status,
					embeddingdomain.JobStatusQueued,
				)
			}
		})
	}
}
