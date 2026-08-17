package document

import (
	"context"
	"fmt"

	documentdomain "rag-reasoning-platform/backend/internal/domain/document"
)

var (
	// ErrInvalidOwnerClaimUserID 是 Domain 稳定错误在 Application 的便捷别名。
	ErrInvalidOwnerClaimUserID = documentdomain.ErrInvalidOwnerClaimUserID

	// ErrInvalidExpectedUnownedCount 是预计数量错误的便捷别名。
	ErrInvalidExpectedUnownedCount = documentdomain.ErrInvalidExpectedUnownedCount

	// ErrOwnerClaimTargetNotFound 是目标用户不存在错误的便捷别名。
	ErrOwnerClaimTargetNotFound = documentdomain.ErrOwnerClaimTargetNotFound

	// ErrOwnerClaimTargetInactive 是目标用户不可用错误的便捷别名。
	ErrOwnerClaimTargetInactive = documentdomain.ErrOwnerClaimTargetInactive
)

// 类型别名让 Application 使用简洁名称，同时共享类型实际归属 Domain。
type OwnerClaimTarget = documentdomain.OwnerClaimTarget
type OwnerClaimPreview = documentdomain.OwnerClaimPreview
type OwnerClaimResult = documentdomain.OwnerClaimResult
type OwnerClaimCountMismatchError = documentdomain.OwnerClaimCountMismatchError

// ownerClaimRepository 是历史文档认领用例需要的最小持久化端口。
// Preview 只读；Claim 必须在一次数据库事务中完成数量核对和更新。
type ownerClaimRepository interface {
	PreviewOwnerClaim(
		ctx context.Context,
		ownerUserID int64,
	) (OwnerClaimPreview, error)
	ClaimUnownedDocuments(
		ctx context.Context,
		ownerUserID int64,
		expectedUnowned int64,
	) (OwnerClaimResult, error)
}

// OwnerClaimService 编排历史无主文档的预览和显式认领。
// 它只服务于受控的运维命令，不暴露为普通用户 HTTP 接口。
type OwnerClaimService struct {
	repository ownerClaimRepository
}

// NewOwnerClaimService 创建历史文档认领应用服务。
func NewOwnerClaimService(repository ownerClaimRepository) *OwnerClaimService {
	return &OwnerClaimService{repository: repository}
}

// Preview 返回目标用户和当前无主文档数量，但不修改任何数据。
func (s *OwnerClaimService) Preview(
	ctx context.Context,
	ownerUserID int64,
) (OwnerClaimPreview, error) {
	if ownerUserID <= 0 {
		return OwnerClaimPreview{}, ErrInvalidOwnerClaimUserID
	}

	preview, err := s.repository.PreviewOwnerClaim(ctx, ownerUserID)
	if err != nil {
		return OwnerClaimPreview{}, fmt.Errorf("preview owner claim: %w", err)
	}
	return preview, nil
}

// Claim 在预计数量仍与数据库一致时，认领全部 owner_user_id 为 NULL 的文档。
func (s *OwnerClaimService) Claim(
	ctx context.Context,
	ownerUserID int64,
	expectedUnowned int64,
) (OwnerClaimResult, error) {
	if ownerUserID <= 0 {
		return OwnerClaimResult{}, ErrInvalidOwnerClaimUserID
	}
	if expectedUnowned < 0 {
		return OwnerClaimResult{}, ErrInvalidExpectedUnownedCount
	}

	result, err := s.repository.ClaimUnownedDocuments(
		ctx,
		ownerUserID,
		expectedUnowned,
	)
	if err != nil {
		return OwnerClaimResult{}, fmt.Errorf("claim unowned documents: %w", err)
	}
	return result, nil
}
