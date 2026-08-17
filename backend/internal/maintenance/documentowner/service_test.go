package documentowner

import (
	"context"
	"errors"
	"testing"

	userdomain "rag-reasoning-platform/backend/internal/domain/user"
)

func TestServicePreviewsWithoutWriting(t *testing.T) {
	repository := &fakeRepository{
		preview: Preview{
			Target: Target{
				UserID:      17,
				DisplayName: "bigboss",
				Status:      userdomain.StatusActive,
			},
			UnownedDocuments: 46,
		},
	}
	service := NewService(repository)

	result, err := service.PreviewClaim(context.Background(), 17)
	if err != nil {
		t.Fatalf("PreviewClaim() error = %v", err)
	}
	if result.UnownedDocuments != 46 || result.Target.UserID != 17 {
		t.Fatalf("PreviewClaim() = %+v, want user 17 and 46 documents", result)
	}
	if repository.previewCalls != 1 || repository.claimCalls != 0 {
		t.Fatalf(
			"repository calls: preview=%d claim=%d, want 1 and 0",
			repository.previewCalls,
			repository.claimCalls,
		)
	}
}

func TestServiceClaimsExpectedDocuments(t *testing.T) {
	repository := &fakeRepository{
		claimResult: Result{
			Target: Target{
				UserID:      17,
				DisplayName: "bigboss",
				Status:      userdomain.StatusActive,
			},
			ClaimedDocuments: 46,
			RemainingUnowned: 0,
		},
	}
	service := NewService(repository)

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

func TestServiceRejectsInvalidInputBeforeRepository(t *testing.T) {
	repository := &fakeRepository{}
	service := NewService(repository)

	if _, err := service.PreviewClaim(context.Background(), 0); !errors.Is(err, ErrInvalidUserID) {
		t.Fatalf("PreviewClaim(0) error = %v, want ErrInvalidUserID", err)
	}
	if _, err := service.Claim(context.Background(), 17, -1); !errors.Is(err, ErrInvalidExpectedCount) {
		t.Fatalf("Claim(expected=-1) error = %v, want ErrInvalidExpectedCount", err)
	}
	if repository.previewCalls != 0 || repository.claimCalls != 0 {
		t.Fatalf("invalid input reached repository: %+v", repository)
	}
}

// fakeRepository 是维护 Service 的内存测试替身。
// 它只记录调用参数，不访问 PostgreSQL，也不会修改真实文档。
type fakeRepository struct {
	preview          Preview
	previewErr       error
	claimResult      Result
	claimErr         error
	previewCalls     int
	claimCalls       int
	receivedOwnerID  int64
	receivedExpected int64
}

// PreviewOwnerClaim 返回测试预设的只读预览。
func (r *fakeRepository) PreviewOwnerClaim(
	_ context.Context,
	ownerUserID int64,
) (Preview, error) {
	r.previewCalls++
	r.receivedOwnerID = ownerUserID
	return r.preview, r.previewErr
}

// ClaimUnownedDocuments 返回测试预设的认领结果并记录预计数量。
func (r *fakeRepository) ClaimUnownedDocuments(
	_ context.Context,
	ownerUserID int64,
	expectedUnowned int64,
) (Result, error) {
	r.claimCalls++
	r.receivedOwnerID = ownerUserID
	r.receivedExpected = expectedUnowned
	return r.claimResult, r.claimErr
}
