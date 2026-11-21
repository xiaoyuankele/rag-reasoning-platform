package integration_test

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/jackc/pgx/v5/pgxpool"

	"rag-reasoning-platform/backend/internal/api"
	authapplication "rag-reasoning-platform/backend/internal/application/auth"
	documentapplication "rag-reasoning-platform/backend/internal/application/document"
	"rag-reasoning-platform/backend/internal/config"
	documentdomain "rag-reasoning-platform/backend/internal/domain/document"
	"rag-reasoning-platform/backend/internal/infrastructure/filestorage"
	postgresrepository "rag-reasoning-platform/backend/internal/infrastructure/postgres"
	"rag-reasoning-platform/backend/internal/infrastructure/pythonprocessor"
	sessioninfrastructure "rag-reasoning-platform/backend/internal/infrastructure/session"
	"rag-reasoning-platform/backend/internal/observability"
)

// TestOSSDocumentLifecycleWithPostgreSQLAndPython 验证真实产品组件的纵向链路：
//
// Session Cookie -> HTTP Upload -> OSS -> PostgreSQL -> HTTP Queue ->
// Document Worker -> OSS Materialize -> Python -> chunks -> HTTP Delete。
//
// 本测试会产生少量真实 OSS 请求，因此默认跳过。它还要求数据库名称以
// rag_integration_ 开头，防止 Worker 从开发数据库领取其他用户的排队任务。
func TestOSSDocumentLifecycleWithPostgreSQLAndPython(t *testing.T) {
	requireOSSVerticalGate(t, "RUN_DATABASE_TESTS")
	requireOSSVerticalGate(t, "RUN_PYTHON_TESTS")
	requireOSSVerticalGate(t, "RUN_OSS_INTEGRATION_TESTS")
	requireOSSVerticalGate(t, "RUN_OSS_VERTICAL_INTEGRATION_TESTS")

	databaseName := strings.TrimSpace(os.Getenv("DB_NAME"))
	if !strings.HasPrefix(databaseName, "rag_integration_") {
		t.Fatalf(
			"DB_NAME=%q is unsafe; OSS vertical test requires an isolated rag_integration_* database",
			databaseName,
		)
	}

	ctx, cancel := context.WithTimeout(context.Background(), 90*time.Second)
	defer cancel()

	repositoryRoot := ossVerticalRepositoryRoot(t)
	storageConfig, err := config.LoadStorage(repositoryRoot)
	if err != nil {
		t.Fatalf("load OSS storage configuration: %v", err)
	}
	if storageConfig.Driver != config.StorageDriverOSS {
		t.Fatalf(
			"FILE_STORAGE_DRIVER resolved to %q, want %q",
			storageConfig.Driver,
			config.StorageDriverOSS,
		)
	}

	objectClient, err := filestorage.NewAliyunOSSObjectClient(
		filestorage.AliyunOSSClientConfig{
			Bucket:         storageConfig.OSS.Bucket,
			Region:         storageConfig.OSS.Region,
			Endpoint:       storageConfig.OSS.Endpoint,
			CredentialMode: storageConfig.OSS.CredentialMode,
			ECSRAMRole:     storageConfig.OSS.ECSRAMRole,
		},
	)
	if err != nil {
		t.Fatalf("create Aliyun OSS client: %v", err)
	}

	stagingRoot := t.TempDir()
	storage, err := filestorage.NewObjectStorage(
		objectClient,
		stagingRoot,
		storageConfig.MaxFileSizeBytes,
	)
	if err != nil {
		t.Fatalf("create OSS file storage: %v", err)
	}

	pool := openDocumentOwnerIntegrationPool(t, ctx)
	assertOSSVerticalQueueIsEmpty(t, ctx, pool)

	uniqueSuffix := time.Now().UnixNano()
	ownerID := insertDocumentOwnerIntegrationUser(
		t,
		ctx,
		pool,
		fmt.Sprintf("oss-vertical-%d@example.com", uniqueSuffix),
	)

	var documentID int64
	var storagePath string
	t.Cleanup(func() {
		cleanupContext, cleanupCancel := context.WithTimeout(
			context.Background(),
			15*time.Second,
		)
		defer cleanupCancel()

		if storagePath != "" {
			if cleanupErr := storage.Delete(cleanupContext, storagePath); cleanupErr != nil {
				t.Errorf("clean up OSS vertical object: %v", cleanupErr)
			}
		}
		if documentID > 0 {
			if _, cleanupErr := pool.Exec(
				cleanupContext,
				"DELETE FROM documents WHERE id = $1",
				documentID,
			); cleanupErr != nil {
				t.Errorf("clean up OSS vertical document: %v", cleanupErr)
			}
		}
		if _, cleanupErr := pool.Exec(
			cleanupContext,
			"DELETE FROM users WHERE id = $1",
			ownerID,
		); cleanupErr != nil {
			t.Errorf("clean up OSS vertical owner: %v", cleanupErr)
		}
	})

	router, sessionToken := newOSSVerticalRouter(
		t,
		ctx,
		pool,
		ownerID,
		storage,
		storageConfig.MaxFileSizeBytes,
	)

	pythonExecutable := strings.TrimSpace(os.Getenv("PYTHON_EXECUTABLE"))
	if pythonExecutable == "" {
		pythonExecutable = "python"
	}
	aiRoot := filepath.Join(repositoryRoot, "ai")
	fixturePath := filepath.Join(t.TempDir(), "oss-vertical.pdf")
	writeIntegrationTextPDF(t, pythonExecutable, aiRoot, fixturePath)
	fixtureContent, err := os.ReadFile(fixturePath)
	if err != nil {
		t.Fatalf("read OSS vertical PDF fixture: %v", err)
	}

	uploadResponse := performDocumentOwnerUpload(
		t,
		router,
		sessionToken,
		"oss-vertical.pdf",
		string(fixtureContent),
	)
	if uploadResponse.Code != http.StatusCreated {
		t.Fatalf(
			"OSS vertical upload status=%d want=201 body=%s",
			uploadResponse.Code,
			uploadResponse.Body.String(),
		)
	}
	var uploadedDocument struct {
		ID int64 `json:"id"`
	}
	if err := json.Unmarshal(uploadResponse.Body.Bytes(), &uploadedDocument); err != nil {
		t.Fatalf("decode OSS vertical upload response: %v", err)
	}
	documentID = uploadedDocument.ID
	if documentID <= 0 {
		t.Fatalf("uploaded document ID=%d, want positive", documentID)
	}

	if err := pool.QueryRow(
		ctx,
		"SELECT storage_path FROM documents WHERE id = $1 AND owner_user_id = $2",
		documentID,
		ownerID,
	).Scan(&storagePath); err != nil {
		t.Fatalf("query OSS vertical storage path: %v", err)
	}
	if !strings.HasPrefix(storagePath, "documents/") {
		t.Fatalf("storage path=%q, want documents/* object key", storagePath)
	}
	assertOSSObjectReadable(t, ctx, storage, storagePath)

	queueResponse := performDocumentOwnerRequest(
		router,
		http.MethodPost,
		fmt.Sprintf("/documents/%d/process", documentID),
		nil,
		sessionToken,
		"",
	)
	if queueResponse.Code != http.StatusAccepted {
		t.Fatalf(
			"OSS vertical queue status=%d want=202 body=%s",
			queueResponse.Code,
			queueResponse.Body.String(),
		)
	}
	var queuedJob struct {
		ID int64 `json:"id"`
	}
	if err := json.Unmarshal(queueResponse.Body.Bytes(), &queuedJob); err != nil {
		t.Fatalf("decode OSS vertical queue response: %v", err)
	}

	processor, err := pythonprocessor.NewProcessor(
		storage,
		pythonExecutable,
		filepath.Join(aiRoot, "src"),
		5*1024*1024,
		10,
	)
	if err != nil {
		t.Fatalf("create OSS vertical Python processor: %v", err)
	}
	logger := slog.New(slog.NewJSONHandler(io.Discard, nil))
	workerJobs := postgresrepository.NewProcessingJobRepository(pool)
	workerDocuments := postgresrepository.NewDocumentRepository(pool)
	workerChunks := postgresrepository.NewChunkRepository(pool)
	worker := documentapplication.NewWorker(
		workerJobs,
		workerDocuments,
		processor,
		workerChunks,
		observability.NewProcessingJobLogger(logger),
		30*time.Second,
	)

	handled, err := worker.RunOnce(ctx)
	if err != nil {
		t.Fatalf("run OSS vertical Document Worker: %v", err)
	}
	if !handled {
		t.Fatal("OSS vertical Document Worker handled=false, want true")
	}

	processedJob, err := workerJobs.GetProcessingJobByID(ctx, queuedJob.ID)
	if err != nil {
		t.Fatalf("get OSS vertical processing job: %v", err)
	}
	if processedJob.Status != documentdomain.ProcessingJobStatusSucceeded {
		t.Fatalf(
			"processing job status=%q want=%q",
			processedJob.Status,
			documentdomain.ProcessingJobStatusSucceeded,
		)
	}
	processedDocument, err := workerDocuments.GetByID(ctx, documentID)
	if err != nil {
		t.Fatalf("get processed OSS document: %v", err)
	}
	if processedDocument.Status != documentdomain.StatusReady {
		t.Fatalf(
			"processed document status=%q want=%q",
			processedDocument.Status,
			documentdomain.StatusReady,
		)
	}

	storedChunks, err := workerChunks.ListByDocumentID(ctx, documentID)
	if err != nil {
		t.Fatalf("list OSS vertical chunks: %v", err)
	}
	if len(storedChunks) != 2 {
		t.Fatalf("OSS vertical chunk count=%d want=2", len(storedChunks))
	}
	assertPersistedPDFChunk(
		t,
		storedChunks[0],
		documentID,
		0,
		"first page content",
		1,
	)
	assertPersistedPDFChunk(
		t,
		storedChunks[1],
		documentID,
		1,
		"second page content",
		2,
	)
	assertOSSStagingEmpty(t, stagingRoot)

	deleteResponse := performDocumentOwnerRequest(
		router,
		http.MethodDelete,
		fmt.Sprintf("/documents/%d", documentID),
		nil,
		sessionToken,
		"",
	)
	if deleteResponse.Code != http.StatusNoContent {
		t.Fatalf(
			"OSS vertical delete status=%d want=204 body=%s",
			deleteResponse.Code,
			deleteResponse.Body.String(),
		)
	}

	assertOSSVerticalDatabaseDeleted(t, ctx, pool, documentID, queuedJob.ID)
	if _, err := storage.Open(ctx, storagePath); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("open deleted OSS object error=%v, want os.ErrNotExist", err)
	}
	assertOSSStagingEmpty(t, stagingRoot)
}

func newOSSVerticalRouter(
	t *testing.T,
	ctx context.Context,
	pool *pgxpool.Pool,
	ownerID int64,
	storage documentapplication.FileStorage,
	maxFileSizeBytes int64,
) (http.Handler, string) {
	t.Helper()

	sessionRepository := postgresrepository.NewAuthSessionRepository(pool)
	tokenManager := sessioninfrastructure.NewTokenGenerator()
	rawSessionToken := createDocumentOwnerIntegrationSession(
		t,
		ctx,
		sessionRepository,
		tokenManager,
		ownerID,
	)
	sessionService, err := authapplication.NewSessionService(
		sessionRepository,
		tokenManager,
		time.Now,
	)
	if err != nil {
		t.Fatalf("create OSS vertical session service: %v", err)
	}

	documents := postgresrepository.NewScopedDocumentRepository(pool)
	jobs := postgresrepository.NewScopedProcessingJobRepository(
		pool,
		documentdomain.ProcessingJobAdmissionLimits{
			MaxActiveJobsPerOwner: 5,
			MaxActiveJobsGlobal:   40,
		},
	)
	logger := slog.New(slog.NewJSONHandler(io.Discard, nil))

	gin.SetMode(gin.TestMode)
	router := api.NewRouter(logger)
	protectedRoutes := router.Group("")
	protectedRoutes.Use(api.NewAuthMiddleware(sessionService, logger).Require)
	api.NewDocumentUploadHandler(
		documentapplication.NewUploadService(documents, storage),
		maxFileSizeBytes,
	).RegisterRoutes(protectedRoutes)
	api.NewDocumentProcessingHandler(
		documentapplication.NewQueueProcessingService(documents, jobs),
	).RegisterRoutes(protectedRoutes)
	api.NewDocumentDeleteHandler(
		documentapplication.NewDeleteService(documents, storage),
	).RegisterRoutes(protectedRoutes)

	return router, rawSessionToken
}

func requireOSSVerticalGate(t *testing.T, name string) {
	t.Helper()
	if os.Getenv(name) != "1" {
		t.Skipf("set %s=1 to run OSS document vertical integration test", name)
	}
}

// ossVerticalRepositoryRoot 允许预编译测试二进制在另一台机器上运行。
// 未显式配置时仍沿用当前源码树推导逻辑；跨操作系统执行时应把
// OSS_VERTICAL_REPOSITORY_ROOT 指向目标机器上的项目根目录。
func ossVerticalRepositoryRoot(t *testing.T) string {
	t.Helper()

	override := strings.TrimSpace(os.Getenv("OSS_VERTICAL_REPOSITORY_ROOT"))
	if override == "" {
		return integrationRepositoryRoot(t)
	}

	absoluteRoot, err := filepath.Abs(override)
	if err != nil {
		t.Fatalf("resolve OSS vertical repository root: %v", err)
	}
	info, err := os.Stat(absoluteRoot)
	if err != nil {
		t.Fatalf("stat OSS vertical repository root: %v", err)
	}
	if !info.IsDir() {
		t.Fatalf("OSS vertical repository root %q is not a directory", absoluteRoot)
	}

	return absoluteRoot
}

func assertOSSVerticalQueueIsEmpty(
	t *testing.T,
	ctx context.Context,
	pool *pgxpool.Pool,
) {
	t.Helper()
	var activeJobs int
	if err := pool.QueryRow(
		ctx,
		"SELECT COUNT(*) FROM document_jobs WHERE status IN ('queued', 'processing')",
	).Scan(&activeJobs); err != nil {
		t.Fatalf("count active document jobs: %v", err)
	}
	if activeJobs != 0 {
		t.Fatalf("isolated database contains %d active document jobs", activeJobs)
	}
}

func assertOSSObjectReadable(
	t *testing.T,
	ctx context.Context,
	storage *filestorage.ObjectStorage,
	storagePath string,
) {
	t.Helper()
	reader, err := storage.Open(ctx, storagePath)
	if err != nil {
		t.Fatalf("open uploaded OSS object: %v", err)
	}
	if _, err := io.Copy(io.Discard, reader); err != nil {
		_ = reader.Close()
		t.Fatalf("read uploaded OSS object: %v", err)
	}
	if err := reader.Close(); err != nil {
		t.Fatalf("close uploaded OSS object: %v", err)
	}
}

func assertOSSStagingEmpty(t *testing.T, stagingRoot string) {
	t.Helper()
	entries, err := os.ReadDir(stagingRoot)
	if err != nil {
		t.Fatalf("read OSS staging directory: %v", err)
	}
	if len(entries) != 0 {
		t.Fatalf("OSS staging directory contains %d leftover entries", len(entries))
	}
}

func assertOSSVerticalDatabaseDeleted(
	t *testing.T,
	ctx context.Context,
	pool *pgxpool.Pool,
	documentID int64,
	jobID int64,
) {
	t.Helper()
	var documents int
	var jobs int
	var chunks int
	if err := pool.QueryRow(
		ctx,
		`SELECT
			(SELECT COUNT(*) FROM documents WHERE id = $1),
			(SELECT COUNT(*) FROM document_jobs WHERE id = $2),
			(SELECT COUNT(*) FROM text_chunks WHERE document_id = $1)`,
		documentID,
		jobID,
	).Scan(&documents, &jobs, &chunks); err != nil {
		t.Fatalf("count deleted OSS vertical rows: %v", err)
	}
	if documents != 0 || jobs != 0 || chunks != 0 {
		t.Fatalf(
			"deleted OSS vertical rows documents=%d jobs=%d chunks=%d, want all zero",
			documents,
			jobs,
			chunks,
		)
	}
}
