// Package documentowner 提供旧版无主文档升级到个人所有者模型的一次性维护用例。
// 它不属于普通用户业务链路，也不会被 HTTP Handler 或 Worker 调用。
package documentowner

import (
	"context"
	"errors"
	"fmt"

	userdomain "rag-reasoning-platform/backend/internal/domain/user"
)

var (
	// ErrInvalidUserID 表示历史数据认领目标不是有效的用户 ID。
	ErrInvalidUserID = errors.New("owner claim user ID must be positive")

	// ErrInvalidExpectedCount 表示确认执行时没有提供有效的预计数量。
	ErrInvalidExpectedCount = errors.New("expected unowned document count must not be negative")

	// ErrTargetNotFound 表示认领目标用户不存在。
	ErrTargetNotFound = errors.New("owner claim target user was not found")

	// ErrTargetInactive 表示目标用户存在，但当前不允许接收历史数据。
	ErrTargetInactive = errors.New("owner claim target user is not active")
)

// Target 是历史数据将要归属的个人用户摘要。
// 它不包含密码哈希、联系方式或 Session 等与运维确认无关的数据。
type Target struct {
	UserID      int64
	DisplayName string
	Status      userdomain.Status
}

// Preview 是不会修改数据库的历史文档认领预览。
type Preview struct {
	Target           Target
	UnownedDocuments int64
}

// Result 是认领事务成功提交后的结果。
type Result struct {
	Target           Target
	ClaimedDocuments int64
	RemainingUnowned int64
}

// CountMismatchError 表示确认时数据库现状已经不同于 dry-run 结果。
// 调用者必须重新预览，不能在数量发生变化后继续盲目更新。
type CountMismatchError struct {
	Expected int64
	Actual   int64
}

// Error 实现 error 接口，并保留预期数量和实际数量用于命令行诊断。
func (e *CountMismatchError) Error() string {
	return fmt.Sprintf(
		"unowned document count changed: expected %d, actual %d",
		e.Expected,
		e.Actual,
	)
}

// repository 是维护用例需要的最小持久化端口。
// PreviewOwnerClaim 只读；ClaimUnownedDocuments 必须原子核对并更新。
type repository interface {
	PreviewOwnerClaim(
		ctx context.Context,
		ownerUserID int64,
	) (Preview, error)
	ClaimUnownedDocuments(
		ctx context.Context,
		ownerUserID int64,
		expectedUnowned int64,
	) (Result, error)
}

// Service 编排历史无主文档的预览和显式认领。
type Service struct {
	repository repository
}

// NewService 创建历史文档认领维护服务。
func NewService(repository repository) *Service {
	return &Service{repository: repository}
}

// PreviewClaim 返回目标用户和当前无主文档数量，但不修改任何数据。
func (s *Service) PreviewClaim(
	ctx context.Context,
	ownerUserID int64,
) (Preview, error) {
	if ownerUserID <= 0 {
		return Preview{}, ErrInvalidUserID
	}

	preview, err := s.repository.PreviewOwnerClaim(ctx, ownerUserID)
	if err != nil {
		return Preview{}, fmt.Errorf("preview owner claim: %w", err)
	}
	return preview, nil
}

// Claim 在预计数量仍与数据库一致时，认领全部 owner_user_id 为 NULL 的文档。
func (s *Service) Claim(
	ctx context.Context,
	ownerUserID int64,
	expectedUnowned int64,
) (Result, error) {
	if ownerUserID <= 0 {
		return Result{}, ErrInvalidUserID
	}
	if expectedUnowned < 0 {
		return Result{}, ErrInvalidExpectedCount
	}

	result, err := s.repository.ClaimUnownedDocuments(
		ctx,
		ownerUserID,
		expectedUnowned,
	)
	if err != nil {
		return Result{}, fmt.Errorf("claim unowned documents: %w", err)
	}
	return result, nil
}
