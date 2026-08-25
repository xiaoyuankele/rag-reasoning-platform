package postgres_test

import (
	"context"
	"errors"
	"fmt"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"

	accessdomain "rag-reasoning-platform/backend/internal/domain/access"
	documentdomain "rag-reasoning-platform/backend/internal/domain/document"
	postgresrepository "rag-reasoning-platform/backend/internal/infrastructure/postgres"
)

// TestScopedDocumentRepositoryEnforcesOwnerBoundary 使用两个真实用户验证：
// 所有者可以操作文档，其他用户看到的结果与文档不存在完全一致。
func TestScopedDocumentRepositoryEnforcesOwnerBoundary(t *testing.T) {
	if os.Getenv("RUN_DATABASE_TESTS") != "1" {
		t.Skip("set RUN_DATABASE_TESTS=1 to run PostgreSQL integration tests")
	}

	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	pool := openIsolatedDocumentTestPool(t, ctx)
	repository := postgresrepository.NewScopedDocumentRepository(pool)
	revisionRepository := postgresrepository.NewCorpusRevisionRepository(pool)

	ownerAID := insertScopedRepositoryUser(t, ctx, pool, "owner-a@example.com")
	ownerBID := insertScopedRepositoryUser(t, ctx, pool, "owner-b@example.com")
	ownerA, err := accessdomain.NewOwnerScope(ownerAID)
	if err != nil {
		t.Fatalf("create owner A scope: %v", err)
	}
	ownerB, err := accessdomain.NewOwnerScope(ownerBID)
	if err != nil {
		t.Fatalf("create owner B scope: %v", err)
	}

	createdA, err := repository.Create(ctx, ownerA, scopedDocumentInput(
		"owner-a.pdf",
		"scoped-tests/owner-a.pdf",
		"a",
	))
	if err != nil {
		t.Fatalf("Create(owner A) error = %v", err)
	}
	if createdA.OwnerUserID != ownerAID {
		t.Fatalf("Create(owner A) OwnerUserID = %d, want %d", createdA.OwnerUserID, ownerAID)
	}

	if _, err := repository.Create(ctx, ownerA, scopedDocumentInput(
		"owner-a-second.pdf",
		"scoped-tests/owner-a-second.pdf",
		"b",
	)); err != nil {
		t.Fatalf("Create(owner A second) error = %v", err)
	}
	if _, err := repository.Create(ctx, ownerB, scopedDocumentInput(
		"owner-b.pdf",
		"scoped-tests/owner-b.pdf",
		"c",
	)); err != nil {
		t.Fatalf("Create(owner B) error = %v", err)
	}

	foundA, err := repository.GetByID(ctx, ownerA, createdA.ID)
	if err != nil {
		t.Fatalf("GetByID(owner A) error = %v", err)
	}
	if foundA.ID != createdA.ID || foundA.OwnerUserID != ownerAID {
		t.Fatalf("GetByID(owner A) = %+v, want owned document", foundA)
	}

	_, err = repository.GetByID(ctx, ownerB, createdA.ID)
	if !errors.Is(err, documentdomain.ErrNotFound) {
		t.Fatalf("GetByID(owner B, owner A document) error = %v, want ErrNotFound", err)
	}

	listA, err := repository.List(ctx, ownerA, documentdomain.ListOptions{Limit: 10})
	if err != nil {
		t.Fatalf("List(owner A) error = %v", err)
	}
	if listA.Total != 2 || len(listA.Documents) != 2 {
		t.Fatalf("List(owner A) = %+v, want two owner A documents", listA)
	}
	for _, listedDocument := range listA.Documents {
		if listedDocument.OwnerUserID != ownerAID {
			t.Fatalf("List(owner A) leaked document = %+v", listedDocument)
		}
	}

	listB, err := repository.List(ctx, ownerB, documentdomain.ListOptions{Limit: 10})
	if err != nil {
		t.Fatalf("List(owner B) error = %v", err)
	}
	if listB.Total != 1 || len(listB.Documents) != 1 ||
		listB.Documents[0].OwnerUserID != ownerBID {
		t.Fatalf("List(owner B) = %+v, want one owner B document", listB)
	}

	if err := repository.Delete(ctx, ownerB, createdA.ID); !errors.Is(err, documentdomain.ErrNotFound) {
		t.Fatalf("Delete(owner B, owner A document) error = %v, want ErrNotFound", err)
	}
	if _, err := repository.GetByID(ctx, ownerA, createdA.ID); err != nil {
		t.Fatalf("owner A document disappeared after rejected delete: %v", err)
	}
	if err := repository.Delete(ctx, ownerA, createdA.ID); err != nil {
		t.Fatalf("Delete(owner A) error = %v", err)
	}
	ownerARevision, err := revisionRepository.GetCorpusRevision(ctx, ownerA)
	if err != nil {
		t.Fatalf("GetCorpusRevision(owner A) error = %v", err)
	}
	if ownerARevision != 2 {
		t.Fatalf("owner A corpus revision after delete = %d, want 2", ownerARevision)
	}
	ownerBRevision, err := revisionRepository.GetCorpusRevision(ctx, ownerB)
	if err != nil {
		t.Fatalf("GetCorpusRevision(owner B) error = %v", err)
	}
	if ownerBRevision != 1 {
		t.Fatalf("owner B corpus revision = %d, want unchanged 1", ownerBRevision)
	}
	if _, err := repository.GetByID(ctx, ownerA, createdA.ID); !errors.Is(err, documentdomain.ErrNotFound) {
		t.Fatalf("GetByID() after owner delete error = %v, want ErrNotFound", err)
	}
}

func TestScopedDocumentRepositoryRejectsInvalidScopeBeforeDatabaseAccess(t *testing.T) {
	repository := postgresrepository.NewScopedDocumentRepository(nil)
	var invalidScope accessdomain.OwnerScope
	ctx := context.Background()

	if _, err := repository.Create(ctx, invalidScope, documentdomain.CreateInput{}); !errors.Is(err, accessdomain.ErrInvalidOwnerScope) {
		t.Fatalf("Create(invalid scope) error = %v, want ErrInvalidOwnerScope", err)
	}
	if _, err := repository.CreateOrGetBySHA256(ctx, invalidScope, documentdomain.CreateInput{}); !errors.Is(err, accessdomain.ErrInvalidOwnerScope) {
		t.Fatalf("CreateOrGetBySHA256(invalid scope) error = %v, want ErrInvalidOwnerScope", err)
	}
	if _, err := repository.FindBySHA256AndSize(ctx, invalidScope, strings.Repeat("a", 64), 1); !errors.Is(err, accessdomain.ErrInvalidOwnerScope) {
		t.Fatalf("FindBySHA256AndSize(invalid scope) error = %v, want ErrInvalidOwnerScope", err)
	}
	if _, err := repository.GetByID(ctx, invalidScope, 1); !errors.Is(err, accessdomain.ErrInvalidOwnerScope) {
		t.Fatalf("GetByID(invalid scope) error = %v, want ErrInvalidOwnerScope", err)
	}
	if _, err := repository.List(ctx, invalidScope, documentdomain.ListOptions{}); !errors.Is(err, accessdomain.ErrInvalidOwnerScope) {
		t.Fatalf("List(invalid scope) error = %v, want ErrInvalidOwnerScope", err)
	}
	if err := repository.Delete(ctx, invalidScope, 1); !errors.Is(err, accessdomain.ErrInvalidOwnerScope) {
		t.Fatalf("Delete(invalid scope) error = %v, want ErrInvalidOwnerScope", err)
	}
}

// TestScopedDocumentRepositoryCreateOrGetBySHA256 验证同一用户内去重、
// 不同用户隔离，以及并发上传时数据库唯一索引仍然只允许一条记录。
func TestScopedDocumentRepositoryCreateOrGetBySHA256(t *testing.T) {
	if os.Getenv("RUN_DATABASE_TESTS") != "1" {
		t.Skip("set RUN_DATABASE_TESTS=1 to run PostgreSQL integration tests")
	}

	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	pool := openIsolatedDocumentTestPool(t, ctx)
	repository := postgresrepository.NewScopedDocumentRepository(pool)

	uniqueSuffix := time.Now().UnixNano()
	ownerAID := insertScopedRepositoryUser(
		t,
		ctx,
		pool,
		fmt.Sprintf("dedup-owner-a-%d@example.com", uniqueSuffix),
	)
	ownerBID := insertScopedRepositoryUser(
		t,
		ctx,
		pool,
		fmt.Sprintf("dedup-owner-b-%d@example.com", uniqueSuffix),
	)
	ownerA, err := accessdomain.NewOwnerScope(ownerAID)
	if err != nil {
		t.Fatalf("create owner A scope: %v", err)
	}
	ownerB, err := accessdomain.NewOwnerScope(ownerBID)
	if err != nil {
		t.Fatalf("create owner B scope: %v", err)
	}

	const workers = 6
	sharedHash := strings.Repeat("f", 64)
	type concurrentResult struct {
		result documentdomain.CreateOrGetResult
		err    error
	}
	results := make(chan concurrentResult, workers)
	for workerIndex := 0; workerIndex < workers; workerIndex++ {
		go func(index int) {
			result, err := repository.CreateOrGetBySHA256(
				ctx,
				ownerA,
				documentdomain.CreateInput{
					OriginalName: fmt.Sprintf("copy-%d.pdf", index),
					StoragePath:  fmt.Sprintf("dedup-tests/copy-%d.pdf", index),
					MIMEType:     "application/pdf",
					SizeBytes:    1024,
					SHA256:       sharedHash,
				},
			)
			results <- concurrentResult{result: result, err: err}
		}(workerIndex)
	}

	createdCount := 0
	var sharedDocumentID int64
	for resultIndex := 0; resultIndex < workers; resultIndex++ {
		concurrentUpload := <-results
		if concurrentUpload.err != nil {
			t.Fatalf("concurrent CreateOrGetBySHA256() error = %v", concurrentUpload.err)
		}
		if concurrentUpload.result.Created {
			createdCount++
		}
		if sharedDocumentID == 0 {
			sharedDocumentID = concurrentUpload.result.Document.ID
		}
		if concurrentUpload.result.Document.ID != sharedDocumentID {
			t.Fatalf(
				"concurrent result document ID = %d, want shared ID %d",
				concurrentUpload.result.Document.ID,
				sharedDocumentID,
			)
		}
	}
	if createdCount != 1 {
		t.Fatalf("Created=true count = %d, want 1", createdCount)
	}

	var ownerACount int
	if err := pool.QueryRow(
		ctx,
		"SELECT COUNT(*) FROM documents WHERE owner_user_id = $1 AND sha256 = $2",
		ownerAID,
		sharedHash,
	).Scan(&ownerACount); err != nil {
		t.Fatalf("count owner A duplicate rows: %v", err)
	}
	if ownerACount != 1 {
		t.Fatalf("owner A duplicate row count = %d, want 1", ownerACount)
	}

	foundDuplicate, err := repository.FindBySHA256AndSize(
		ctx,
		ownerA,
		sharedHash,
		1024,
	)
	if err != nil {
		t.Fatalf("FindBySHA256AndSize(owner A) error = %v", err)
	}
	if foundDuplicate.ID != sharedDocumentID || foundDuplicate.OwnerUserID != ownerAID {
		t.Fatalf("FindBySHA256AndSize(owner A) = %+v, want document %d", foundDuplicate, sharedDocumentID)
	}
	if _, err := repository.FindBySHA256AndSize(ctx, ownerA, sharedHash, 2048); !errors.Is(err, documentdomain.ErrNotFound) {
		t.Fatalf("FindBySHA256AndSize(wrong size) error = %v, want ErrNotFound", err)
	}
	if _, err := repository.FindBySHA256AndSize(ctx, ownerB, sharedHash, 1024); !errors.Is(err, documentdomain.ErrNotFound) {
		t.Fatalf("FindBySHA256AndSize(other owner) error = %v, want ErrNotFound", err)
	}

	otherOwnerResult, err := repository.CreateOrGetBySHA256(
		ctx,
		ownerB,
		documentdomain.CreateInput{
			OriginalName: "other-owner.pdf",
			StoragePath:  "dedup-tests/other-owner.pdf",
			MIMEType:     "application/pdf",
			SizeBytes:    1024,
			SHA256:       sharedHash,
		},
	)
	if err != nil {
		t.Fatalf("other owner CreateOrGetBySHA256() error = %v", err)
	}
	if !otherOwnerResult.Created {
		t.Fatal("same content for another owner was incorrectly treated as duplicate")
	}
	if otherOwnerResult.Document.ID == sharedDocumentID {
		t.Fatal("different owners unexpectedly share the same document record")
	}
}

func insertScopedRepositoryUser(
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
			VALUES ($1, CURRENT_TIMESTAMP, 'Scoped Owner', '$argon2id$test-hash')
			RETURNING id
		`,
		email,
	).Scan(&userID)
	if err != nil {
		t.Fatalf("insert scoped repository user %q: %v", email, err)
	}
	return userID
}

func scopedDocumentInput(
	originalName string,
	storagePath string,
	hashCharacter string,
) documentdomain.CreateInput {
	return documentdomain.CreateInput{
		OriginalName: originalName,
		StoragePath:  storagePath,
		MIMEType:     "application/pdf",
		SizeBytes:    1024,
		SHA256:       strings.Repeat(hashCharacter, 64),
	}
}
