package integration_test

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"testing"
	"time"

	documentapplication "rag-reasoning-platform/backend/internal/application/document"
	"rag-reasoning-platform/backend/internal/config"
	accessdomain "rag-reasoning-platform/backend/internal/domain/access"
	documentdomain "rag-reasoning-platform/backend/internal/domain/document"
	"rag-reasoning-platform/backend/internal/infrastructure/database"
	"rag-reasoning-platform/backend/internal/infrastructure/filestorage"
	postgresrepository "rag-reasoning-platform/backend/internal/infrastructure/postgres"
	"rag-reasoning-platform/backend/internal/infrastructure/pythonprocessor"
	"rag-reasoning-platform/backend/migrations"
)

// TestPDFProcessorPersistsPageMetadataInPostgreSQL 验证 PDF 主链路中的关键交接：
// 本地文件 -> Python 分页提取 -> Go 统一 ChunkInput -> PostgreSQL TextChunk。
//
// 这是跨进程、跨语言、跨数据库的集成测试，默认跳过。运行时需要同时设置：
// RUN_PYTHON_TESTS=1、RUN_DATABASE_TESTS=1 和有效的 DB_PASSWORD。
func TestPDFProcessorPersistsPageMetadataInPostgreSQL(t *testing.T) {
	if os.Getenv("RUN_PYTHON_TESTS") != "1" {
		t.Skip("set RUN_PYTHON_TESTS=1 to run Go/Python integration tests")
	}
	if os.Getenv("RUN_DATABASE_TESTS") != "1" {
		t.Skip("set RUN_DATABASE_TESTS=1 to run PostgreSQL integration tests")
	}

	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	pythonExecutable := os.Getenv("PYTHON_EXECUTABLE")
	if pythonExecutable == "" {
		pythonExecutable = "python"
	}

	repositoryRoot := integrationRepositoryRoot(t)
	aiRoot := filepath.Join(repositoryRoot, "ai")
	pythonSourceRoot := filepath.Join(aiRoot, "src")
	fixturePath := filepath.Join(t.TempDir(), "page-metadata-test.pdf")
	writeIntegrationTextPDF(t, pythonExecutable, aiRoot, fixturePath)

	storage, err := filestorage.NewLocalStorage(
		t.TempDir(),
		5*1024*1024,
	)
	if err != nil {
		t.Fatalf("create local storage: %v", err)
	}

	fixture, err := os.Open(fixturePath)
	if err != nil {
		t.Fatalf("open PDF fixture: %v", err)
	}
	storedFile, saveErr := storage.Save(
		ctx,
		"page-metadata-test.pdf",
		fixture,
	)
	closeErr := fixture.Close()
	if saveErr != nil {
		t.Fatalf("save PDF fixture: %v", saveErr)
	}
	if closeErr != nil {
		t.Fatalf("close PDF fixture: %v", closeErr)
	}
	t.Cleanup(func() {
		cleanupContext, cleanupCancel := context.WithTimeout(
			context.Background(),
			5*time.Second,
		)
		defer cleanupCancel()

		if err := storage.Delete(
			cleanupContext,
			storedFile.StoragePath,
		); err != nil {
			t.Errorf("clean up stored PDF: %v", err)
		}
	})

	databaseConfig, err := config.LoadDatabase()
	if err != nil {
		t.Fatalf("load database configuration: %v", err)
	}
	pool, err := database.Open(ctx, databaseConfig.ConnectionString())
	if err != nil {
		t.Fatalf("open database: %v", err)
	}
	// Cleanup 按“后注册、先执行”的顺序运行。连接池清理先注册，
	// 后面注册的测试数据清理就会先执行，避免在 closed pool 上执行 SQL。
	t.Cleanup(pool.Close)

	if err := database.Migrate(ctx, pool, migrations.Files); err != nil {
		t.Fatalf("migrate database: %v", err)
	}

	documentRepository := postgresrepository.NewScopedDocumentRepository(pool)
	var ownerID int64
	if err := pool.QueryRow(
		ctx,
		`
			INSERT INTO users (
				email, email_verified_at, display_name, password_hash
			)
			VALUES ($1, CURRENT_TIMESTAMP, 'PDF Test Owner', '$argon2id$test-hash')
			RETURNING id
		`,
		fmt.Sprintf("pdf-integration-%d@example.com", time.Now().UnixNano()),
	).Scan(&ownerID); err != nil {
		t.Fatalf("create PDF integration owner: %v", err)
	}
	ownerScope, err := accessdomain.NewOwnerScope(ownerID)
	if err != nil {
		t.Fatalf("create PDF integration owner scope: %v", err)
	}
	chunkRepository := postgresrepository.NewChunkRepository(pool)
	createdDocument, err := documentRepository.Create(
		ctx,
		ownerScope,
		documentdomain.CreateInput{
			OriginalName: "page-metadata-test.pdf",
			StoragePath:  storedFile.StoragePath,
			MIMEType:     storedFile.MIMEType,
			SizeBytes:    storedFile.SizeBytes,
			SHA256:       storedFile.SHA256,
		},
	)
	if err != nil {
		t.Fatalf("create PDF document record: %v", err)
	}
	t.Cleanup(func() {
		cleanupContext, cleanupCancel := context.WithTimeout(
			context.Background(),
			5*time.Second,
		)
		defer cleanupCancel()

		if _, err := pool.Exec(
			cleanupContext,
			"DELETE FROM documents WHERE id = $1",
			createdDocument.ID,
		); err != nil {
			t.Errorf("clean up PDF document record: %v", err)
			return
		}
		if _, err := pool.Exec(
			cleanupContext,
			"DELETE FROM users WHERE id = $1",
			ownerID,
		); err != nil {
			t.Errorf("clean up PDF integration owner: %v", err)
		}
	})

	processor, err := pythonprocessor.NewProcessor(
		storage,
		pythonExecutable,
		pythonSourceRoot,
		5*1024*1024,
		10,
	)
	if err != nil {
		t.Fatalf("create Python processor: %v", err)
	}
	result, err := processor.Process(ctx, createdDocument)
	if err != nil {
		t.Fatalf("process PDF through Python: %v", err)
	}

	if err := chunkRepository.ReplaceForDocument(
		ctx,
		createdDocument.ID,
		result.Chunks,
	); err != nil {
		t.Fatalf("persist PDF chunks: %v", err)
	}

	storedChunks, err := chunkRepository.ListByDocumentID(
		ctx,
		createdDocument.ID,
	)
	if err != nil {
		t.Fatalf("list persisted PDF chunks: %v", err)
	}
	if len(storedChunks) != 2 {
		t.Fatalf("persisted chunk count = %d, want 2", len(storedChunks))
	}

	assertPersistedPDFChunk(
		t,
		storedChunks[0],
		createdDocument.ID,
		0,
		"first page content",
		1,
	)
	assertPersistedPDFChunk(
		t,
		storedChunks[1],
		createdDocument.ID,
		1,
		"second page content",
		2,
	)
}

// TestPDFProcessorHandlesTwoDocumentsConcurrently 验证同一个无状态
// PythonProcessor 可以被两个 Go Worker 同时调用。每次调用仍会创建独立的
// Python 子进程、stdin/stdout 缓冲区和 request_id，不会互相串包。
func TestPDFProcessorHandlesTwoDocumentsConcurrently(t *testing.T) {
	if os.Getenv("RUN_PYTHON_TESTS") != "1" {
		t.Skip("set RUN_PYTHON_TESTS=1 to run Go/Python integration tests")
	}

	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	pythonExecutable := os.Getenv("PYTHON_EXECUTABLE")
	if pythonExecutable == "" {
		pythonExecutable = "python"
	}

	repositoryRoot := integrationRepositoryRoot(t)
	aiRoot := filepath.Join(repositoryRoot, "ai")
	storage, documents := prepareConcurrentPDFDocuments(
		t,
		ctx,
		pythonExecutable,
		aiRoot,
	)
	processor, err := pythonprocessor.NewProcessor(
		storage,
		pythonExecutable,
		filepath.Join(aiRoot, "src"),
		5*1024*1024,
		10,
	)
	if err != nil {
		t.Fatalf("create concurrent Python processor: %v", err)
	}
	assertProcessesDocumentsConcurrently(t, ctx, processor, documents)
}

// TestPDFProcessPoolHandlesTwoDocumentsConcurrently 验证两个真实常驻 Python
// 槽位可以同时接收 Go Worker 请求，并各自返回完整且不串包的 chunks。
func TestPDFProcessPoolHandlesTwoDocumentsConcurrently(t *testing.T) {
	if os.Getenv("RUN_PYTHON_TESTS") != "1" {
		t.Skip("set RUN_PYTHON_TESTS=1 to run Go/Python process pool tests")
	}

	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	pythonExecutable := os.Getenv("PYTHON_EXECUTABLE")
	if pythonExecutable == "" {
		pythonExecutable = "python"
	}
	repositoryRoot := integrationRepositoryRoot(t)
	aiRoot := filepath.Join(repositoryRoot, "ai")
	storage, documents := prepareConcurrentPDFDocuments(
		t,
		ctx,
		pythonExecutable,
		aiRoot,
	)
	processPool, err := pythonprocessor.NewProcessPool(
		storage,
		pythonExecutable,
		filepath.Join(aiRoot, "src"),
		5*1024*1024,
		10,
		2,
		20,
	)
	if err != nil {
		t.Fatalf("create concurrent Python process pool: %v", err)
	}
	t.Cleanup(func() {
		if err := processPool.Close(); err != nil {
			t.Errorf("close concurrent Python process pool: %v", err)
		}
	})

	assertProcessesDocumentsConcurrently(t, ctx, processPool, documents)
}

func prepareConcurrentPDFDocuments(
	t *testing.T,
	ctx context.Context,
	pythonExecutable string,
	aiRoot string,
) (*filestorage.LocalStorage, []documentdomain.Document) {
	t.Helper()

	storage, err := filestorage.NewLocalStorage(t.TempDir(), 5*1024*1024)
	if err != nil {
		t.Fatalf("create concurrent Python test storage: %v", err)
	}

	documents := make([]documentdomain.Document, 0, 2)
	for index := 0; index < 2; index++ {
		fixturePath := filepath.Join(
			t.TempDir(),
			fmt.Sprintf("concurrent-%d.pdf", index+1),
		)
		writeIntegrationTextPDF(t, pythonExecutable, aiRoot, fixturePath)

		fixture, err := os.Open(fixturePath)
		if err != nil {
			t.Fatalf("open concurrent PDF fixture %d: %v", index+1, err)
		}
		storedFile, saveErr := storage.Save(
			ctx,
			fmt.Sprintf("concurrent-%d.pdf", index+1),
			fixture,
		)
		closeErr := fixture.Close()
		if saveErr != nil {
			t.Fatalf("save concurrent PDF fixture %d: %v", index+1, saveErr)
		}
		if closeErr != nil {
			t.Fatalf("close concurrent PDF fixture %d: %v", index+1, closeErr)
		}

		documents = append(documents, documentdomain.Document{
			ID:           int64(index + 1),
			OriginalName: fmt.Sprintf("concurrent-%d.pdf", index+1),
			StoragePath:  storedFile.StoragePath,
			MIMEType:     storedFile.MIMEType,
			SizeBytes:    storedFile.SizeBytes,
			Status:       documentdomain.StatusProcessing,
		})
	}

	return storage, documents
}

func assertProcessesDocumentsConcurrently(
	t *testing.T,
	ctx context.Context,
	processor documentapplication.DocumentProcessor,
	documents []documentdomain.Document,
) {
	t.Helper()

	type processingResult struct {
		documentID int64
		result     documentapplication.ProcessingResult
		err        error
	}
	start := make(chan struct{})
	results := make(chan processingResult, len(documents))
	for _, currentDocument := range documents {
		go func(document documentdomain.Document) {
			<-start
			result, processErr := processor.Process(ctx, document)
			results <- processingResult{
				documentID: document.ID,
				result:     result,
				err:        processErr,
			}
		}(currentDocument)
	}
	close(start)

	seenDocumentIDs := make(map[int64]struct{}, len(documents))
	for range documents {
		result := <-results
		if result.err != nil {
			t.Fatalf(
				"process concurrent document %d: %v",
				result.documentID,
				result.err,
			)
		}
		if len(result.result.Chunks) != 2 {
			t.Fatalf(
				"concurrent document %d chunks = %d, want 2",
				result.documentID,
				len(result.result.Chunks),
			)
		}
		seenDocumentIDs[result.documentID] = struct{}{}
	}
	if len(seenDocumentIDs) != len(documents) {
		t.Fatalf(
			"concurrent Python results = %v, want %d documents",
			seenDocumentIDs,
			len(documents),
		)
	}
}

func integrationRepositoryRoot(t *testing.T) string {
	t.Helper()

	_, currentFile, _, ok := runtime.Caller(0)
	if !ok {
		t.Fatal("resolve integration test file path")
	}

	return filepath.Clean(
		filepath.Join(filepath.Dir(currentFile), "..", "..", ".."),
	)
}

func writeIntegrationTextPDF(
	t *testing.T,
	pythonExecutable string,
	aiRoot string,
	outputPath string,
) {
	t.Helper()

	const script = `
from pathlib import Path
import sys

from tests.pdf_test_support import write_text_pdf

write_text_pdf(
    Path(sys.argv[1]),
    ["first page content", "second page content"],
)
`

	command := exec.Command(pythonExecutable, "-c", script, outputPath)
	command.Dir = aiRoot
	command.Env = append(os.Environ(), "PYTHONPATH="+aiRoot)
	output, err := command.CombinedOutput()
	if err != nil {
		t.Fatalf(
			"generate integration PDF fixture: %v; output=%q",
			err,
			string(output),
		)
	}
}

func assertPersistedPDFChunk(
	t *testing.T,
	chunk documentdomain.TextChunk,
	wantDocumentID int64,
	wantIndex int,
	wantContent string,
	wantPage int,
) {
	t.Helper()

	if chunk.DocumentID != wantDocumentID ||
		chunk.Index != wantIndex ||
		chunk.Content != wantContent {
		t.Fatalf(
			"persisted chunk = %+v, want document %d, index %d, content %q",
			chunk,
			wantDocumentID,
			wantIndex,
			wantContent,
		)
	}
	if chunk.PageStart == nil || chunk.PageEnd == nil {
		t.Fatalf("persisted chunk page range = %+v, want page %d", chunk, wantPage)
	}
	if *chunk.PageStart != wantPage || *chunk.PageEnd != wantPage {
		t.Fatalf(
			"persisted chunk page range = %d-%d, want %d-%d",
			*chunk.PageStart,
			*chunk.PageEnd,
			wantPage,
			wantPage,
		)
	}
}
