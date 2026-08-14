package postgres_test

import (
	"context"
	"errors"
	"math"
	"os"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"

	documentdomain "rag-reasoning-platform/backend/internal/domain/document"
	embeddingdomain "rag-reasoning-platform/backend/internal/domain/embedding"
	"rag-reasoning-platform/backend/internal/infrastructure/database"
	postgresrepository "rag-reasoning-platform/backend/internal/infrastructure/postgres"
)

const semanticSearchTestDimensions = 1536

// TestChunkRepositorySearchSimilar 使用真实 pgvector 验证语义检索的数据边界。
//
// Fake 无法证明 <=> 运算符、四表 JOIN、模型隔离和余弦排序是否正确，因此这些规则
// 必须放在真实 PostgreSQL 隔离 schema 中验证。
func TestChunkRepositorySearchSimilar(t *testing.T) {
	if os.Getenv("RUN_DATABASE_TESTS") != "1" {
		t.Skip("set RUN_DATABASE_TESTS=1 to run PostgreSQL integration tests")
	}

	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	pool := openIsolatedDocumentTestPool(t, ctx)
	if err := database.RefreshVectorTypes(ctx, pool); err != nil {
		t.Fatalf("refresh pgvector types for isolated search schema: %v", err)
	}

	documentRepository := postgresrepository.NewDocumentRepository(pool)
	chunkRepository := postgresrepository.NewChunkRepository(pool)
	jobRepository := postgresrepository.NewEmbeddingJobRepository(pool)

	closestDocument := createSemanticSearchFixture(
		t,
		ctx,
		pool,
		documentRepository,
		chunkRepository,
		jobRepository,
		"closest",
		"text-embedding-v4",
		[]semanticChunkFixture{
			{
				content: "与查询方向完全一致的文本块",
				page:    3,
				vector:  semanticTestVector(1, 0),
			},
			{
				content: "与查询较为相近的文本块",
				page:    4,
				vector:  semanticTestVector(0.8, 0.6),
			},
		},
	)
	orthogonalDocument := createSemanticSearchFixture(
		t,
		ctx,
		pool,
		documentRepository,
		chunkRepository,
		jobRepository,
		"orthogonal",
		"text-embedding-v4",
		[]semanticChunkFixture{
			{
				content: "与查询方向正交的文本块",
				page:    8,
				vector:  semanticTestVector(0, 1),
			},
		},
	)

	// 数值完全相同但模型不同，必须被当前 DashScope 查询排除。
	createSemanticSearchFixture(
		t,
		ctx,
		pool,
		documentRepository,
		chunkRepository,
		jobRepository,
		"other-model",
		"text-embedding-3-small",
		[]semanticChunkFixture{
			{
				content: "不同模型生成的相同数字不能混入结果",
				page:    10,
				vector:  semanticTestVector(1, 0),
			},
		},
	)

	// 向量生成后把文档改回 processing，证明查询只暴露 ready 文档。
	notReadyDocument := createSemanticSearchFixture(
		t,
		ctx,
		pool,
		documentRepository,
		chunkRepository,
		jobRepository,
		"not-ready",
		"text-embedding-v4",
		[]semanticChunkFixture{
			{
				content: "未就绪文档不能出现在检索结果中",
				page:    12,
				vector:  semanticTestVector(1, 0),
			},
		},
	)
	setSearchTestDocumentStatus(
		t,
		ctx,
		pool,
		notReadyDocument.ID,
		documentdomain.StatusProcessing,
	)

	t.Run("orders by cosine similarity and filters model and readiness", func(t *testing.T) {
		hits, err := chunkRepository.SearchSimilar(
			ctx,
			documentdomain.SemanticSearchOptions{
				QueryVector: semanticTestVector(1, 0),
				ModelName:   "text-embedding-v4",
				Dimensions:  semanticSearchTestDimensions,
				Limit:       2,
			},
		)
		if err != nil {
			t.Fatalf("SearchSimilar() error = %v, want nil", err)
		}
		if len(hits) != 2 {
			t.Fatalf("SearchSimilar() hits = %d, want 2", len(hits))
		}
		if hits[0].DocumentID != closestDocument.ID || hits[0].ChunkIndex != 0 {
			t.Fatalf("first hit = %+v, want closest document chunk 0", hits[0])
		}
		if hits[1].DocumentID != closestDocument.ID || hits[1].ChunkIndex != 1 {
			t.Fatalf("second hit = %+v, want closest document chunk 1", hits[1])
		}
		assertApproximateFloat(t, "first similarity", hits[0].Similarity, 1)
		assertApproximateFloat(t, "second similarity", hits[1].Similarity, 0.8)
		if hits[0].Title == nil || *hits[0].Title != "closest semantic title" ||
			hits[0].PageStart == nil || *hits[0].PageStart != 3 {
			t.Fatalf("first hit metadata = %+v, want title and physical page", hits[0])
		}
	})

	t.Run("filters by document ID", func(t *testing.T) {
		documentID := orthogonalDocument.ID
		hits, err := chunkRepository.SearchSimilar(
			ctx,
			documentdomain.SemanticSearchOptions{
				QueryVector: semanticTestVector(1, 0),
				ModelName:   "text-embedding-v4",
				Dimensions:  semanticSearchTestDimensions,
				DocumentID:  &documentID,
				Limit:       5,
			},
		)
		if err != nil {
			t.Fatalf("SearchSimilar() filtered error = %v, want nil", err)
		}
		if len(hits) != 1 || hits[0].DocumentID != orthogonalDocument.ID {
			t.Fatalf("filtered hits = %+v, want orthogonal document only", hits)
		}
		assertApproximateFloat(t, "orthogonal similarity", hits[0].Similarity, 0)
	})

	t.Run("returns a non-nil empty slice for unmatched model", func(t *testing.T) {
		hits, err := chunkRepository.SearchSimilar(
			ctx,
			documentdomain.SemanticSearchOptions{
				QueryVector: semanticTestVector(1, 0),
				ModelName:   "model-without-vectors",
				Dimensions:  semanticSearchTestDimensions,
				Limit:       5,
			},
		)
		if err != nil {
			t.Fatalf("SearchSimilar() empty error = %v, want nil", err)
		}
		if hits == nil || len(hits) != 0 {
			t.Fatalf("empty hits = %#v, want non-nil empty slice", hits)
		}
	})
}

// TestChunkRepositoryHasCompleteSemanticEmbeddings 使用真实表关系验证“向量完整”的定义。
//
// 这个测试不是在验证某条 SQL 能否运行，而是在保护跨表业务事实：文档、文本块、
// 向量和生成任务必须完整对应，指定文档才可以进入在线语义检索。
func TestChunkRepositoryHasCompleteSemanticEmbeddings(t *testing.T) {
	if os.Getenv("RUN_DATABASE_TESTS") != "1" {
		t.Skip("set RUN_DATABASE_TESTS=1 to run PostgreSQL integration tests")
	}

	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	pool := openIsolatedDocumentTestPool(t, ctx)
	if err := database.RefreshVectorTypes(ctx, pool); err != nil {
		t.Fatalf("refresh pgvector types for isolated readiness schema: %v", err)
	}

	documentRepository := postgresrepository.NewDocumentRepository(pool)
	chunkRepository := postgresrepository.NewChunkRepository(pool)
	jobRepository := postgresrepository.NewEmbeddingJobRepository(pool)

	completeDocument := createSemanticSearchFixture(
		t,
		ctx,
		pool,
		documentRepository,
		chunkRepository,
		jobRepository,
		"readiness-complete",
		"text-embedding-v4",
		[]semanticChunkFixture{
			{content: "完整向量的第一个文本块", page: 1, vector: semanticTestVector(1, 0)},
			{content: "完整向量的第二个文本块", page: 2, vector: semanticTestVector(0, 1)},
		},
	)

	partialDocument := createSemanticSearchFixture(
		t,
		ctx,
		pool,
		documentRepository,
		chunkRepository,
		jobRepository,
		"readiness-partial",
		"text-embedding-v4",
		[]semanticChunkFixture{
			{content: "保留向量的文本块", page: 3, vector: semanticTestVector(1, 0)},
			{content: "模拟向量丢失的文本块", page: 4, vector: semanticTestVector(0, 1)},
		},
	)
	if _, err := pool.Exec(
		ctx,
		`
			DELETE FROM chunk_embeddings
			WHERE chunk_id = (
				SELECT id
				FROM text_chunks
				WHERE document_id = $1
				ORDER BY chunk_index DESC
				LIMIT 1
			)
		`,
		partialDocument.ID,
	); err != nil {
		t.Fatalf("remove one vector from partial readiness fixture: %v", err)
	}

	emptyDocument := createSearchTestDocument(
		t,
		ctx,
		documentRepository,
		"readiness-empty.pdf",
		"semantic/readiness-empty.pdf",
		"application/pdf",
		"b",
	)
	setSearchTestDocumentStatus(
		t,
		ctx,
		pool,
		emptyDocument.ID,
		documentdomain.StatusReady,
	)

	notReadyDocument := createSemanticSearchFixture(
		t,
		ctx,
		pool,
		documentRepository,
		chunkRepository,
		jobRepository,
		"readiness-document-status",
		"text-embedding-v4",
		[]semanticChunkFixture{
			{content: "文档状态尚未就绪", page: 5, vector: semanticTestVector(1, 0)},
		},
	)
	setSearchTestDocumentStatus(
		t,
		ctx,
		pool,
		notReadyDocument.ID,
		documentdomain.StatusProcessing,
	)

	tests := []struct {
		name       string
		documentID int64
		modelName  string
		dimensions int
		wantReady  bool
	}{
		{name: "complete current embeddings", documentID: completeDocument.ID, modelName: "text-embedding-v4", dimensions: semanticSearchTestDimensions, wantReady: true},
		{name: "one chunk vector is missing", documentID: partialDocument.ID, modelName: "text-embedding-v4", dimensions: semanticSearchTestDimensions, wantReady: false},
		{name: "document has no chunks", documentID: emptyDocument.ID, modelName: "text-embedding-v4", dimensions: semanticSearchTestDimensions, wantReady: false},
		{name: "document status is not ready", documentID: notReadyDocument.ID, modelName: "text-embedding-v4", dimensions: semanticSearchTestDimensions, wantReady: false},
		{name: "model does not match", documentID: completeDocument.ID, modelName: "other-model", dimensions: semanticSearchTestDimensions, wantReady: false},
		{name: "dimensions do not match", documentID: completeDocument.ID, modelName: "text-embedding-v4", dimensions: 3072, wantReady: false},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			ready, err := chunkRepository.HasCompleteSemanticEmbeddings(
				ctx,
				documentdomain.SemanticEmbeddingReadinessOptions{
					DocumentID: test.documentID,
					ModelName:  test.modelName,
					Dimensions: test.dimensions,
				},
			)
			if err != nil {
				t.Fatalf("HasCompleteSemanticEmbeddings() error = %v, want nil", err)
			}
			if ready != test.wantReady {
				t.Fatalf("HasCompleteSemanticEmbeddings() = %t, want %t", ready, test.wantReady)
			}
		})
	}

	_, err := chunkRepository.HasCompleteSemanticEmbeddings(
		ctx,
		documentdomain.SemanticEmbeddingReadinessOptions{
			DocumentID: completeDocument.ID + 1_000_000,
			ModelName:  "text-embedding-v4",
			Dimensions: semanticSearchTestDimensions,
		},
	)
	if !errors.Is(err, documentdomain.ErrNotFound) {
		t.Fatalf("missing document error = %v, want ErrNotFound", err)
	}
}

type semanticChunkFixture struct {
	content string
	page    int
	vector  []float32
}

func createSemanticSearchFixture(
	t *testing.T,
	ctx context.Context,
	pool *pgxpool.Pool,
	documentRepository *postgresrepository.DocumentRepository,
	chunkRepository *postgresrepository.ChunkRepository,
	jobRepository *postgresrepository.EmbeddingJobRepository,
	name string,
	modelName string,
	chunkFixtures []semanticChunkFixture,
) documentdomain.Document {
	t.Helper()

	document := createSearchTestDocument(
		t,
		ctx,
		documentRepository,
		name+".pdf",
		"semantic/"+name+".pdf",
		"application/pdf",
		"a",
	)
	setSearchTestDocumentStatus(t, ctx, pool, document.ID, documentdomain.StatusReady)
	setSearchTestDocumentTitle(t, ctx, pool, document.ID, name+" semantic title")

	chunkInputs := make([]documentdomain.ChunkInput, len(chunkFixtures))
	for index, fixture := range chunkFixtures {
		chunkInputs[index] = documentdomain.ChunkInput{
			Index:     index,
			Content:   fixture.content,
			PageStart: intPointer(fixture.page),
			PageEnd:   intPointer(fixture.page),
		}
	}
	if err := chunkRepository.ReplaceForDocument(ctx, document.ID, chunkInputs); err != nil {
		t.Fatalf("replace semantic chunks for %s: %v", name, err)
	}
	persistedChunks, err := chunkRepository.ListByDocumentID(ctx, document.ID)
	if err != nil {
		t.Fatalf("list semantic chunks for %s: %v", name, err)
	}

	job, err := jobRepository.CreateEmbeddingJob(
		ctx,
		document.ID,
		modelName,
		semanticSearchTestDimensions,
	)
	if err != nil {
		t.Fatalf("create semantic embedding job for %s: %v", name, err)
	}
	markEmbeddingJobProcessing(t, ctx, pool, job.ID)

	vectors := make([]embeddingdomain.ChunkVector, len(chunkFixtures))
	for index, fixture := range chunkFixtures {
		vectors[index] = embeddingdomain.ChunkVector{
			ChunkID: persistedChunks[index].ID,
			Values:  fixture.vector,
		}
	}
	if err := jobRepository.MarkEmbeddingJobSucceeded(
		ctx,
		job.ID,
		embeddingdomain.JobCompletion{
			Vectors:      vectors,
			PromptTokens: len(vectors),
			TotalTokens:  len(vectors),
		},
	); err != nil {
		t.Fatalf("complete semantic embedding job for %s: %v", name, err)
	}

	return document
}

func semanticTestVector(first float32, second float32) []float32 {
	values := make([]float32, semanticSearchTestDimensions)
	values[0] = first
	values[1] = second
	return values
}

func assertApproximateFloat(
	t *testing.T,
	name string,
	actual float64,
	wanted float64,
) {
	t.Helper()
	if math.Abs(actual-wanted) > 0.000001 {
		t.Fatalf("%s = %f, want approximately %f", name, actual, wanted)
	}
}
