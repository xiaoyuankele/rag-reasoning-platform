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

// TestSearchOwnerHTTPWithPostgreSQL 验证关键词搜索的完整用户隔离链路：
// Cookie → AuthMiddleware → OwnerScope → SearchService → PostgreSQL。
func TestSearchOwnerHTTPWithPostgreSQL(t *testing.T) {
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
		fmt.Sprintf("search-http-a-%d@example.com", uniqueSuffix),
	)
	ownerBID := insertDocumentOwnerIntegrationUser(
		t,
		ctx,
		pool,
		fmt.Sprintf("search-http-b-%d@example.com", uniqueSuffix),
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
	ownerB, err := accessdomain.NewOwnerScope(ownerBID)
	if err != nil {
		t.Fatalf("create owner B scope: %v", err)
	}
	scopedDocuments := postgresrepository.NewScopedDocumentRepository(pool)
	ownerADocument, err := scopedDocuments.Create(
		ctx,
		ownerA,
		documentdomain.CreateInput{
			OriginalName: "owner-a-search.md",
			StoragePath:  fmt.Sprintf("documents/owner-a-search-%d.md", uniqueSuffix),
			MIMEType:     "text/markdown",
			SizeBytes:    32,
			SHA256:       strings.Repeat("d", 64),
		},
	)
	if err != nil {
		t.Fatalf("create owner A document: %v", err)
	}
	ownerBDocument, err := scopedDocuments.Create(
		ctx,
		ownerB,
		documentdomain.CreateInput{
			OriginalName: "owner-b-search.md",
			StoragePath:  fmt.Sprintf("documents/owner-b-search-%d.md", uniqueSuffix),
			MIMEType:     "text/markdown",
			SizeBytes:    32,
			SHA256:       strings.Repeat("e", 64),
		},
	)
	if err != nil {
		t.Fatalf("create owner B document: %v", err)
	}

	systemChunks := postgresrepository.NewChunkRepository(pool)
	if err := systemChunks.ReplaceForDocument(
		ctx,
		ownerADocument.ID,
		[]documentdomain.ChunkInput{{
			Index:   0,
			Content: "shared-http-keyword evidence visible only to owner A",
		}},
	); err != nil {
		t.Fatalf("persist owner A search chunk: %v", err)
	}
	if err := systemChunks.ReplaceForDocument(
		ctx,
		ownerBDocument.ID,
		[]documentdomain.ChunkInput{{
			Index:   0,
			Content: "shared-http-keyword evidence visible only to owner B",
		}},
	); err != nil {
		t.Fatalf("persist owner B search chunk: %v", err)
	}
	if _, err := pool.Exec(
		ctx,
		"UPDATE documents SET status = 'ready' WHERE id = ANY($1::bigint[])",
		[]int64{ownerADocument.ID, ownerBDocument.ID},
	); err != nil {
		t.Fatalf("mark search documents ready: %v", err)
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

	logger := slog.New(slog.NewJSONHandler(io.Discard, nil))
	scopedChunks := postgresrepository.NewScopedChunkRepository(pool)
	gin.SetMode(gin.TestMode)
	router := api.NewRouter(logger)
	protectedRoutes := router.Group("")
	protectedRoutes.Use(api.NewAuthMiddleware(sessionService, logger).Require)
	api.NewDocumentSearchHandler(
		documentapplication.NewSearchService(scopedChunks),
	).RegisterRoutes(protectedRoutes)

	const searchPath = "/search?q=shared-http-keyword"
	unauthenticated := performDocumentOwnerRequest(
		router, http.MethodGet, searchPath, nil, "", "",
	)
	assertProcessingOwnerStatus(
		t, unauthenticated.Code, http.StatusUnauthorized, unauthenticated.Body.String(),
	)

	ownerAResponse := performDocumentOwnerRequest(
		router, http.MethodGet, searchPath, nil, ownerACookie, "",
	)
	assertSearchOwnerResponse(
		t,
		ownerAResponse.Code,
		ownerAResponse.Body.Bytes(),
		ownerADocument.ID,
		"owner A",
	)
	ownerBResponse := performDocumentOwnerRequest(
		router, http.MethodGet, searchPath, nil, ownerBCookie, "",
	)
	assertSearchOwnerResponse(
		t,
		ownerBResponse.Code,
		ownerBResponse.Body.Bytes(),
		ownerBDocument.ID,
		"owner B",
	)

	foreignFilterPath := fmt.Sprintf(
		"/search?q=shared-http-keyword&document_id=%d",
		ownerADocument.ID,
	)
	ownerBForeignFilter := performDocumentOwnerRequest(
		router, http.MethodGet, foreignFilterPath, nil, ownerBCookie, "",
	)
	if ownerBForeignFilter.Code != http.StatusOK {
		t.Fatalf(
			"owner B foreign filter status=%d want=200 body=%s",
			ownerBForeignFilter.Code,
			ownerBForeignFilter.Body.String(),
		)
	}
	var emptyResult struct {
		Results    []json.RawMessage `json:"results"`
		Pagination struct {
			Total int64 `json:"total"`
		} `json:"pagination"`
	}
	if err := json.Unmarshal(ownerBForeignFilter.Body.Bytes(), &emptyResult); err != nil {
		t.Fatalf("decode foreign document filter response: %v", err)
	}
	if len(emptyResult.Results) != 0 || emptyResult.Pagination.Total != 0 {
		t.Fatalf(
			"owner B foreign document filter leaked results: %s",
			ownerBForeignFilter.Body.String(),
		)
	}
}

// assertSearchOwnerResponse 核对搜索结果只包含预期用户的单个文档。
func assertSearchOwnerResponse(
	t *testing.T,
	status int,
	body []byte,
	wantedDocumentID int64,
	wantedOwnerMarker string,
) {
	t.Helper()
	if status != http.StatusOK {
		t.Fatalf("search status=%d want=200 body=%s", status, string(body))
	}
	if strings.Contains(string(body), "owner_user_id") {
		t.Fatalf("search response leaked internal owner field: %s", string(body))
	}
	var result struct {
		Results []struct {
			DocumentID int64  `json:"document_id"`
			Content    string `json:"content"`
		} `json:"results"`
		Pagination struct {
			Total int64 `json:"total"`
		} `json:"pagination"`
	}
	if err := json.Unmarshal(body, &result); err != nil {
		t.Fatalf("decode owner search response: %v", err)
	}
	if len(result.Results) != 1 || result.Pagination.Total != 1 ||
		result.Results[0].DocumentID != wantedDocumentID ||
		!strings.Contains(result.Results[0].Content, wantedOwnerMarker) {
		t.Fatalf(
			"search result=%+v total=%d want document=%d marker=%q",
			result.Results,
			result.Pagination.Total,
			wantedDocumentID,
			wantedOwnerMarker,
		)
	}
}
