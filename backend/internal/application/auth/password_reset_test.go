package auth

import (
	"context"
	"errors"
	"testing"
	"time"

	authdomain "rag-reasoning-platform/backend/internal/domain/auth"
	userdomain "rag-reasoning-platform/backend/internal/domain/user"
)

func TestPasswordResetServiceResetsPassword(t *testing.T) {
	now := time.Date(2026, 8, 18, 9, 0, 0, 0, time.UTC)
	repository := newFakePasswordResetRepository(now)
	service, err := NewPasswordResetService(
		repository,
		&fakePasswordHasher{hash: "$argon2id$new-hash"},
		&fakeVerificationCodeMatcher{matches: true},
		func() time.Time { return now },
	)
	if err != nil {
		t.Fatalf("NewPasswordResetService() error = %v", err)
	}

	err = service.ResetPassword(context.Background(), validPasswordResetInput())
	if err != nil {
		t.Fatalf("ResetPassword() error = %v", err)
	}
	if repository.resetCalls != 1 {
		t.Fatalf("ResetPassword repository calls = %d, want 1", repository.resetCalls)
	}
	if repository.resetRecord.ChallengeID != repository.challenge.ID ||
		repository.resetRecord.ExpectedChallengeCodeHash != repository.challenge.CodeHash ||
		repository.resetRecord.PasswordHash != "$argon2id$new-hash" ||
		!repository.resetRecord.ResetAt.Equal(now) {
		t.Fatalf("ResetPassword record = %+v", repository.resetRecord)
	}
}

func TestPasswordResetServiceCountsWrongVerificationCode(t *testing.T) {
	now := time.Date(2026, 8, 18, 9, 0, 0, 0, time.UTC)
	repository := newFakePasswordResetRepository(now)
	repository.incrementedAttemptCount = 1
	service, err := NewPasswordResetService(
		repository,
		&fakePasswordHasher{},
		&fakeVerificationCodeMatcher{matches: false},
		func() time.Time { return now },
	)
	if err != nil {
		t.Fatalf("NewPasswordResetService() error = %v", err)
	}

	err = service.ResetPassword(context.Background(), validPasswordResetInput())
	if !errors.Is(err, ErrVerificationCodeInvalid) {
		t.Fatalf("ResetPassword() error = %v, want invalid code", err)
	}
	if repository.incrementCalls != 1 || repository.resetCalls != 0 {
		t.Fatalf(
			"repository calls increment=%d reset=%d, want 1/0",
			repository.incrementCalls,
			repository.resetCalls,
		)
	}
}

func TestPasswordResetServiceRejectsWrongPurpose(t *testing.T) {
	now := time.Date(2026, 8, 18, 9, 0, 0, 0, time.UTC)
	repository := newFakePasswordResetRepository(now)
	repository.challenge.Purpose = authdomain.VerificationPurposeRegister
	service, err := NewPasswordResetService(
		repository,
		&fakePasswordHasher{},
		&fakeVerificationCodeMatcher{matches: true},
		func() time.Time { return now },
	)
	if err != nil {
		t.Fatalf("NewPasswordResetService() error = %v", err)
	}

	err = service.ResetPassword(context.Background(), validPasswordResetInput())
	if !errors.Is(err, ErrVerificationCodeInvalid) || repository.resetCalls != 0 {
		t.Fatalf("ResetPassword() error=%v reset calls=%d, want rejected", err, repository.resetCalls)
	}
}

func TestPasswordResetServiceHidesMissingAccount(t *testing.T) {
	now := time.Date(2026, 8, 18, 9, 0, 0, 0, time.UTC)
	repository := newFakePasswordResetRepository(now)
	repository.resetErr = ErrPasswordResetAccountNotFound
	service, err := NewPasswordResetService(
		repository,
		&fakePasswordHasher{hash: "$argon2id$new-hash"},
		&fakeVerificationCodeMatcher{matches: true},
		func() time.Time { return now },
	)
	if err != nil {
		t.Fatalf("NewPasswordResetService() error = %v", err)
	}

	err = service.ResetPassword(context.Background(), validPasswordResetInput())
	if !errors.Is(err, ErrVerificationCodeInvalid) {
		t.Fatalf("ResetPassword() error = %v, want generic invalid code", err)
	}
}

func TestPasswordResetServiceRejectsInvalidInputBeforeRepository(t *testing.T) {
	tests := []struct {
		name        string
		change      func(*PasswordResetInput)
		wantedError error
	}{
		{name: "invalid verification ID", change: func(input *PasswordResetInput) { input.VerificationID = 0 }, wantedError: ErrInvalidPasswordResetRequest},
		{name: "invalid verification code", change: func(input *PasswordResetInput) { input.VerificationCode = "12345x" }, wantedError: ErrInvalidPasswordResetRequest},
		{name: "weak new password", change: func(input *PasswordResetInput) { input.NewPassword = "password" }, wantedError: userdomain.ErrPasswordMissingUppercase},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			repository := &fakePasswordResetRepository{}
			service, err := NewPasswordResetService(
				repository,
				&fakePasswordHasher{},
				&fakeVerificationCodeMatcher{},
				time.Now,
			)
			if err != nil {
				t.Fatalf("NewPasswordResetService() error = %v", err)
			}
			input := validPasswordResetInput()
			test.change(&input)

			err = service.ResetPassword(context.Background(), input)
			if !errors.Is(err, test.wantedError) {
				t.Fatalf("ResetPassword() error = %v, want %v", err, test.wantedError)
			}
			if repository.findCalls != 0 {
				t.Fatalf("repository called %d times for invalid input", repository.findCalls)
			}
		})
	}
}

func validPasswordResetInput() PasswordResetInput {
	return PasswordResetInput{
		VerificationID:   21,
		VerificationCode: "483921",
		NewPassword:      "Changed123",
	}
}

// fakePasswordResetRepository 记录重置用例发出的命令，避免 Application 测试依赖数据库。
type fakePasswordResetRepository struct {
	challenge               authdomain.VerificationChallenge
	findErr                 error
	findCalls               int
	incrementedAttemptCount int
	incrementErr            error
	incrementCalls          int
	resetRecord             PasswordResetRecord
	resetErr                error
	resetCalls              int
}

func newFakePasswordResetRepository(now time.Time) *fakePasswordResetRepository {
	lastSentAt := now.Add(-time.Minute)
	return &fakePasswordResetRepository{
		challenge: authdomain.VerificationChallenge{
			ID:          21,
			Channel:     authdomain.VerificationChannelEmail,
			Destination: "user@example.com",
			Purpose:     authdomain.VerificationPurposePasswordReset,
			CodeHash:    "stored-code-hash",
			ExpiresAt:   now.Add(5 * time.Minute),
			SendCount:   1,
			LastSentAt:  &lastSentAt,
		},
	}
}

func (r *fakePasswordResetRepository) FindVerificationChallenge(
	_ context.Context,
	_ int64,
) (authdomain.VerificationChallenge, error) {
	r.findCalls++
	return r.challenge, r.findErr
}

func (r *fakePasswordResetRepository) IncrementVerificationAttempts(
	_ context.Context,
	_ int64,
	_ time.Time,
) (int, error) {
	r.incrementCalls++
	return r.incrementedAttemptCount, r.incrementErr
}

func (r *fakePasswordResetRepository) ResetPassword(
	_ context.Context,
	record PasswordResetRecord,
) error {
	r.resetCalls++
	r.resetRecord = record
	return r.resetErr
}
