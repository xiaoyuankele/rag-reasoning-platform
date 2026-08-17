package document

import (
	"errors"
	"fmt"

	userdomain "rag-reasoning-platform/backend/internal/domain/user"
)

var (
	// ErrInvalidOwnerClaimUserID 表示历史数据认领目标不是有效的用户 ID。
	ErrInvalidOwnerClaimUserID = errors.New("owner claim user ID must be positive")

	// ErrInvalidExpectedUnownedCount 表示确认执行时没有提供有效的预计数量。
	ErrInvalidExpectedUnownedCount = errors.New("expected unowned document count must not be negative")

	// ErrOwnerClaimTargetNotFound 表示认领目标用户不存在。
	ErrOwnerClaimTargetNotFound = errors.New("owner claim target user was not found")

	// ErrOwnerClaimTargetInactive 表示目标用户存在，但当前不允许接收历史数据。
	ErrOwnerClaimTargetInactive = errors.New("owner claim target user is not active")
)

// OwnerClaimTarget 是历史数据将要归属的个人用户摘要。
// 这里只暴露运维确认需要的信息，不包含密码哈希或 Session 等敏感字段。
type OwnerClaimTarget struct {
	UserID      int64
	DisplayName string
	Status      userdomain.Status
}

// OwnerClaimPreview 是不会修改数据库的历史文档认领预览。
type OwnerClaimPreview struct {
	Target           OwnerClaimTarget
	UnownedDocuments int64
}

// OwnerClaimResult 是事务成功提交后的认领结果。
type OwnerClaimResult struct {
	Target           OwnerClaimTarget
	ClaimedDocuments int64
	RemainingUnowned int64
}

// OwnerClaimCountMismatchError 表示确认时数据库现状已经不同于 dry-run 结果。
// 调用者必须重新预览，不能在数量发生变化后继续盲目更新。
type OwnerClaimCountMismatchError struct {
	Expected int64
	Actual   int64
}

// Error 实现 error 接口，并保留预期数量和实际数量用于命令行诊断。
func (e *OwnerClaimCountMismatchError) Error() string {
	return fmt.Sprintf(
		"unowned document count changed: expected %d, actual %d",
		e.Expected,
		e.Actual,
	)
}
