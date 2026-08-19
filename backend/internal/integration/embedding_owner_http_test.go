package integration_test

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/gin-gonic/gin"

	"rag-reasoning-platform/backend/internal/api"
	authapplication "rag-reasoning-platform/backend/internal/application/auth"
	embeddingapplication "rag-reasoning-platform/backend/internal/application/embedding"
	accessdomain "rag-reasoning-platform/backend/internal/domain/access"
	documentdomain "rag-reasoning-platform/backend/internal/domain/document"
	embeddingdomain "rag-reasoning-platform/backend/internal/domain/embedding"
	postgresrepository "rag-reasoning-platform/backend/internal/infrastructure/postgres"
	sessioninfrastructure "rag-reasoning-platform/backend/internal/infrastructure/session"
)

// TestEmbeddingOwnerHTTPWithPostgreSQL 验证向量任务创建和查询
// 都不能越过关联文档的所有者边界；本测试不会启动远程 Embedding Worker。
func TestEmbeddingOwnerHTTPWithPostgreSQL(t *testing.T) {
	if os.Getenv("RUN_DATABASE_TESTS") != "1" {
		t.Skip("set RUN_DATABASE_TESTS=1 to run PostgreSQL integration tests")
	}

	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	pool := openDocumentOwnerIntegrationPool(t, ctx)
	uniqueSuffix := time.Now().UnixNano()
	ownerAID := insertDocumentOwnerIntegrationUser(
		t,
		ctx,
		pool,
		fmt.Sprintf("embedding-http-a-%d@example.com", uniqueSuffix),
	)
	ownerBID := insertDocumentOwnerIntegrationUser(
		t,
		ctx,
		pool,
		fmt.Sprintf("embedding-http-b-%d@example.com", uniqueSuffix),
	)
	t.Cleanup(func() {
		cleanupContext, cleanupCancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cleanupCancel()
		_, _ = pool.Exec(
			cleanupContext,
			"DELETE FROM documents WHERE owner_user_id = ANY($1::bigint[])",
			[]int64{ownerAID, ownerBID},
		)
		_, _ = pool.Exec(
			cleanupContext,
			"DELETE FROM users WHERE id = ANY($1::bigint[])",
			[]int64{ownerAID, ownerBID},
		)
	})

	ownerA, err := accessdomain.NewOwnerScope(ownerAID)
	if err != nil {
		t.Fatalf("create owner A scope: %v", err)
	}
	scopedDocuments := postgresrepository.NewScopedDocumentRepository(pool)
	readyDocument, err := scopedDocuments.Create(
		ctx,
		ownerA,
		documentdomain.CreateInput{
			OriginalName: "owner-a-embedding.md",
			StoragePath:  fmt.Sprintf("documents/owner-a-embedding-%d.md", uniqueSuffix),
			MIMEType:     "text/markdown",
			SizeBytes:    24,
			SHA256:       strings.Repeat("c", 64),
		},
	)
	if err != nil {
		t.Fatalf("create owner A document: %v", err)
	}
	if _, err := pool.Exec(
		ctx,
		"UPDATE documents SET status = 'ready', updated_at = CURRENT_TIMESTAMP WHERE id = $1",
		readyDocument.ID,
	); err != nil {
		t.Fatalf("mark owner A document ready: %v", err)
	}
	uploadedDocument, err := scopedDocuments.Create(
		ctx,
		ownerA,
		documentdomain.CreateInput{
			OriginalName: "owner-a-waiting-embedding.md",
			StoragePath: fmt.Sprintf(
				"documents/owner-a-waiting-embedding-%d.md",
				uniqueSuffix,
			),
			MIMEType:  "text/markdown",
			SizeBytes: 24,
			SHA256:    strings.Repeat("d", 64),
		},
	)
	if err != nil {
		t.Fatalf("create owner A uploaded document: %v", err)
	}

	sessionRepository := postgresrepository.NewAuthSessionRepository(pool)
	tokenManager := sessioninfrastructure.NewTokenGenerator()
	ownerACookie := createDocumentOwnerIntegrationSession(
		t, ctx, sessionRepository, tokenManager, ownerAID,
	)
	ownerBCookie := createDocumentOwnerIntegrationSession(
		t, ctx, sessionRepository, tokenManager, ownerBID,
	)
	sessionService, err := authapplication.NewSessionService(
		sessionRepository,
		tokenManager,
		time.Now,
	)
	if err != nil {
		t.Fatalf("create session service: %v", err)
	}

	const modelName = "test-embedding-model"
	const dimensions = 1536
	scopedJobs := postgresrepository.NewScopedEmbeddingJobRepository(pool)
	queueService := embeddingapplication.NewQueueService(
		scopedJobs,
		modelName,
		dimensions,
	)
	queryService := embeddingapplication.NewJobQueryService(scopedJobs)
	cancelService := embeddingapplication.NewCancelService(scopedJobs)
	// 集成测试只验证 HTTP、应用层和数据库之间的协作，
	// 因此把结构化日志写入 io.Discard，避免正常错误分支污染测试输出。
	logger := slog.New(slog.NewJSONHandler(io.Discard, nil))
	gin.SetMode(gin.TestMode)
	router := api.NewRouter(logger)
	protectedRoutes := router.Group("")
	protectedRoutes.Use(api.NewAuthMiddleware(sessionService, logger).Require)
	api.NewDocumentEmbeddingHandler(queueService).RegisterRoutes(protectedRoutes)
	api.NewDocumentEmbeddingBatchHandler(queueService, logger).RegisterRoutes(protectedRoutes)
	api.NewEmbeddingJobHandler(queryService).RegisterRoutes(protectedRoutes)
	api.NewEmbeddingJobCancelHandler(cancelService, logger).RegisterRoutes(protectedRoutes)

	queuePath := fmt.Sprintf("/documents/%d/embeddings", readyDocument.ID)
	unauthenticatedQueue := performDocumentOwnerRequest(
		router, http.MethodPost, queuePath, nil, "", "",
	)
	assertProcessingOwnerStatus(t, unauthenticatedQueue.Code, http.StatusUnauthorized, unauthenticatedQueue.Body.String())

	ownerBQueue := performDocumentOwnerRequest(
		router, http.MethodPost, queuePath, nil, ownerBCookie, "",
	)
	assertProcessingOwnerStatus(t, ownerBQueue.Code, http.StatusNotFound, ownerBQueue.Body.String())
	var jobsAfterRejectedQueue int
	if err := pool.QueryRow(
		ctx,
		"SELECT COUNT(*) FROM embedding_jobs WHERE document_id = $1",
		readyDocument.ID,
	).Scan(&jobsAfterRejectedQueue); err != nil {
		t.Fatalf("count jobs after rejected owner B queue: %v", err)
	}
	if jobsAfterRejectedQueue != 0 {
		t.Fatalf("embedding jobs after rejected owner B queue=%d want=0", jobsAfterRejectedQueue)
	}

	ownerAQueue := performDocumentOwnerRequest(
		router, http.MethodPost, queuePath, nil, ownerACookie, "",
	)
	assertProcessingOwnerStatus(t, ownerAQueue.Code, http.StatusAccepted, ownerAQueue.Body.String())
	var queuedJob struct {
		ID         int64                     `json:"id"`
		DocumentID int64                     `json:"document_id"`
		ModelName  string                    `json:"model_name"`
		Dimensions int                       `json:"dimensions"`
		Status     embeddingdomain.JobStatus `json:"status"`
	}
	if err := json.Unmarshal(ownerAQueue.Body.Bytes(), &queuedJob); err != nil {
		t.Fatalf("decode queued embedding job: %v", err)
	}
	if queuedJob.ID <= 0 || queuedJob.DocumentID != readyDocument.ID ||
		queuedJob.ModelName != modelName || queuedJob.Dimensions != dimensions ||
		queuedJob.Status != embeddingdomain.JobStatusQueued {
		t.Fatalf("unexpected queued embedding job: %+v", queuedJob)
	}

	// 尚未解析的文档也可以保存向量化意图，但返回 waiting_document，
	// 不会被 Worker 当成已经具备 chunks 的 queued 任务。
	waitingQueuePath := fmt.Sprintf(
		"/documents/%d/embeddings",
		uploadedDocument.ID,
	)
	waitingQueueResponse := performDocumentOwnerRequest(
		router,
		http.MethodPost,
		waitingQueuePath,
		nil,
		ownerACookie,
		"",
	)
	assertProcessingOwnerStatus(
		t,
		waitingQueueResponse.Code,
		http.StatusAccepted,
		waitingQueueResponse.Body.String(),
	)
	var waitingJob struct {
		ID         int64                     `json:"id"`
		DocumentID int64                     `json:"document_id"`
		Status     embeddingdomain.JobStatus `json:"status"`
	}
	if err := json.Unmarshal(waitingQueueResponse.Body.Bytes(), &waitingJob); err != nil {
		t.Fatalf("decode waiting embedding job: %v", err)
	}
	if waitingJob.ID <= 0 ||
		waitingJob.DocumentID != uploadedDocument.ID ||
		waitingJob.Status != embeddingdomain.JobStatusWaitingDocument {
		t.Fatalf("unexpected waiting embedding job: %+v", waitingJob)
	}

	// waiting_document 也属于活动任务。重复申请返回原任务和真实状态，
	// 让前端显示“仍在等待”，同时数据库不能创建第二条等待任务。
	duplicateWaitingResponse := performDocumentOwnerRequest(
		router,
		http.MethodPost,
		waitingQueuePath,
		nil,
		ownerACookie,
		"",
	)
	assertProcessingOwnerStatus(
		t,
		duplicateWaitingResponse.Code,
		http.StatusOK,
		duplicateWaitingResponse.Body.String(),
	)
	var duplicateWaitingJob struct {
		ID     int64                     `json:"id"`
		Status embeddingdomain.JobStatus `json:"status"`
	}
	if err := json.Unmarshal(duplicateWaitingResponse.Body.Bytes(), &duplicateWaitingJob); err != nil {
		t.Fatalf("decode duplicate waiting embedding job: %v", err)
	}
	if duplicateWaitingJob.ID != waitingJob.ID ||
		duplicateWaitingJob.Status != embeddingdomain.JobStatusWaitingDocument {
		t.Fatalf("duplicate waiting response = %+v, want original job %d", duplicateWaitingJob, waitingJob.ID)
	}
	var waitingJobCount int
	if err := pool.QueryRow(
		ctx,
		`SELECT COUNT(*)
		 FROM embedding_jobs
		 WHERE document_id = $1
		   AND status = 'waiting_document'`,
		uploadedDocument.ID,
	).Scan(&waitingJobCount); err != nil {
		t.Fatalf("count waiting embedding jobs after duplicate request: %v", err)
	}
	if waitingJobCount != 1 {
		t.Fatalf(
			"waiting embedding job count after duplicate request = %d, want 1",
			waitingJobCount,
		)
	}

	jobPath := fmt.Sprintf("/embedding-jobs/%d", queuedJob.ID)
	unauthenticatedJob := performDocumentOwnerRequest(
		router, http.MethodGet, jobPath, nil, "", "",
	)
	assertProcessingOwnerStatus(t, unauthenticatedJob.Code, http.StatusUnauthorized, unauthenticatedJob.Body.String())
	ownerBJob := performDocumentOwnerRequest(
		router, http.MethodGet, jobPath, nil, ownerBCookie, "",
	)
	assertProcessingOwnerStatus(t, ownerBJob.Code, http.StatusNotFound, ownerBJob.Body.String())
	ownerAJob := performDocumentOwnerRequest(
		router, http.MethodGet, jobPath, nil, ownerACookie, "",
	)
	assertProcessingOwnerStatus(t, ownerAJob.Code, http.StatusOK, ownerAJob.Body.String())

	// 其他用户不能取消任务；所有者可以取消 waiting_document，重复取消保持幂等。
	cancelPath := fmt.Sprintf("/embedding-jobs/%d/cancel", waitingJob.ID)
	ownerBCancel := performDocumentOwnerRequest(router, http.MethodPost, cancelPath, nil, ownerBCookie, "")
	assertProcessingOwnerStatus(t, ownerBCancel.Code, http.StatusNotFound, ownerBCancel.Body.String())
	ownerACancel := performDocumentOwnerRequest(router, http.MethodPost, cancelPath, nil, ownerACookie, "")
	assertProcessingOwnerStatus(t, ownerACancel.Code, http.StatusOK, ownerACancel.Body.String())
	var canceledJob struct {
		ID     int64                     `json:"id"`
		Status embeddingdomain.JobStatus `json:"status"`
	}
	if err := json.Unmarshal(ownerACancel.Body.Bytes(), &canceledJob); err != nil {
		t.Fatalf("decode canceled embedding job: %v", err)
	}
	if canceledJob.ID != waitingJob.ID || canceledJob.Status != embeddingdomain.JobStatusCanceled {
		t.Fatalf("canceled response = %+v, want canceled job %d", canceledJob, waitingJob.ID)
	}
	repeatedCancel := performDocumentOwnerRequest(router, http.MethodPost, cancelPath, nil, ownerACookie, "")
	assertProcessingOwnerStatus(t, repeatedCancel.Code, http.StatusOK, repeatedCancel.Body.String())

	// 批量申请按文件独立返回：已取消的文档创建新 waiting 任务，ready 文档
	// 返回已有 queued 任务，不存在的 ID 只影响自己的结果。
	batchBody := fmt.Sprintf(
		`{"document_ids":[%d,%d,999999999]}`,
		uploadedDocument.ID,
		readyDocument.ID,
	)
	batchResponse := performDocumentOwnerRequest(
		router,
		http.MethodPost,
		"/embedding-jobs/batch",
		strings.NewReader(batchBody),
		ownerACookie,
		"application/json",
	)
	assertProcessingOwnerStatus(t, batchResponse.Code, http.StatusOK, batchResponse.Body.String())
	var batchResult struct {
		Items []struct {
			DocumentID int64  `json:"document_id"`
			Outcome    string `json:"outcome"`
		} `json:"items"`
	}
	if err := json.Unmarshal(batchResponse.Body.Bytes(), &batchResult); err != nil {
		t.Fatalf("decode batch embedding response: %v", err)
	}
	if len(batchResult.Items) != 3 ||
		batchResult.Items[0].DocumentID != uploadedDocument.ID || batchResult.Items[0].Outcome != "created" ||
		batchResult.Items[1].DocumentID != readyDocument.ID || batchResult.Items[1].Outcome != "already_active" ||
		batchResult.Items[2].Outcome != "not_found" {
		t.Fatalf("unexpected batch embedding response: %+v", batchResult.Items)
	}
}
