package postgres_test

import (
	"context"
	"fmt"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"

	documentdomain "rag-reasoning-platform/backend/internal/domain/document"
	postgresrepository "rag-reasoning-platform/backend/internal/infrastructure/postgres"
)

// TestChunkRepositorySearch 使用真实 PostgreSQL 验证 P3 关键词检索的核心语义。
func TestChunkRepositorySearch(t *testing.T) {
	if os.Getenv("RUN_DATABASE_TESTS") != "1" {
		t.Skip("set RUN_DATABASE_TESTS=1 to run PostgreSQL integration tests")
	}

	ctx, cancel := context.WithTimeout(
		context.Background(),
		30*time.Second,
	)
	defer cancel()

	// 每次测试使用独立 schema，不读取或修改开发库 public 中的真实文档。
	pool := openIsolatedDocumentTestPool(t, ctx)
	documentRepository := newOwnedDocumentFixture(t, ctx, pool)
	chunkRepository := postgresrepository.NewChunkRepository(pool)

	chineseDocument := createSearchTestDocument(
		t,
		ctx,
		documentRepository,
		"磁悬浮论文.pdf",
		"search/chinese.pdf",
		"application/pdf",
		"a",
	)
	englishDocument := createSearchTestDocument(
		t,
		ctx,
		documentRepository,
		"maglev-paper.pdf",
		"search/english.pdf",
		"application/pdf",
		"b",
	)
	processingDocument := createSearchTestDocument(
		t,
		ctx,
		documentRepository,
		"processing.pdf",
		"search/processing.pdf",
		"application/pdf",
		"c",
	)

	setSearchTestDocumentStatus(
		t,
		ctx,
		pool,
		chineseDocument.ID,
		documentdomain.StatusReady,
	)
	chineseTitle := "磁悬浮列车协同控制"
	setSearchTestDocumentTitle(
		t,
		ctx,
		pool,
		chineseDocument.ID,
		chineseTitle,
	)
	setSearchTestDocumentStatus(
		t,
		ctx,
		pool,
		englishDocument.ID,
		documentdomain.StatusReady,
	)
	setSearchTestDocumentStatus(
		t,
		ctx,
		pool,
		processingDocument.ID,
		documentdomain.StatusProcessing,
	)

	if err := chunkRepository.ReplaceForDocument(
		ctx,
		chineseDocument.ID,
		[]documentdomain.ChunkInput{
			{
				Index:     0,
				Content:   "磁悬浮车辆 control 系统研究",
				PageStart: intPointer(3),
				PageEnd:   intPointer(3),
			},
			{
				Index:     1,
				Content:   "第二个 control 结果为 100%_verified",
				PageStart: intPointer(4),
				PageEnd:   intPointer(4),
			},
			{
				Index:     2,
				Content:   `Windows source path C:\research\paper.pdf`,
				PageStart: intPointer(5),
				PageEnd:   intPointer(5),
			},
		},
	); err != nil {
		t.Fatalf("replace Chinese search chunks: %v", err)
	}
	if err := chunkRepository.ReplaceForDocument(
		ctx,
		englishDocument.ID,
		[]documentdomain.ChunkInput{
			{
				Index:     0,
				Content:   "MAGLEV vehicle control stability",
				PageStart: intPointer(8),
				PageEnd:   intPointer(8),
			},
		},
	); err != nil {
		t.Fatalf("replace English search chunks: %v", err)
	}
	if err := chunkRepository.ReplaceForDocument(
		ctx,
		processingDocument.ID,
		[]documentdomain.ChunkInput{
			{
				Index:   0,
				Content: "磁悬浮内容尚未处理完成",
			},
		},
	); err != nil {
		t.Fatalf("replace processing search chunks: %v", err)
	}

	t.Run("matches Chinese substring and filters non-ready document", func(t *testing.T) {
		result, err := chunkRepository.Search(
			ctx,
			documentdomain.SearchOptions{
				Query: "磁悬浮",
				Limit: 10,
			},
		)
		if err != nil {
			t.Fatalf("Search() Chinese error = %v", err)
		}
		if result.Total != 1 || len(result.Hits) != 1 {
			t.Fatalf(
				"Search() Chinese result = total %d, hits %d; want 1/1",
				result.Total,
				len(result.Hits),
			)
		}

		hit := result.Hits[0]
		if hit.DocumentID != chineseDocument.ID ||
			hit.Title == nil || *hit.Title != chineseTitle ||
			hit.OriginalName != chineseDocument.OriginalName ||
			hit.ChunkIndex != 0 ||
			hit.PageStart == nil || *hit.PageStart != 3 {
			t.Fatalf("Search() Chinese hit = %+v, want Chinese page 3", hit)
		}
	})

	t.Run("filters matches by document ID", func(t *testing.T) {
		documentID := chineseDocument.ID
		result, err := chunkRepository.Search(
			ctx,
			documentdomain.SearchOptions{
				Query:      "control",
				DocumentID: &documentID,
				Limit:      10,
			},
		)
		if err != nil {
			t.Fatalf("Search() document filter error = %v", err)
		}
		if result.Total != 2 || len(result.Hits) != 2 {
			t.Fatalf(
				"Search() document filter result = total %d, hits %d; want 2/2",
				result.Total,
				len(result.Hits),
			)
		}
		for _, hit := range result.Hits {
			if hit.DocumentID != chineseDocument.ID {
				t.Fatalf(
					"Search() filtered document ID = %d, want %d",
					hit.DocumentID,
					chineseDocument.ID,
				)
			}
		}
	})

	t.Run("matches English without case sensitivity", func(t *testing.T) {
		result, err := chunkRepository.Search(
			ctx,
			documentdomain.SearchOptions{Query: "maglev", Limit: 10},
		)
		if err != nil {
			t.Fatalf("Search() English error = %v", err)
		}
		if result.Total != 1 || len(result.Hits) != 1 {
			t.Fatalf(
				"Search() English result = total %d, hits %d; want 1/1",
				result.Total,
				len(result.Hits),
			)
		}
		if result.Hits[0].DocumentID != englishDocument.ID {
			t.Fatalf(
				"Search() English document ID = %d, want %d",
				result.Hits[0].DocumentID,
				englishDocument.ID,
			)
		}
	})

	t.Run("treats SQL wildcard characters literally", func(t *testing.T) {
		result, err := chunkRepository.Search(
			ctx,
			documentdomain.SearchOptions{Query: "%_", Limit: 10},
		)
		if err != nil {
			t.Fatalf("Search() literal wildcard error = %v", err)
		}
		if result.Total != 1 || len(result.Hits) != 1 {
			t.Fatalf(
				"Search() literal wildcard result = total %d, hits %d; want 1/1",
				result.Total,
				len(result.Hits),
			)
		}
		if result.Hits[0].ChunkIndex != 1 {
			t.Fatalf(
				"Search() literal wildcard chunk index = %d, want 1",
				result.Hits[0].ChunkIndex,
			)
		}
	})

	t.Run("treats the SQL escape character literally", func(t *testing.T) {
		result, err := chunkRepository.Search(
			ctx,
			documentdomain.SearchOptions{Query: `\research\`, Limit: 10},
		)
		if err != nil {
			t.Fatalf("Search() literal escape character error = %v", err)
		}
		if result.Total != 1 || len(result.Hits) != 1 {
			t.Fatalf(
				"Search() literal escape result = total %d, hits %d; want 1/1",
				result.Total,
				len(result.Hits),
			)
		}
		if result.Hits[0].ChunkIndex != 2 {
			t.Fatalf(
				"Search() literal escape chunk index = %d, want 2",
				result.Hits[0].ChunkIndex,
			)
		}
	})

	t.Run("returns stable paginated hits and full total", func(t *testing.T) {
		result, err := chunkRepository.Search(
			ctx,
			documentdomain.SearchOptions{
				Query:  "control",
				Limit:  2,
				Offset: 1,
			},
		)
		if err != nil {
			t.Fatalf("Search() pagination error = %v", err)
		}
		if result.Total != 3 || len(result.Hits) != 2 {
			t.Fatalf(
				"Search() pagination result = total %d, hits %d; want 3/2",
				result.Total,
				len(result.Hits),
			)
		}
		if result.Hits[0].DocumentID != chineseDocument.ID ||
			result.Hits[0].ChunkIndex != 0 ||
			result.Hits[1].DocumentID != chineseDocument.ID ||
			result.Hits[1].ChunkIndex != 1 {
			t.Fatalf("Search() paginated hit order = %+v", result.Hits)
		}
	})

	t.Run("returns non-nil empty hits", func(t *testing.T) {
		result, err := chunkRepository.Search(
			ctx,
			documentdomain.SearchOptions{Query: "不存在的关键词", Limit: 10},
		)
		if err != nil {
			t.Fatalf("Search() empty error = %v", err)
		}
		if result.Hits == nil {
			t.Fatal("Search() empty hits = nil, want non-nil empty slice")
		}
		if result.Total != 0 || len(result.Hits) != 0 {
			t.Fatalf(
				"Search() empty result = total %d, hits %d; want 0/0",
				result.Total,
				len(result.Hits),
			)
		}
	})
}

func createSearchTestDocument(
	t *testing.T,
	ctx context.Context,
	repository *ownedDocumentFixture,
	originalName string,
	storagePath string,
	mimeType string,
	hashCharacter string,
) documentdomain.Document {
	t.Helper()

	createdDocument, err := repository.Create(
		ctx,
		documentdomain.CreateInput{
			OriginalName: originalName,
			StoragePath: fmt.Sprintf(
				"%s-%d",
				storagePath,
				time.Now().UnixNano(),
			),
			MIMEType:  mimeType,
			SizeBytes: 1024,
			SHA256:    strings.Repeat(hashCharacter, 64),
		},
	)
	if err != nil {
		t.Fatalf("create search test document %q: %v", originalName, err)
	}

	return createdDocument
}

func setSearchTestDocumentStatus(
	t *testing.T,
	ctx context.Context,
	pool *pgxpool.Pool,
	documentID int64,
	status documentdomain.Status,
) {
	t.Helper()

	commandTag, err := pool.Exec(
		ctx,
		"UPDATE documents SET status = $1 WHERE id = $2",
		status,
		documentID,
	)
	if err != nil {
		t.Fatalf("set search test document %d status: %v", documentID, err)
	}
	if commandTag.RowsAffected() != 1 {
		t.Fatalf(
			"set search test document %d affected %d rows, want 1",
			documentID,
			commandTag.RowsAffected(),
		)
	}
}

func setSearchTestDocumentTitle(
	t *testing.T,
	ctx context.Context,
	pool *pgxpool.Pool,
	documentID int64,
	title string,
) {
	t.Helper()

	commandTag, err := pool.Exec(
		ctx,
		"UPDATE documents SET title = $1 WHERE id = $2",
		title,
		documentID,
	)
	if err != nil {
		t.Fatalf("set search test document %d title: %v", documentID, err)
	}
	if commandTag.RowsAffected() != 1 {
		t.Fatalf(
			"set search test document %d title affected %d rows, want 1",
			documentID,
			commandTag.RowsAffected(),
		)
	}
}
