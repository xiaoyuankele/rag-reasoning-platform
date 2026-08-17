package document

import (
	"context"
	"errors"
	"testing"

	userdomain "rag-reasoning-platform/backend/internal/domain/user"
)

func TestOwnerClaimServicePreviewsWithoutWriting(t *testing.T) {
	repository := &fakeOwnerClaimRepository{
		preview: OwnerClaimPreview{
			Target: OwnerClaimTarget{
				UserID:      17,
				DisplayName: "bigboss",
				Status:      userdomain.StatusActive,
			},
			UnownedDocuments: 46,
		},
	}
	service := NewOwnerClaimService(repository)

	result, err := service.Preview(context.Background(), 17)
	if err != nil {
		t.Fatalf("Preview() error = %v", err)
	}
	if result.UnownedDocuments != 46 || result.Target.UserID != 17 {
		t.Fatalf("Preview() = %+v, want user 17 and 46 documents", result)
	}
	if repository.previewCalls != 1 || repository.claimCalls != 0 {
		t.Fatalf(
			"repository calls: preview=%d claim=%d, want 1 and 0",
			repository.previewCalls,
			repository.claimCalls,
		)
	}
}

func TestOwnerClaimServiceClaimsExpectedDocuments(t *testing.T) {
	repository := &fakeOwnerClaimRepository{
		claimResult: OwnerClaimResult{
			Target: OwnerClaimTarget{
				UserID:      17,
				DisplayName: "bigboss",
				Status:      userdomain.StatusActive,
			},
			ClaimedDocuments: 46,
			RemainingUnowned: 0,
		},
	}
	service := NewOwnerClaimService(repository)

	result, err := service.Claim(context.Background(), 17, 46)
	if err != nil {
		t.Fatalf("Claim() error = %v", err)
	}
	if result.ClaimedDocuments != 46 || result.RemainingUnowned != 0 {
		t.Fatalf("Claim() = %+v, want 46 claimed and 0 remaining", result)
	}
	if repository.receivedOwnerID != 17 || repository.receivedExpected != 46 {
		t.Fatalf(
			"repository received owner=%d expected=%d, want 17 and 46",
			repository.receivedOwnerID,
			repository.receivedExpected,
		)
	}
}

func TestOwnerClaimServiceRejectsInvalidInputBeforeRepository(t *testing.T) {
	repository := &fakeOwnerClaimRepository{}
	service := NewOwnerClaimService(repository)

	if _, err := service.Preview(context.Background(), 0); !errors.Is(err, ErrInvalidOwnerClaimUserID) {
		t.Fatalf("Preview(0) error = %v, want ErrInvalidOwnerClaimUserID", err)
	}
	if _, err := service.Claim(context.Background(), 17, -1); !errors.Is(err, ErrInvalidExpectedUnownedCount) {
		t.Fatalf("Claim(expected=-1) error = %v, want ErrInvalidExpectedUnownedCount", err)
	}
	if repository.previewCalls != 0 || repository.claimCalls != 0 {
		t.Fatalf("invalid input reached repository: %+v", repository)
	}
}

// fakeOwnerClaimRepository 是 OwnerClaimService 的内存测试替身。
// 它只记录调用参数，不访问 PostgreSQL，也不会修改真实文档。
type fakeOwnerClaimRepository struct {
	preview          OwnerClaimPreview
	previewErr       error
	claimResult      OwnerClaimResult
	claimErr         error
	previewCalls     int
	claimCalls       int
	receivedOwnerID  int64
	receivedExpected int64
}

// PreviewOwnerClaim 返回测试预设的只读预览。
func (r *fakeOwnerClaimRepository) PreviewOwnerClaim(
	_ context.Context,
	ownerUserID int64,
) (OwnerClaimPreview, error) {
	r.previewCalls++
	r.receivedOwnerID = ownerUserID
	return r.preview, r.previewErr
}

// ClaimUnownedDocuments 返回测试预设的认领结果并记录预计数量。
func (r *fakeOwnerClaimRepository) ClaimUnownedDocuments(
	_ context.Context,
	ownerUserID int64,
	expectedUnowned int64,
) (OwnerClaimResult, error) {
	r.claimCalls++
	r.receivedOwnerID = ownerUserID
	r.receivedExpected = expectedUnowned
	return r.claimResult, r.claimErr
}
