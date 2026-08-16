package integration_test

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"mime/multipart"
	"net/http"
	"net/http/httptest"
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
	"rag-reasoning-platform/backend/internal/infrastructure/database"
	"rag-reasoning-platform/backend/internal/infrastructure/filestorage"
	postgresrepository "rag-reasoning-platform/backend/internal/infrastructure/postgres"
	sessioninfrastructure "rag-reasoning-platform/backend/internal/infrastructure/session"
	"rag-reasoning-platform/backend/migrations"
)

const integrationSessionCookieName = "rag_session"

// TestDocumentOwnerHTTPWithPostgreSQL 验证个人用户文档接口的完整隔离链路：
// Cookie → AuthMiddleware → OwnerScope → Application → PostgreSQL。
func TestDocumentOwnerHTTPWithPostgreSQL(t *testing.T) {
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
		fmt.Sprintf("document-owner-a-%d@example.com", uniqueSuffix),
	)
	ownerBID := insertDocumentOwnerIntegrationUser(
		t,
		ctx,
		pool,
		fmt.Sprintf("document-owner-b-%d@example.com", uniqueSuffix),
	)

	// 即使测试中途失败，仍按“文档 → 用户”的外键顺序清理数据。
	t.Cleanup(func() {
		cleanupContext, cleanupCancel := context.WithTimeout(
			context.Background(),
			5*time.Second,
		)
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

	sessionRepository := postgresrepository.NewAuthSessionRepository(pool)
	tokenManager := sessioninfrastructure.NewTokenGenerator()
	ownerACookie := createDocumentOwnerIntegrationSession(
		t,
		ctx,
		sessionRepository,
		tokenManager,
		ownerAID,
	)
	ownerBCookie := createDocumentOwnerIntegrationSession(
		t,
		ctx,
		sessionRepository,
		tokenManager,
		ownerBID,
	)

	storageRoot := t.TempDir()
	storage, err := filestorage.NewLocalStorage(storageRoot, 1024*1024)
	if err != nil {
		t.Fatalf("create local storage: %v", err)
	}
	repository := postgresrepository.NewScopedDocumentRepository(pool)
	logger := slog.New(slog.NewJSONHandler(io.Discard, nil))
	sessionService, err := authapplication.NewSessionService(
		sessionRepository,
		tokenManager,
		time.Now,
	)
	if err != nil {
		t.Fatalf("create session service: %v", err)
	}

	gin.SetMode(gin.TestMode)
	router := api.NewRouter(logger)
	protectedRoutes := router.Group("")
	protectedRoutes.Use(api.NewAuthMiddleware(sessionService, logger).Require)
	api.NewDocumentHandler(
		documentapplication.NewService(repository),
		logger,
	).RegisterRoutes(protectedRoutes)
	api.NewDocumentUploadHandler(
		documentapplication.NewUploadService(repository, storage),
		1024*1024,
	).RegisterRoutes(protectedRoutes)
	api.NewDocumentListHandler(
		documentapplication.NewListService(repository),
	).RegisterRoutes(protectedRoutes)
	api.NewDocumentDeleteHandler(
		documentapplication.NewDeleteService(repository, storage),
	).RegisterRoutes(protectedRoutes)

	unauthenticatedResponse := performDocumentOwnerRequest(
		router,
		http.MethodGet,
		"/documents",
		nil,
		"",
		"",
	)
	if unauthenticatedResponse.Code != http.StatusUnauthorized {
		t.Fatalf(
			"unauthenticated list status=%d want=%d body=%s",
			unauthenticatedResponse.Code,
			http.StatusUnauthorized,
			unauthenticatedResponse.Body.String(),
		)
	}

	uploadResponse := performDocumentOwnerUpload(
		t,
		router,
		ownerACookie,
		"owner-a.md",
		"# Owner A\n\nOnly owner A may read this document.",
	)
	if uploadResponse.Code != http.StatusCreated {
		t.Fatalf(
			"owner A upload status=%d want=%d body=%s",
			uploadResponse.Code,
			http.StatusCreated,
			uploadResponse.Body.String(),
		)
	}
	if strings.Contains(uploadResponse.Body.String(), "owner_user_id") {
		t.Fatalf("upload response leaked internal owner field: %s", uploadResponse.Body.String())
	}

	var uploadedDocument struct {
		ID           int64  `json:"id"`
		OriginalName string `json:"original_name"`
	}
	if err := json.Unmarshal(uploadResponse.Body.Bytes(), &uploadedDocument); err != nil {
		t.Fatalf("decode upload response: %v", err)
	}
	if uploadedDocument.ID <= 0 || uploadedDocument.OriginalName != "owner-a.md" {
		t.Fatalf("unexpected upload response: %+v", uploadedDocument)
	}

	var storedOwnerID int64
	var storagePath string
	if err := pool.QueryRow(
		ctx,
		"SELECT owner_user_id, storage_path FROM documents WHERE id = $1",
		uploadedDocument.ID,
	).Scan(&storedOwnerID, &storagePath); err != nil {
		t.Fatalf("query uploaded document owner: %v", err)
	}
	if storedOwnerID != ownerAID {
		t.Fatalf("stored owner_user_id=%d want=%d", storedOwnerID, ownerAID)
	}
	absoluteStoragePath := filepath.Join(
		storageRoot,
		filepath.FromSlash(storagePath),
	)
	if _, err := os.Stat(absoluteStoragePath); err != nil {
		t.Fatalf("uploaded physical file is not available: %v", err)
	}

	ownerAList := performDocumentOwnerRequest(
		router,
		http.MethodGet,
		"/documents",
		nil,
		ownerACookie,
		"",
	)
	assertDocumentOwnerListCount(t, ownerAList, 1)

	ownerBList := performDocumentOwnerRequest(
		router,
		http.MethodGet,
		"/documents",
		nil,
		ownerBCookie,
		"",
	)
	assertDocumentOwnerListCount(t, ownerBList, 0)

	documentPath := fmt.Sprintf("/documents/%d", uploadedDocument.ID)
	ownerBGet := performDocumentOwnerRequest(
		router,
		http.MethodGet,
		documentPath,
		nil,
		ownerBCookie,
		"",
	)
	if ownerBGet.Code != http.StatusNotFound {
		t.Fatalf("owner B get status=%d want=404 body=%s", ownerBGet.Code, ownerBGet.Body.String())
	}
	ownerBDelete := performDocumentOwnerRequest(
		router,
		http.MethodDelete,
		documentPath,
		nil,
		ownerBCookie,
		"",
	)
	if ownerBDelete.Code != http.StatusNotFound {
		t.Fatalf("owner B delete status=%d want=404 body=%s", ownerBDelete.Code, ownerBDelete.Body.String())
	}

	ownerAGet := performDocumentOwnerRequest(
		router,
		http.MethodGet,
		documentPath,
		nil,
		ownerACookie,
		"",
	)
	if ownerAGet.Code != http.StatusOK {
		t.Fatalf("owner A get status=%d want=200 body=%s", ownerAGet.Code, ownerAGet.Body.String())
	}
	if strings.Contains(ownerAGet.Body.String(), "owner_user_id") {
		t.Fatalf("detail response leaked internal owner field: %s", ownerAGet.Body.String())
	}

	ownerADelete := performDocumentOwnerRequest(
		router,
		http.MethodDelete,
		documentPath,
		nil,
		ownerACookie,
		"",
	)
	if ownerADelete.Code != http.StatusNoContent || ownerADelete.Body.Len() != 0 {
		t.Fatalf(
			"owner A delete status=%d body=%q want empty 204",
			ownerADelete.Code,
			ownerADelete.Body.String(),
		)
	}

	var remainingDocuments int
	if err := pool.QueryRow(
		ctx,
		"SELECT COUNT(*) FROM documents WHERE id = $1",
		uploadedDocument.ID,
	).Scan(&remainingDocuments); err != nil {
		t.Fatalf("count deleted document: %v", err)
	}
	if remainingDocuments != 0 {
		t.Fatalf("remaining document rows=%d want=0", remainingDocuments)
	}
	if _, err := os.Stat(absoluteStoragePath); !os.IsNotExist(err) {
		t.Fatalf("deleted physical file still exists or cannot be checked: %v", err)
	}
}

// openDocumentOwnerIntegrationPool 连接测试数据库并应用最新迁移。
func openDocumentOwnerIntegrationPool(
	t *testing.T,
	ctx context.Context,
) *pgxpool.Pool {
	t.Helper()
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
	return pool
}

// insertDocumentOwnerIntegrationUser 创建一个已验证且可登录的测试用户。
func insertDocumentOwnerIntegrationUser(
	t *testing.T,
	ctx context.Context,
	pool *pgxpool.Pool,
	email string,
) int64 {
	t.Helper()
	var userID int64
	err := pool.QueryRow(
		ctx,
		`
			INSERT INTO users (
				email, email_verified_at, display_name, password_hash
			)
			VALUES ($1, CURRENT_TIMESTAMP, 'Document Owner', '$argon2id$integration-test')
			RETURNING id
		`,
		email,
	).Scan(&userID)
	if err != nil {
		t.Fatalf("insert integration user %q: %v", email, err)
	}
	return userID
}

// createDocumentOwnerIntegrationSession 创建真实数据库 Session，返回浏览器 Cookie 值。
func createDocumentOwnerIntegrationSession(
	t *testing.T,
	ctx context.Context,
	repository *postgresrepository.AuthSessionRepository,
	tokenManager *sessioninfrastructure.TokenGenerator,
	userID int64,
) string {
	t.Helper()
	pair, err := tokenManager.Generate()
	if err != nil {
		t.Fatalf("generate session token: %v", err)
	}
	now := time.Now().UTC()
	if _, err := repository.CreateSession(
		ctx,
		authapplication.SessionRecord{
			UserID:    userID,
			TokenHash: pair.Hash,
			ExpiresAt: now.Add(time.Hour),
			CreatedAt: now,
		},
	); err != nil {
		t.Fatalf("create session for user %d: %v", userID, err)
	}
	return pair.Raw
}

// performDocumentOwnerUpload 构造真实 multipart/form-data 上传请求。
func performDocumentOwnerUpload(
	t *testing.T,
	router http.Handler,
	rawToken string,
	fileName string,
	content string,
) *httptest.ResponseRecorder {
	t.Helper()
	var body bytes.Buffer
	writer := multipart.NewWriter(&body)
	part, err := writer.CreateFormFile("file", fileName)
	if err != nil {
		t.Fatalf("create multipart file part: %v", err)
	}
	if _, err := io.WriteString(part, content); err != nil {
		t.Fatalf("write multipart file content: %v", err)
	}
	if err := writer.Close(); err != nil {
		t.Fatalf("close multipart writer: %v", err)
	}
	return performDocumentOwnerRequest(
		router,
		http.MethodPost,
		"/documents",
		&body,
		rawToken,
		writer.FormDataContentType(),
	)
}

// performDocumentOwnerRequest 发送带可选 Session Cookie 的测试请求。
func performDocumentOwnerRequest(
	router http.Handler,
	method string,
	path string,
	body io.Reader,
	rawToken string,
	contentType string,
) *httptest.ResponseRecorder {
	request := httptest.NewRequest(method, path, body)
	if rawToken != "" {
		request.AddCookie(&http.Cookie{
			Name:  integrationSessionCookieName,
			Value: rawToken,
		})
	}
	if contentType != "" {
		request.Header.Set("Content-Type", contentType)
	}
	response := httptest.NewRecorder()
	router.ServeHTTP(response, request)
	return response
}

// assertDocumentOwnerListCount 核对列表状态码、结果数量和内部字段隔离。
func assertDocumentOwnerListCount(
	t *testing.T,
	response *httptest.ResponseRecorder,
	wantedCount int,
) {
	t.Helper()
	if response.Code != http.StatusOK {
		t.Fatalf("list status=%d want=200 body=%s", response.Code, response.Body.String())
	}
	if strings.Contains(response.Body.String(), "owner_user_id") {
		t.Fatalf("list response leaked internal owner field: %s", response.Body.String())
	}
	var result struct {
		Documents  []json.RawMessage `json:"documents"`
		Pagination struct {
			Total int64 `json:"total"`
		} `json:"pagination"`
	}
	if err := json.Unmarshal(response.Body.Bytes(), &result); err != nil {
		t.Fatalf("decode list response: %v", err)
	}
	if len(result.Documents) != wantedCount || result.Pagination.Total != int64(wantedCount) {
		t.Fatalf(
			"list count=%d total=%d want=%d body=%s",
			len(result.Documents),
			result.Pagination.Total,
			wantedCount,
			response.Body.String(),
		)
	}
}
