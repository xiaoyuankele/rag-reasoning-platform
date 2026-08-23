package postgres_test

import (
	"context"
	"errors"
	"os"
	"reflect"
	"testing"
	"time"

	accessdomain "rag-reasoning-platform/backend/internal/domain/access"
	documentdomain "rag-reasoning-platform/backend/internal/domain/document"
	postgresrepository "rag-reasoning-platform/backend/internal/infrastructure/postgres"
)

func testProcessingAdmissionLimits() documentdomain.ProcessingJobAdmissionLimits {
	return documentdomain.ProcessingJobAdmissionLimits{
		MaxActiveJobsPerOwner: 100,
		MaxActiveJobsGlobal:   500,
	}
}

// TestScopedProcessingJobRepositoryEnforcesOwnerBoundary 验证任务创建与查询
// 都通过关联文档的 owner_user_id 执行隔离。
func TestScopedProcessingJobRepositoryEnforcesOwnerBoundary(t *testing.T) {
	if os.Getenv("RUN_DATABASE_TESTS") != "1" {
		t.Skip("set RUN_DATABASE_TESTS=1 to run PostgreSQL integration tests")
	}

	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	pool := openIsolatedDocumentTestPool(t, ctx)

	ownerAID := insertScopedRepositoryUser(t, ctx, pool, "job-owner-a@example.com")
	ownerBID := insertScopedRepositoryUser(t, ctx, pool, "job-owner-b@example.com")
	ownerA, _ := accessdomain.NewOwnerScope(ownerAID)
	ownerB, _ := accessdomain.NewOwnerScope(ownerBID)
	documents := postgresrepository.NewScopedDocumentRepository(pool)
	jobs := postgresrepository.NewScopedProcessingJobRepository(
		pool,
		testProcessingAdmissionLimits(),
	)

	ownerADocument, err := documents.Create(
		ctx,
		ownerA,
		scopedDocumentInput("owner-a.md", "scoped-tests/job-owner-a.md", "a"),
	)
	if err != nil {
		t.Fatalf("create owner A document: %v", err)
	}
	ownerBDocument, err := documents.Create(
		ctx,
		ownerB,
		scopedDocumentInput("owner-b.md", "scoped-tests/job-owner-b.md", "b"),
	)
	if err != nil {
		t.Fatalf("create owner B document: %v", err)
	}

	createdJob, err := jobs.CreateProcessingJob(ctx, ownerA, ownerADocument.ID)
	if err != nil {
		t.Fatalf("CreateProcessingJob(owner A) error = %v", err)
	}
	if createdJob.DocumentID != ownerADocument.ID ||
		createdJob.Status != documentdomain.ProcessingJobStatusQueued {
		t.Fatalf("CreateProcessingJob(owner A) = %+v, want queued owner A job", createdJob)
	}
	if _, err := jobs.CreateProcessingJob(ctx, ownerB, ownerADocument.ID); !errors.Is(err, documentdomain.ErrNotFound) {
		t.Fatalf("CreateProcessingJob(owner B, owner A document) error = %v, want ErrNotFound", err)
	}
	if _, err := jobs.CreateProcessingJob(ctx, ownerA, ownerBDocument.ID); !errors.Is(err, documentdomain.ErrNotFound) {
		t.Fatalf("CreateProcessingJob(owner A, owner B document) error = %v, want ErrNotFound", err)
	}
	if _, err := jobs.CreateProcessingJob(ctx, ownerA, ownerADocument.ID); !errors.Is(err, documentdomain.ErrActiveProcessingJobExists) {
		t.Fatalf("CreateProcessingJob(duplicate) error = %v, want ErrActiveProcessingJobExists", err)
	}

	foundJob, err := jobs.GetProcessingJobByID(ctx, ownerA, createdJob.ID)
	if err != nil || foundJob.ID != createdJob.ID {
		t.Fatalf("GetProcessingJobByID(owner A) = (%+v, %v), want created job", foundJob, err)
	}
	if _, err := jobs.GetProcessingJobByID(ctx, ownerB, createdJob.ID); !errors.Is(err, documentdomain.ErrProcessingJobNotFound) {
		t.Fatalf("GetProcessingJobByID(owner B) error = %v, want ErrProcessingJobNotFound", err)
	}
}

// TestScopedChunkRepositoryEnforcesOwnerBoundary 验证 count 与分页数据 SQL
// 都只能读取当前 OwnerScope 对应的文档文本块。
func TestScopedChunkRepositoryEnforcesOwnerBoundary(t *testing.T) {
	if os.Getenv("RUN_DATABASE_TESTS") != "1" {
		t.Skip("set RUN_DATABASE_TESTS=1 to run PostgreSQL integration tests")
	}

	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	pool := openIsolatedDocumentTestPool(t, ctx)

	ownerAID := insertScopedRepositoryUser(t, ctx, pool, "chunk-owner-a@example.com")
	ownerBID := insertScopedRepositoryUser(t, ctx, pool, "chunk-owner-b@example.com")
	ownerA, _ := accessdomain.NewOwnerScope(ownerAID)
	ownerB, _ := accessdomain.NewOwnerScope(ownerBID)
	documents := postgresrepository.NewScopedDocumentRepository(pool)
	systemChunks := postgresrepository.NewChunkRepository(pool)
	scopedChunks := postgresrepository.NewScopedChunkRepository(pool)

	ownerADocument, err := documents.Create(
		ctx,
		ownerA,
		scopedDocumentInput("owner-a.md", "scoped-tests/chunk-owner-a.md", "c"),
	)
	if err != nil {
		t.Fatalf("create owner A document: %v", err)
	}
	ownerBDocument, err := documents.Create(
		ctx,
		ownerB,
		scopedDocumentInput("owner-b.md", "scoped-tests/chunk-owner-b.md", "d"),
	)
	if err != nil {
		t.Fatalf("create owner B document: %v", err)
	}

	ownerAChunks := []documentdomain.ChunkInput{
		{Index: 0, Content: "owner A first chunk"},
		{Index: 1, Content: "owner A second chunk"},
	}
	if err := systemChunks.ReplaceForDocument(ctx, ownerADocument.ID, ownerAChunks); err != nil {
		t.Fatalf("replace owner A chunks: %v", err)
	}
	if err := systemChunks.ReplaceForDocument(
		ctx,
		ownerBDocument.ID,
		[]documentdomain.ChunkInput{{Index: 0, Content: "owner B private chunk"}},
	); err != nil {
		t.Fatalf("replace owner B chunks: %v", err)
	}

	result, err := scopedChunks.ListPageByDocumentID(
		ctx,
		ownerA,
		ownerADocument.ID,
		documentdomain.ChunkPageOptions{Limit: 20},
	)
	if err != nil {
		t.Fatalf("ListPageByDocumentID(owner A) error = %v", err)
	}
	actualContents := make([]string, 0, len(result.Chunks))
	for _, chunk := range result.Chunks {
		actualContents = append(actualContents, chunk.Content)
	}
	expectedContents := []string{"owner A first chunk", "owner A second chunk"}
	if result.Total != 2 || !reflect.DeepEqual(actualContents, expectedContents) {
		t.Fatalf("ListPageByDocumentID(owner A) = %+v contents=%v", result, actualContents)
	}
	if _, err := scopedChunks.ListPageByDocumentID(
		ctx,
		ownerB,
		ownerADocument.ID,
		documentdomain.ChunkPageOptions{Limit: 20},
	); !errors.Is(err, documentdomain.ErrNotFound) {
		t.Fatalf("ListPageByDocumentID(owner B, owner A document) error = %v, want ErrNotFound", err)
	}
}

// TestScopedChunkRepositorySearchEnforcesOwnerBoundary 验证相同关键词存在于
// 两个用户的文档时，count、结果和可选 document_id 都不会跨越 OwnerScope。
func TestScopedChunkRepositorySearchEnforcesOwnerBoundary(t *testing.T) {
	if os.Getenv("RUN_DATABASE_TESTS") != "1" {
		t.Skip("set RUN_DATABASE_TESTS=1 to run PostgreSQL integration tests")
	}

	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	pool := openIsolatedDocumentTestPool(t, ctx)

	ownerAID := insertScopedRepositoryUser(t, ctx, pool, "search-owner-a@example.com")
	ownerBID := insertScopedRepositoryUser(t, ctx, pool, "search-owner-b@example.com")
	ownerA, _ := accessdomain.NewOwnerScope(ownerAID)
	ownerB, _ := accessdomain.NewOwnerScope(ownerBID)
	documents := postgresrepository.NewScopedDocumentRepository(pool)
	systemChunks := postgresrepository.NewChunkRepository(pool)
	scopedChunks := postgresrepository.NewScopedChunkRepository(pool)

	ownerADocument, err := documents.Create(
		ctx,
		ownerA,
		scopedDocumentInput("owner-a-search.md", "scoped-tests/search-owner-a.md", "e"),
	)
	if err != nil {
		t.Fatalf("create owner A search document: %v", err)
	}
	ownerBDocument, err := documents.Create(
		ctx,
		ownerB,
		scopedDocumentInput("owner-b-search.md", "scoped-tests/search-owner-b.md", "f"),
	)
	if err != nil {
		t.Fatalf("create owner B search document: %v", err)
	}

	for _, documentID := range []int64{ownerADocument.ID, ownerBDocument.ID} {
		if _, err := pool.Exec(
			ctx,
			"UPDATE documents SET status = 'ready' WHERE id = $1",
			documentID,
		); err != nil {
			t.Fatalf("mark search document %d ready: %v", documentID, err)
		}
	}
	if err := systemChunks.ReplaceForDocument(
		ctx,
		ownerADocument.ID,
		[]documentdomain.ChunkInput{
			{Index: 0, Content: "shared-keyword owner A first evidence"},
			{Index: 1, Content: "shared-keyword owner A second evidence"},
		},
	); err != nil {
		t.Fatalf("replace owner A search chunks: %v", err)
	}
	if err := systemChunks.ReplaceForDocument(
		ctx,
		ownerBDocument.ID,
		[]documentdomain.ChunkInput{
			{Index: 0, Content: "shared-keyword owner B private evidence"},
		},
	); err != nil {
		t.Fatalf("replace owner B search chunks: %v", err)
	}

	ownerAResult, err := scopedChunks.Search(
		ctx,
		ownerA,
		documentdomain.SearchOptions{Query: "shared-keyword", Limit: 10},
	)
	if err != nil {
		t.Fatalf("Search(owner A) error = %v", err)
	}
	if ownerAResult.Total != 2 || len(ownerAResult.Hits) != 2 {
		t.Fatalf("Search(owner A) = %+v, want two owner A hits", ownerAResult)
	}
	for _, hit := range ownerAResult.Hits {
		if hit.DocumentID != ownerADocument.ID {
			t.Fatalf("Search(owner A) leaked hit = %+v", hit)
		}
	}

	ownerBResult, err := scopedChunks.Search(
		ctx,
		ownerB,
		documentdomain.SearchOptions{Query: "shared-keyword", Limit: 10},
	)
	if err != nil {
		t.Fatalf("Search(owner B) error = %v", err)
	}
	if ownerBResult.Total != 1 || len(ownerBResult.Hits) != 1 ||
		ownerBResult.Hits[0].DocumentID != ownerBDocument.ID {
		t.Fatalf("Search(owner B) = %+v, want one owner B hit", ownerBResult)
	}

	foreignDocumentID := ownerADocument.ID
	foreignFilterResult, err := scopedChunks.Search(
		ctx,
		ownerB,
		documentdomain.SearchOptions{
			Query:      "shared-keyword",
			DocumentID: &foreignDocumentID,
			Limit:      10,
		},
	)
	if err != nil {
		t.Fatalf("Search(owner B, owner A document) error = %v", err)
	}
	if foreignFilterResult.Total != 0 || len(foreignFilterResult.Hits) != 0 {
		t.Fatalf(
			"Search(owner B, owner A document) = %+v, want empty result",
			foreignFilterResult,
		)
	}
}

// TestScopedProcessingAndChunkRepositoriesRejectInvalidScope 验证无效 Scope
// 在访问数据库前就会被拒绝。
func TestScopedProcessingAndChunkRepositoriesRejectInvalidScope(t *testing.T) {
	var invalidScope accessdomain.OwnerScope
	ctx := context.Background()
	jobs := postgresrepository.NewScopedProcessingJobRepository(
		nil,
		testProcessingAdmissionLimits(),
	)
	chunks := postgresrepository.NewScopedChunkRepository(nil)

	if _, err := jobs.CreateProcessingJob(ctx, invalidScope, 1); !errors.Is(err, accessdomain.ErrInvalidOwnerScope) {
		t.Fatalf("CreateProcessingJob(invalid scope) error = %v, want ErrInvalidOwnerScope", err)
	}
	if _, err := jobs.GetProcessingJobByID(ctx, invalidScope, 1); !errors.Is(err, accessdomain.ErrInvalidOwnerScope) {
		t.Fatalf("GetProcessingJobByID(invalid scope) error = %v, want ErrInvalidOwnerScope", err)
	}
	if _, err := chunks.ListPageByDocumentID(
		ctx,
		invalidScope,
		1,
		documentdomain.ChunkPageOptions{Limit: 20},
	); !errors.Is(err, accessdomain.ErrInvalidOwnerScope) {
		t.Fatalf("ListPageByDocumentID(invalid scope) error = %v, want ErrInvalidOwnerScope", err)
	}
	if _, err := chunks.Search(
		ctx,
		invalidScope,
		documentdomain.SearchOptions{Query: "private", Limit: 20},
	); !errors.Is(err, accessdomain.ErrInvalidOwnerScope) {
		t.Fatalf("Search(invalid scope) error = %v, want ErrInvalidOwnerScope", err)
	}
}
