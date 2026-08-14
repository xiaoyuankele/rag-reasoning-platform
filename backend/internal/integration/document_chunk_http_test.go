package integration_test

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/gin-gonic/gin"

	"rag-reasoning-platform/backend/internal/api"
	applicationdocument "rag-reasoning-platform/backend/internal/application/document"
	"rag-reasoning-platform/backend/internal/config"
	documentdomain "rag-reasoning-platform/backend/internal/domain/document"
	"rag-reasoning-platform/backend/internal/infrastructure/database"
	postgresrepository "rag-reasoning-platform/backend/internal/infrastructure/postgres"
	"rag-reasoning-platform/backend/migrations"
)

// TestDocumentChunkHTTPWithPostgreSQL 验证真实 HTTP → Application → PostgreSQL
// 纵向链路，同时确认非 ready 文档不会暴露旧文本块。
func TestDocumentChunkHTTPWithPostgreSQL(t *testing.T) {
	if os.Getenv("RUN_DATABASE_TESTS") != "1" {
		t.Skip("set RUN_DATABASE_TESTS=1 to run PostgreSQL integration tests")
	}

	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()

	databaseConfig, err := config.LoadDatabase()
	if err != nil {
		t.Fatalf("load database configuration: %v", err)
	}
	pool, err := database.Open(ctx, databaseConfig.ConnectionString())
	if err != nil {
		t.Fatalf("open database: %v", err)
	}
	defer pool.Close()
	if err := database.Migrate(ctx, pool, migrations.Files); err != nil {
		t.Fatalf("migrate database: %v", err)
	}

	documentRepository := postgresrepository.NewDocumentRepository(pool)
	chunkRepository := postgresrepository.NewChunkRepository(pool)
	createdDocument, err := documentRepository.Create(
		ctx,
		documentdomain.CreateInput{
			OriginalName: "chunk-http-test.md",
			StoragePath: fmt.Sprintf(
				"integration-tests/chunk-http-%d.md",
				time.Now().UnixNano(),
			),
			MIMEType:  "text/markdown",
			SizeBytes: 256,
			SHA256:    strings.Repeat("d", 64),
		},
	)
	if err != nil {
		t.Fatalf("create document: %v", err)
	}
	defer func() {
		cleanupContext, cleanupCancel := context.WithTimeout(
			context.Background(),
			5*time.Second,
		)
		defer cleanupCancel()
		_, _ = pool.Exec(
			cleanupContext,
			"DELETE FROM documents WHERE id = $1",
			createdDocument.ID,
		)
	}()

	chunks := []documentdomain.ChunkInput{
		{Index: 0, Content: "first chunk"},
		{Index: 1, Content: "second chunk"},
		{Index: 2, Content: "third chunk"},
	}
	if err := chunkRepository.ReplaceForDocument(
		ctx,
		createdDocument.ID,
		chunks,
	); err != nil {
		t.Fatalf("replace document chunks: %v", err)
	}
	if _, err := pool.Exec(
		ctx,
		"UPDATE documents SET status = $2 WHERE id = $1",
		createdDocument.ID,
		documentdomain.StatusReady,
	); err != nil {
		t.Fatalf("mark document ready: %v", err)
	}

	service := applicationdocument.NewChunkListService(
		documentRepository,
		chunkRepository,
	)
	handler := api.NewDocumentChunkHandler(service)
	gin.SetMode(gin.TestMode)
	router := gin.New()
	handler.RegisterRoutes(router)

	request := httptest.NewRequest(
		http.MethodGet,
		fmt.Sprintf(
			"/documents/%d/chunks?page=2&page_size=2",
			createdDocument.ID,
		),
		nil,
	)
	response := httptest.NewRecorder()
	router.ServeHTTP(response, request)

	if response.Code != http.StatusOK {
		t.Fatalf("ready document status = %d, body = %s", response.Code, response.Body.String())
	}
	var successResponse struct {
		DocumentID int64 `json:"document_id"`
		Chunks     []struct {
			ChunkIndex int    `json:"chunk_index"`
			Content    string `json:"content"`
		} `json:"chunks"`
		Pagination struct {
			Page       int64 `json:"page"`
			PageSize   int64 `json:"page_size"`
			Total      int64 `json:"total"`
			TotalPages int64 `json:"total_pages"`
		} `json:"pagination"`
	}
	if err := json.Unmarshal(response.Body.Bytes(), &successResponse); err != nil {
		t.Fatalf("decode successful response: %v", err)
	}
	if successResponse.DocumentID != createdDocument.ID ||
		len(successResponse.Chunks) != 1 ||
		successResponse.Chunks[0].ChunkIndex != 2 ||
		successResponse.Chunks[0].Content != "third chunk" {
		t.Fatalf("unexpected chunk response: %+v", successResponse)
	}
	if successResponse.Pagination.Page != 2 ||
		successResponse.Pagination.PageSize != 2 ||
		successResponse.Pagination.Total != 3 ||
		successResponse.Pagination.TotalPages != 2 {
		t.Fatalf("unexpected pagination: %+v", successResponse.Pagination)
	}

	if _, err := pool.Exec(
		ctx,
		"UPDATE documents SET status = $2 WHERE id = $1",
		createdDocument.ID,
		documentdomain.StatusProcessing,
	); err != nil {
		t.Fatalf("mark document processing: %v", err)
	}

	conflictRequest := httptest.NewRequest(
		http.MethodGet,
		fmt.Sprintf("/documents/%d/chunks", createdDocument.ID),
		nil,
	)
	conflictResponse := httptest.NewRecorder()
	router.ServeHTTP(conflictResponse, conflictRequest)
	if conflictResponse.Code != http.StatusConflict {
		t.Fatalf(
			"processing document status = %d, want %d; body = %s",
			conflictResponse.Code,
			http.StatusConflict,
			conflictResponse.Body.String(),
		)
	}
}
