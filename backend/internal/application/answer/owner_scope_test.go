package answer

import (
	"testing"

	accessdomain "rag-reasoning-platform/backend/internal/domain/access"
)

const testAnswerOwnerUserID int64 = 42

// testAnswerOwnerScope 创建问答 Application 测试共用的可信用户作用域。
func testAnswerOwnerScope(t *testing.T) accessdomain.OwnerScope {
	t.Helper()
	scope, err := accessdomain.NewOwnerScope(testAnswerOwnerUserID)
	if err != nil {
		t.Fatalf("create test answer owner scope: %v", err)
	}
	return scope
}
