package auth

import (
	"context"
	"errors"
	"testing"
	"time"

	authdomain "rag-reasoning-platform/backend/internal/domain/auth"
	userdomain "rag-reasoning-platform/backend/internal/domain/user"
)

func TestRegisterServiceCreatesUserAndSession(t *testing.T) {
	now := time.Date(2026, 8, 16, 9, 0, 0, 0, time.UTC)
	lastSentAt := now.Add(-time.Minute)
	repository := &fakeRegistrationRepository{
		challenge: authdomain.VerificationChallenge{
			ID:           12,
			Channel:      authdomain.VerificationChannelEmail,
			Destination:  "user@example.com",
			Purpose:      authdomain.VerificationPurposeRegister,
			CodeHash:     "stored-code-hash",
			ExpiresAt:    now.Add(5 * time.Minute),
			SendCount:    1,
			LastSentAt:   &lastSentAt,
			AttemptCount: 0,
		},
		createResult: RegistrationResult{
			User: userdomain.User{
				ID:          42,
				DisplayName: "Example User",
				Status:      userdomain.StatusActive,
				CreatedAt:   now,
				UpdatedAt:   now,
			},
			Session: authdomain.Session{
				ID:        8,
				UserID:    42,
				ExpiresAt: now.Add(DefaultSessionTTL),
				CreatedAt: now,
			},
		},
	}
	service, err := NewRegisterService(
		repository,
		&fakePasswordHasher{hash: "argon2id-hash"},
		&fakeVerificationCodeMatcher{matches: true},
		&fakeSessionTokenGenerator{pair: SessionTokenPair{Raw: "raw-token", Hash: "token-hash"}},
		func() time.Time { return now },
		DefaultSessionTTL,
	)
	if err != nil {
		t.Fatalf("NewRegisterService() error = %v", err)
	}

	result, err := service.Register(
		context.Background(),
		RegisterInput{
			VerificationID:   12,
			VerificationCode: "483921",
			DisplayName:      "  Example User  ",
			Password:         "Example123",
		},
	)
	if err != nil {
		t.Fatalf("Register() error = %v", err)
	}
	if result.User.ID != 42 || result.SessionToken != "raw-token" ||
		!result.SessionExpiresAt.Equal(now.Add(DefaultSessionTTL)) {
		t.Fatalf("Register() = %+v, want created user and session", result)
	}
	if repository.createdRecord.DisplayName != "Example User" ||
		repository.createdRecord.PasswordHash != "argon2id-hash" ||
		repository.createdRecord.SessionTokenHash != "token-hash" ||
		repository.createdRecord.ExpectedChallengeCodeHash != "stored-code-hash" {
		t.Fatalf("CreateRegistration() record = %+v", repository.createdRecord)
	}
}

func TestRegisterServiceCountsWrongVerificationCode(t *testing.T) {
	now := time.Date(2026, 8, 16, 9, 0, 0, 0, time.UTC)
	lastSentAt := now.Add(-time.Minute)
	repository := &fakeRegistrationRepository{
		challenge: authdomain.VerificationChallenge{
			ID:          7,
			Channel:     authdomain.VerificationChannelEmail,
			Destination: "user@example.com",
			Purpose:     authdomain.VerificationPurposeRegister,
			CodeHash:    "stored-code-hash",
			ExpiresAt:   now.Add(time.Minute),
			SendCount:   1,
			LastSentAt:  &lastSentAt,
		},
		incrementedAttemptCount: 1,
	}
	service, err := NewRegisterService(
		repository,
		&fakePasswordHasher{},
		&fakeVerificationCodeMatcher{matches: false},
		&fakeSessionTokenGenerator{},
		func() time.Time { return now },
		DefaultSessionTTL,
	)
	if err != nil {
		t.Fatalf("NewRegisterService() error = %v", err)
	}

	_, err = service.Register(context.Background(), validRegisterInput())
	if !errors.Is(err, ErrVerificationCodeInvalid) {
		t.Fatalf("Register() error = %v, want %v", err, ErrVerificationCodeInvalid)
	}
	if repository.incrementCalls != 1 || repository.createCalls != 0 {
		t.Fatalf(
			"repository calls increment=%d create=%d, want 1 and 0",
			repository.incrementCalls,
			repository.createCalls,
		)
	}
}

func TestRegisterServiceRejectsInvalidInputBeforeRepository(t *testing.T) {
	tests := []struct {
		name        string
		change      func(*RegisterInput)
		wantedError error
	}{
		{name: "invalid verification ID", change: func(input *RegisterInput) { input.VerificationID = 0 }, wantedError: ErrInvalidRegistrationRequest},
		{name: "invalid verification code", change: func(input *RegisterInput) { input.VerificationCode = "12ab56" }, wantedError: ErrInvalidRegistrationRequest},
		{name: "blank display name", change: func(input *RegisterInput) { input.DisplayName = " " }, wantedError: userdomain.ErrInvalidDisplayName},
		{name: "weak password", change: func(input *RegisterInput) { input.Password = "password" }, wantedError: userdomain.ErrPasswordMissingUppercase},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			repository := &fakeRegistrationRepository{}
			service, err := NewRegisterService(
				repository,
				&fakePasswordHasher{},
				&fakeVerificationCodeMatcher{},
				&fakeSessionTokenGenerator{},
				time.Now,
				DefaultSessionTTL,
			)
			if err != nil {
				t.Fatalf("NewRegisterService() error = %v", err)
			}
			input := validRegisterInput()
			test.change(&input)

			_, err = service.Register(context.Background(), input)
			if !errors.Is(err, test.wantedError) {
				t.Fatalf("Register() error = %v, want %v", err, test.wantedError)
			}
			if repository.findCalls != 0 {
				t.Fatalf("repository called %d times for invalid input", repository.findCalls)
			}
		})
	}
}

func validRegisterInput() RegisterInput {
	return RegisterInput{
		VerificationID:   7,
		VerificationCode: "483921",
		DisplayName:      "Example User",
		Password:         "Example123",
	}
}

// fakeRegistrationRepository 记录 Application 发出的持久化命令，
// 让测试只验证业务编排，不依赖真实 PostgreSQL。
type fakeRegistrationRepository struct {
	challenge               authdomain.VerificationChallenge
	findErr                 error
	findCalls               int
	incrementedAttemptCount int
	incrementErr            error
	incrementCalls          int
	createResult            RegistrationResult
	createErr               error
	createCalls             int
	createdRecord           RegistrationRecord
}

func (r *fakeRegistrationRepository) FindVerificationChallenge(
	_ context.Context,
	_ int64,
) (authdomain.VerificationChallenge, error) {
	r.findCalls++
	return r.challenge, r.findErr
}

func (r *fakeRegistrationRepository) IncrementVerificationAttempts(
	_ context.Context,
	_ int64,
	_ time.Time,
) (int, error) {
	r.incrementCalls++
	return r.incrementedAttemptCount, r.incrementErr
}

func (r *fakeRegistrationRepository) CreateRegistration(
	_ context.Context,
	record RegistrationRecord,
) (RegistrationResult, error) {
	r.createCalls++
	r.createdRecord = record
	return r.createResult, r.createErr
}

// fakePasswordHasher 避免单元测试消耗真实 Argon2id 成本。
type fakePasswordHasher struct {
	hash string
	err  error
}

func (h *fakePasswordHasher) Hash(_ string) (string, error) {
	return h.hash, h.err
}

func (h *fakePasswordHasher) Verify(_, _ string) (bool, error) {
	return false, nil
}

// fakeVerificationCodeMatcher 由测试直接决定验证码是否匹配。
type fakeVerificationCodeMatcher struct {
	matches bool
}

func (m *fakeVerificationCodeMatcher) Matches(
	_ string,
	_ authdomain.VerificationChannel,
	_ string,
	_ authdomain.VerificationPurpose,
	_ string,
) bool {
	return m.matches
}

// fakeSessionTokenGenerator 提供确定的 Token，便于核对数据库命令与响应。
type fakeSessionTokenGenerator struct {
	pair SessionTokenPair
	err  error
}

func (g *fakeSessionTokenGenerator) Generate() (SessionTokenPair, error) {
	return g.pair, g.err
}
