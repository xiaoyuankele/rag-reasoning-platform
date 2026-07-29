package postgres_test

import (
	"context"
	"errors"
	"fmt"
	"os"
	"strings"
	"testing"
	"time"

	"rag-reasoning-platform/backend/internal/config"
	documentdomain "rag-reasoning-platform/backend/internal/domain/document"
	"rag-reasoning-platform/backend/internal/infrastructure/database"
	postgresrepository "rag-reasoning-platform/backend/internal/infrastructure/postgres"
	"rag-reasoning-platform/backend/migrations"
)

// TestChunkRepositoryReplaceAndList 使用真实 PostgreSQL 验证文本块替换、
// 稳定排序、事务回滚、文档不存在以及外键级联删除。
func TestChunkRepositoryReplaceAndList(t *testing.T) {
	if os.Getenv("RUN_DATABASE_TESTS") != "1" {
		t.Skip("set RUN_DATABASE_TESTS=1 to run PostgreSQL integration tests")
	}

	ctx, cancel := context.WithTimeout(
		context.Background(),
		30*time.Second,
	)
	defer cancel()

	databaseConfig, err := config.LoadDatabase()
	if err != nil {
		t.Fatalf("load database configuration: %v", err)
	}

	pool, err := database.Open(
		ctx,
		databaseConfig.ConnectionString(),
	)
	if err != nil {
		t.Fatalf("open database: %v", err)
	}
	defer pool.Close()

	if err := database.Migrate(ctx, pool, migrations.Files); err != nil {
		t.Fatalf("migrate database: %v", err)
	}

	documentRepository := postgresrepository.NewDocumentRepository(pool)
	chunkRepository := postgresrepository.NewChunkRepository(pool)
	uniqueValue := time.Now().UnixNano()

	createdDocument, err := documentRepository.Create(
		ctx,
		documentdomain.CreateInput{
			OriginalName: "chunk-repository-test.md",
			StoragePath: fmt.Sprintf(
				"integration-tests/chunks-%d.md",
				uniqueValue,
			),
			MIMEType:  "text/markdown",
			SizeBytes: 1024,
			SHA256:    strings.Repeat("c", 64),
		},
	)
	if err != nil {
		t.Fatalf("create chunk test document: %v", err)
	}
	defer func() {
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
			t.Errorf("clean up chunk test document: %v", cleanupErr)
		}
	}()

	firstChunks := []documentdomain.ChunkInput{
		{Index: 0, Content: "first chunk"},
		{Index: 1, Content: "second chunk"},
		{Index: 2, Content: "third chunk"},
	}
	if err := chunkRepository.ReplaceForDocument(
		ctx,
		createdDocument.ID,
		firstChunks,
	); err != nil {
		t.Fatalf("replace first document chunks: %v", err)
	}

	foundChunks, err := chunkRepository.ListByDocumentID(
		ctx,
		createdDocument.ID,
	)
	if err != nil {
		t.Fatalf("list first document chunks: %v", err)
	}
	assertChunkContents(t, foundChunks, firstChunks)

	replacementChunks := []documentdomain.ChunkInput{
		{Index: 0, Content: "replacement first chunk"},
		{Index: 1, Content: "replacement second chunk"},
	}
	if err := chunkRepository.ReplaceForDocument(
		ctx,
		createdDocument.ID,
		replacementChunks,
	); err != nil {
		t.Fatalf("replace document chunks again: %v", err)
	}

	foundChunks, err = chunkRepository.ListByDocumentID(
		ctx,
		createdDocument.ID,
	)
	if err != nil {
		t.Fatalf("list replacement document chunks: %v", err)
	}
	assertChunkContents(t, foundChunks, replacementChunks)

	// 重复 chunk_index 违反唯一约束，整个替换事务必须回滚，
	// 因此原来的 replacementChunks 仍然存在。
	err = chunkRepository.ReplaceForDocument(
		ctx,
		createdDocument.ID,
		[]documentdomain.ChunkInput{
			{Index: 0, Content: "duplicate A"},
			{Index: 0, Content: "duplicate B"},
		},
	)
	if err == nil {
		t.Fatal("duplicate chunk indexes must return an error")
	}

	foundChunks, err = chunkRepository.ListByDocumentID(
		ctx,
		createdDocument.ID,
	)
	if err != nil {
		t.Fatalf("list chunks after rolled-back replacement: %v", err)
	}
	assertChunkContents(t, foundChunks, replacementChunks)

	const missingDocumentID int64 = -1
	err = chunkRepository.ReplaceForDocument(
		ctx,
		missingDocumentID,
		nil,
	)
	if !errors.Is(err, documentdomain.ErrNotFound) {
		t.Fatalf(
			"missing ReplaceForDocument() error = %v, want ErrNotFound",
			err,
		)
	}

	_, err = chunkRepository.ListByDocumentID(
		ctx,
		missingDocumentID,
	)
	if !errors.Is(err, documentdomain.ErrNotFound) {
		t.Fatalf(
			"missing ListByDocumentID() error = %v, want ErrNotFound",
			err,
		)
	}

	if err := documentRepository.Delete(
		ctx,
		createdDocument.ID,
	); err != nil {
		t.Fatalf("delete chunk test document: %v", err)
	}

	var remainingChunks int
	if err := pool.QueryRow(
		ctx,
		"SELECT COUNT(*) FROM text_chunks WHERE document_id = $1",
		createdDocument.ID,
	).Scan(&remainingChunks); err != nil {
		t.Fatalf("count chunks after document deletion: %v", err)
	}
	if remainingChunks != 0 {
		t.Fatalf(
			"remaining chunks after document deletion = %d, want 0",
			remainingChunks,
		)
	}
}

func assertChunkContents(
	t *testing.T,
	actual []documentdomain.TextChunk,
	expected []documentdomain.ChunkInput,
) {
	t.Helper()

	if len(actual) != len(expected) {
		t.Fatalf(
			"chunk count = %d, want %d",
			len(actual),
			len(expected),
		)
	}

	for index := range expected {
		if actual[index].ID <= 0 {
			t.Fatalf("chunk %d ID must be positive", index)
		}
		if actual[index].Index != expected[index].Index ||
			actual[index].Content != expected[index].Content {
			t.Fatalf(
				"chunk %d = %+v, want index=%d content=%q",
				index,
				actual[index],
				expected[index].Index,
				expected[index].Content,
			)
		}
		if actual[index].CreatedAt.IsZero() {
			t.Fatalf("chunk %d created_at must be set", index)
		}
	}
}
