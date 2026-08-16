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
		scopedDocuments,
		scopedJobs,
		modelName,
		dimensions,
	)
	queryService := embeddingapplication.NewJobQueryService(scopedJobs)
	// 集成测试只验证 HTTP、应用层和数据库之间的协作，
	// 因此把结构化日志写入 io.Discard，避免正常错误分支污染测试输出。
	logger := slog.New(slog.NewJSONHandler(io.Discard, nil))
	gin.SetMode(gin.TestMode)
	router := api.NewRouter(logger)
	protectedRoutes := router.Group("")
	protectedRoutes.Use(api.NewAuthMiddleware(sessionService, logger).Require)
	api.NewDocumentEmbeddingHandler(queueService).RegisterRoutes(protectedRoutes)
	api.NewEmbeddingJobHandler(queryService).RegisterRoutes(protectedRoutes)

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
		ID         int64  `json:"id"`
		DocumentID int64  `json:"document_id"`
		ModelName  string `json:"model_name"`
		Dimensions int    `json:"dimensions"`
	}
	if err := json.Unmarshal(ownerAQueue.Body.Bytes(), &queuedJob); err != nil {
		t.Fatalf("decode queued embedding job: %v", err)
	}
	if queuedJob.ID <= 0 || queuedJob.DocumentID != readyDocument.ID ||
		queuedJob.ModelName != modelName || queuedJob.Dimensions != dimensions {
		t.Fatalf("unexpected queued embedding job: %+v", queuedJob)
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
}
