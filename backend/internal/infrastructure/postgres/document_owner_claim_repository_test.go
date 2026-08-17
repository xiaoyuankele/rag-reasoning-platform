package postgres_test

import (
	"context"
	"errors"
	"os"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"

	postgresrepository "rag-reasoning-platform/backend/internal/infrastructure/postgres"
	documentowner "rag-reasoning-platform/backend/internal/maintenance/documentowner"
)

// TestDocumentOwnerClaimRepositoryUsesOneAtomicClaim 验证：
// dry-run 不写数据、数量不一致会回滚、数量一致才一次性认领全部无主文档。
func TestDocumentOwnerClaimRepositoryUsesOneAtomicClaim(t *testing.T) {
	if os.Getenv("RUN_DATABASE_TESTS") != "1" {
		t.Skip("set RUN_DATABASE_TESTS=1 to run PostgreSQL integration tests")
	}

	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	pool := openIsolatedDocumentTestPool(t, ctx)
	// 认领命令只服务于升级前仍允许 NULL owner_user_id 的旧数据库。
	// 隔离测试先执行全部新迁移，因此这里显式恢复 B6 旧表状态来验证过渡工具。
	if _, err := pool.Exec(
		ctx,
		"ALTER TABLE documents ALTER COLUMN owner_user_id DROP NOT NULL",
	); err != nil {
		t.Fatalf("prepare legacy owner-claim schema: %v", err)
	}
	repository := postgresrepository.NewDocumentOwnerClaimRepository(pool)

	targetUserID := insertScopedRepositoryUser(
		t,
		ctx,
		pool,
		"owner-claim-target@example.com",
	)
	otherUserID := insertScopedRepositoryUser(
		t,
		ctx,
		pool,
		"owner-claim-other@example.com",
	)

	insertOwnerClaimDocument(t, ctx, pool, "unowned-a.pdf", "a", nil)
	insertOwnerClaimDocument(t, ctx, pool, "unowned-b.pdf", "b", nil)
	insertOwnerClaimDocument(t, ctx, pool, "already-owned.pdf", "c", &otherUserID)

	preview, err := repository.PreviewOwnerClaim(ctx, targetUserID)
	if err != nil {
		t.Fatalf("PreviewOwnerClaim() error = %v", err)
	}
	if preview.Target.UserID != targetUserID || preview.UnownedDocuments != 2 {
		t.Fatalf("PreviewOwnerClaim() = %+v, want target and two unowned", preview)
	}
	assertOwnerClaimDocumentCounts(t, ctx, pool, targetUserID, 0, 2)

	// 模拟 dry-run 后数据数量发生变化。仓储必须拒绝执行且不留下部分更新。
	_, err = repository.ClaimUnownedDocuments(ctx, targetUserID, 1)
	var mismatch *documentowner.CountMismatchError
	if !errors.As(err, &mismatch) || mismatch.Expected != 1 || mismatch.Actual != 2 {
		t.Fatalf("ClaimUnownedDocuments(expected=1) error = %v, want 1/2 mismatch", err)
	}
	assertOwnerClaimDocumentCounts(t, ctx, pool, targetUserID, 0, 2)

	result, err := repository.ClaimUnownedDocuments(ctx, targetUserID, 2)
	if err != nil {
		t.Fatalf("ClaimUnownedDocuments(expected=2) error = %v", err)
	}
	if result.ClaimedDocuments != 2 || result.RemainingUnowned != 0 {
		t.Fatalf("ClaimUnownedDocuments() = %+v, want two claimed", result)
	}
	assertOwnerClaimDocumentCounts(t, ctx, pool, targetUserID, 2, 0)
	assertOwnerClaimDocumentCounts(t, ctx, pool, otherUserID, 1, 0)
}

func TestDocumentOwnerClaimRepositoryRejectsMissingOrInactiveTarget(t *testing.T) {
	if os.Getenv("RUN_DATABASE_TESTS") != "1" {
		t.Skip("set RUN_DATABASE_TESTS=1 to run PostgreSQL integration tests")
	}

	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	pool := openIsolatedDocumentTestPool(t, ctx)
	repository := postgresrepository.NewDocumentOwnerClaimRepository(pool)

	if _, err := repository.PreviewOwnerClaim(ctx, 999999); !errors.Is(err, documentowner.ErrTargetNotFound) {
		t.Fatalf("PreviewOwnerClaim(missing) error = %v, want target not found", err)
	}

	disabledUserID := insertScopedRepositoryUser(
		t,
		ctx,
		pool,
		"owner-claim-disabled@example.com",
	)
	if _, err := pool.Exec(
		ctx,
		"UPDATE users SET status = 'disabled' WHERE id = $1",
		disabledUserID,
	); err != nil {
		t.Fatalf("disable owner claim test user: %v", err)
	}
	if _, err := repository.PreviewOwnerClaim(ctx, disabledUserID); !errors.Is(err, documentowner.ErrTargetInactive) {
		t.Fatalf("PreviewOwnerClaim(disabled) error = %v, want inactive target", err)
	}
}

func insertOwnerClaimDocument(
	t *testing.T,
	ctx context.Context,
	pool *pgxpool.Pool,
	originalName string,
	hashCharacter string,
	ownerUserID *int64,
) {
	t.Helper()
	_, err := pool.Exec(
		ctx,
		`
			INSERT INTO documents (
				original_name, storage_path, mime_type,
				size_bytes, sha256, owner_user_id
			)
			VALUES ($1, $2, 'application/pdf', 1024, repeat($3, 64), $4)
		`,
		originalName,
		"owner-claim-tests/"+originalName,
		hashCharacter,
		ownerUserID,
	)
	if err != nil {
		t.Fatalf("insert owner claim document %q: %v", originalName, err)
	}
}

func assertOwnerClaimDocumentCounts(
	t *testing.T,
	ctx context.Context,
	pool *pgxpool.Pool,
	ownerUserID int64,
	wantOwned int64,
	wantUnowned int64,
) {
	t.Helper()
	var owned int64
	var unowned int64
	if err := pool.QueryRow(
		ctx,
		"SELECT COUNT(*) FROM documents WHERE owner_user_id = $1",
		ownerUserID,
	).Scan(&owned); err != nil {
		t.Fatalf("count owner %d documents: %v", ownerUserID, err)
	}
	if err := pool.QueryRow(
		ctx,
		"SELECT COUNT(*) FROM documents WHERE owner_user_id IS NULL",
	).Scan(&unowned); err != nil {
		t.Fatalf("count unowned documents: %v", err)
	}
	if owned != wantOwned || unowned != wantUnowned {
		t.Fatalf(
			"document counts owner=%d unowned=%d, want %d and %d",
			owned,
			unowned,
			wantOwned,
			wantUnowned,
		)
	}
}
