package embedding

import (
	"testing"

	accessdomain "rag-reasoning-platform/backend/internal/domain/access"
)

const testEmbeddingOwnerUserID int64 = 42

// testEmbeddingOwnerScope 创建应用层向量测试共用的有效用户作用域。
func testEmbeddingOwnerScope(t *testing.T) accessdomain.OwnerScope {
	t.Helper()
	scope, err := accessdomain.NewOwnerScope(testEmbeddingOwnerUserID)
	if err != nil {
		t.Fatalf("create test embedding owner scope: %v", err)
	}
	return scope
}
