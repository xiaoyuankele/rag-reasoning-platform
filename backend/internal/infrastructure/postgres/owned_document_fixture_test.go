package postgres_test

import (
	"context"
	"fmt"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"

	accessdomain "rag-reasoning-platform/backend/internal/domain/access"
	documentdomain "rag-reasoning-platform/backend/internal/domain/document"
	postgresrepository "rag-reasoning-platform/backend/internal/infrastructure/postgres"
)

// ownedDocumentFixture 为 PostgreSQL 集成测试提供带所有者范围的文档操作。
//
// Release B 之后 documents.owner_user_id 不允许为空。测试创建文档时也必须
// 遵守与生产上传接口相同的用户边界，不能再通过旧的无作用域 Create 绕过约束。
type ownedDocumentFixture struct {
	repository *postgresrepository.ScopedDocumentRepository
	scope      accessdomain.OwnerScope
}

// newOwnedDocumentFixture 创建一个独立测试用户及其文档仓储夹具。
func newOwnedDocumentFixture(
	t *testing.T,
	ctx context.Context,
	pool *pgxpool.Pool,
) *ownedDocumentFixture {
	t.Helper()

	ownerID := insertScopedRepositoryUser(
		t,
		ctx,
		pool,
		fmt.Sprintf("document-fixture-%d@example.com", time.Now().UnixNano()),
	)
	scope, err := accessdomain.NewOwnerScope(ownerID)
	if err != nil {
		t.Fatalf("create document fixture owner scope: %v", err)
	}
	t.Cleanup(func() {
		cleanupContext, cleanupCancel := context.WithTimeout(
			context.Background(),
			5*time.Second,
		)
		defer cleanupCancel()

		// 只删除这个随机测试用户拥有的数据，不触碰开发库中的其他用户。
		if _, cleanupErr := pool.Exec(
			cleanupContext,
			"DELETE FROM documents WHERE owner_user_id = $1",
			ownerID,
		); cleanupErr != nil {
			t.Errorf("clean up document fixture data: %v", cleanupErr)
			return
		}
		if _, cleanupErr := pool.Exec(
			cleanupContext,
			"DELETE FROM users WHERE id = $1",
			ownerID,
		); cleanupErr != nil {
			t.Errorf("clean up document fixture owner: %v", cleanupErr)
		}
	})

	return &ownedDocumentFixture{
		repository: postgresrepository.NewScopedDocumentRepository(pool),
		scope:      scope,
	}
}

// Create 在测试所有者范围内创建文档。
func (f *ownedDocumentFixture) Create(
	ctx context.Context,
	input documentdomain.CreateInput,
) (documentdomain.Document, error) {
	return f.repository.Create(ctx, f.scope, input)
}

// GetByID 在测试所有者范围内查询文档。
func (f *ownedDocumentFixture) GetByID(
	ctx context.Context,
	id int64,
) (documentdomain.Document, error) {
	return f.repository.GetByID(ctx, f.scope, id)
}

// Delete 在测试所有者范围内删除文档。
func (f *ownedDocumentFixture) Delete(ctx context.Context, id int64) error {
	return f.repository.Delete(ctx, f.scope, id)
}

// List 在测试所有者范围内列出文档。
func (f *ownedDocumentFixture) List(
	ctx context.Context,
	options documentdomain.ListOptions,
) (documentdomain.ListResult, error) {
	return f.repository.List(ctx, f.scope, options)
}
