package document

import (
	"testing"

	accessdomain "rag-reasoning-platform/backend/internal/domain/access"
)

const testOwnerUserID int64 = 42

// testOwnerScope 为 Application 单元测试创建固定的可信所有者范围。
func testOwnerScope(t *testing.T) accessdomain.OwnerScope {
	t.Helper()
	scope, err := accessdomain.NewOwnerScope(testOwnerUserID)
	if err != nil {
		t.Fatalf("create test owner scope: %v", err)
	}
	return scope
}
