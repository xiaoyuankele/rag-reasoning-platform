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
	documentapplication "rag-reasoning-platform/backend/internal/application/document"
	accessdomain "rag-reasoning-platform/backend/internal/domain/access"
	documentdomain "rag-reasoning-platform/backend/internal/domain/document"
	postgresrepository "rag-reasoning-platform/backend/internal/infrastructure/postgres"
	sessioninfrastructure "rag-reasoning-platform/backend/internal/infrastructure/session"
)

// TestProcessingOwnerHTTPWithPostgreSQL 验证解析排队、chunks 浏览和任务查询
// 都不能越过文档所有者边界。
func TestProcessingOwnerHTTPWithPostgreSQL(t *testing.T) {
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
		fmt.Sprintf("processing-owner-a-%d@example.com", uniqueSuffix),
	)
	ownerBID := insertDocumentOwnerIntegrationUser(
		t,
		ctx,
		pool,
		fmt.Sprintf("processing-owner-b-%d@example.com", uniqueSuffix),
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
	queuedDocument, err := scopedDocuments.Create(
		ctx,
		ownerA,
		documentdomain.CreateInput{
			OriginalName: "owner-a-queued.md",
			StoragePath:  fmt.Sprintf("documents/owner-a-queued-%d.md", uniqueSuffix),
			MIMEType:     "text/markdown",
			SizeBytes:    14,
			SHA256:       strings.Repeat("a", 64),
		},
	)
	if err != nil {
		t.Fatalf("create queued owner A document: %v", err)
	}
	readyDocument, err := scopedDocuments.Create(
		ctx,
		ownerA,
		documentdomain.CreateInput{
			OriginalName: "owner-a-ready.md",
			StoragePath:  fmt.Sprintf("documents/owner-a-ready-%d.md", uniqueSuffix),
			MIMEType:     "text/markdown",
			SizeBytes:    28,
			SHA256:       strings.Repeat("b", 64),
		},
	)
	if err != nil {
		t.Fatalf("create ready owner A document: %v", err)
	}
	systemChunks := postgresrepository.NewChunkRepository(pool)
	if err := systemChunks.ReplaceForDocument(
		ctx,
		readyDocument.ID,
		[]documentdomain.ChunkInput{
			{Index: 0, Content: "owner A first private chunk"},
			{Index: 1, Content: "owner A second private chunk"},
		},
	); err != nil {
		t.Fatalf("persist ready owner A chunks: %v", err)
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

	scopedJobs := postgresrepository.NewScopedProcessingJobRepository(
		pool,
		documentdomain.ProcessingJobAdmissionLimits{
			MaxActiveJobsPerOwner: 100,
			MaxActiveJobsGlobal:   500,
		},
	)
	scopedChunks := postgresrepository.NewScopedChunkRepository(pool)
	logger := slog.New(slog.NewJSONHandler(io.Discard, nil))
	gin.SetMode(gin.TestMode)
	router := api.NewRouter(logger)
	protectedRoutes := router.Group("")
	protectedRoutes.Use(api.NewAuthMiddleware(sessionService, logger).Require)
	api.NewDocumentProcessingHandler(
		documentapplication.NewQueueProcessingService(scopedDocuments, scopedJobs),
	).RegisterRoutes(protectedRoutes)
	api.NewProcessingJobHandler(
		documentapplication.NewProcessingJobService(scopedJobs),
		logger,
	).RegisterRoutes(protectedRoutes)
	api.NewDocumentChunkHandler(
		documentapplication.NewChunkListService(scopedDocuments, scopedChunks),
	).RegisterRoutes(protectedRoutes)

	queuePath := fmt.Sprintf("/documents/%d/process", queuedDocument.ID)
	unauthenticatedQueue := performDocumentOwnerRequest(
		router, http.MethodPost, queuePath, nil, "", "",
	)
	assertProcessingOwnerStatus(t, unauthenticatedQueue.Code, http.StatusUnauthorized, unauthenticatedQueue.Body.String())

	ownerBQueue := performDocumentOwnerRequest(
		router, http.MethodPost, queuePath, nil, ownerBCookie, "",
	)
	assertProcessingOwnerStatus(t, ownerBQueue.Code, http.StatusNotFound, ownerBQueue.Body.String())
	var jobsBeforeOwnerQueue int
	if err := pool.QueryRow(
		ctx,
		"SELECT COUNT(*) FROM document_jobs WHERE document_id = $1",
		queuedDocument.ID,
	).Scan(&jobsBeforeOwnerQueue); err != nil {
		t.Fatalf("count jobs after rejected owner B queue: %v", err)
	}
	if jobsBeforeOwnerQueue != 0 {
		t.Fatalf("jobs after rejected owner B queue=%d want=0", jobsBeforeOwnerQueue)
	}

	ownerAQueue := performDocumentOwnerRequest(
		router, http.MethodPost, queuePath, nil, ownerACookie, "",
	)
	assertProcessingOwnerStatus(t, ownerAQueue.Code, http.StatusAccepted, ownerAQueue.Body.String())
	var queuedJob struct {
		ID         int64 `json:"id"`
		DocumentID int64 `json:"document_id"`
	}
	if err := json.Unmarshal(ownerAQueue.Body.Bytes(), &queuedJob); err != nil {
		t.Fatalf("decode queued job response: %v", err)
	}
	if queuedJob.ID <= 0 || queuedJob.DocumentID != queuedDocument.ID {
		t.Fatalf("unexpected queued job response: %+v", queuedJob)
	}

	jobPath := fmt.Sprintf("/processing-jobs/%d", queuedJob.ID)
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

	chunksPath := fmt.Sprintf("/documents/%d/chunks", readyDocument.ID)
	unauthenticatedChunks := performDocumentOwnerRequest(
		router, http.MethodGet, chunksPath, nil, "", "",
	)
	assertProcessingOwnerStatus(t, unauthenticatedChunks.Code, http.StatusUnauthorized, unauthenticatedChunks.Body.String())
	ownerBChunks := performDocumentOwnerRequest(
		router, http.MethodGet, chunksPath, nil, ownerBCookie, "",
	)
	assertProcessingOwnerStatus(t, ownerBChunks.Code, http.StatusNotFound, ownerBChunks.Body.String())
	ownerAChunks := performDocumentOwnerRequest(
		router, http.MethodGet, chunksPath, nil, ownerACookie, "",
	)
	assertProcessingOwnerStatus(t, ownerAChunks.Code, http.StatusOK, ownerAChunks.Body.String())
	var chunkResponse struct {
		Chunks []struct {
			Content string `json:"content"`
		} `json:"chunks"`
		Pagination struct {
			Total int64 `json:"total"`
		} `json:"pagination"`
	}
	if err := json.Unmarshal(ownerAChunks.Body.Bytes(), &chunkResponse); err != nil {
		t.Fatalf("decode owner A chunks response: %v", err)
	}
	if len(chunkResponse.Chunks) != 2 || chunkResponse.Pagination.Total != 2 ||
		chunkResponse.Chunks[0].Content != "owner A first private chunk" {
		t.Fatalf("unexpected owner A chunks response: %+v", chunkResponse)
	}
}

// assertProcessingOwnerStatus 输出失败响应体，便于定位身份、业务或 SQL 层问题。
func assertProcessingOwnerStatus(
	t *testing.T,
	actual int,
	expected int,
	body string,
) {
	t.Helper()
	if actual != expected {
		t.Fatalf("HTTP status=%d want=%d body=%s", actual, expected, body)
	}
}
