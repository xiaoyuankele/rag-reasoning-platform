package auth

import (
	"context"
	"errors"
	"testing"
	"time"

	authdomain "rag-reasoning-platform/backend/internal/domain/auth"
	userdomain "rag-reasoning-platform/backend/internal/domain/user"
)

func TestLoginServiceCreatesSessionForValidCredentials(t *testing.T) {
	now := time.Date(2026, 8, 16, 10, 0, 0, 0, time.UTC)
	email := "user@example.com"
	repository := &fakeLoginRepository{
		account: LoginAccount{
			User: userdomain.User{
				ID:          42,
				Email:       &email,
				DisplayName: "Example User",
				Status:      userdomain.StatusActive,
			},
			PasswordHash: "stored-password-hash",
		},
		createdSession: authdomain.Session{
			ID:        9,
			UserID:    42,
			ExpiresAt: now.Add(DefaultSessionTTL),
			CreatedAt: now,
		},
	}
	hasher := &fakePasswordHasher{
		hash: "dummy-password-hash",
		verify: func(password string, encodedHash string) (bool, error) {
			return password == "Example123" && encodedHash == "stored-password-hash", nil
		},
	}
	service, err := NewLoginService(
		repository,
		hasher,
		&fakeSessionTokenGenerator{pair: SessionTokenPair{Raw: "raw-token", Hash: "token-hash"}},
		func() time.Time { return now },
		DefaultSessionTTL,
	)
	if err != nil {
		t.Fatalf("NewLoginService() error = %v", err)
	}

	result, err := service.Login(
		context.Background(),
		LoginInput{Identifier: " User@Example.COM ", Password: "Example123"},
	)
	if err != nil {
		t.Fatalf("Login() error = %v", err)
	}
	if repository.foundIdentifier != "user@example.com" {
		t.Fatalf("FindLoginAccount() identifier = %q, want normalized email", repository.foundIdentifier)
	}
	if repository.createdRecord.UserID != 42 ||
		repository.createdRecord.TokenHash != "token-hash" ||
		!repository.createdRecord.ExpiresAt.Equal(now.Add(DefaultSessionTTL)) {
		t.Fatalf("CreateSession() record = %+v", repository.createdRecord)
	}
	if result.User.ID != 42 || result.SessionToken != "raw-token" ||
		!result.SessionExpiresAt.Equal(now.Add(DefaultSessionTTL)) {
		t.Fatalf("Login() = %+v, want user and raw session token", result)
	}
}

func TestLoginServiceHidesMissingWrongAndDisabledAccounts(t *testing.T) {
	now := time.Date(2026, 8, 16, 10, 0, 0, 0, time.UTC)
	tests := []struct {
		name       string
		repository *fakeLoginRepository
		matches    bool
	}{
		{
			name:       "missing account",
			repository: &fakeLoginRepository{findErr: ErrLoginAccountNotFound},
		},
		{
			name: "wrong password",
			repository: &fakeLoginRepository{account: LoginAccount{
				User:         userdomain.User{ID: 1, Status: userdomain.StatusActive},
				PasswordHash: "stored-hash",
			}},
		},
		{
			name: "disabled account",
			repository: &fakeLoginRepository{account: LoginAccount{
				User:         userdomain.User{ID: 1, Status: userdomain.StatusDisabled},
				PasswordHash: "stored-hash",
			}},
			matches: true,
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			hasher := &fakePasswordHasher{
				hash: "dummy-hash",
				verify: func(_ string, _ string) (bool, error) {
					return test.matches, nil
				},
			}
			service, err := NewLoginService(
				test.repository,
				hasher,
				&fakeSessionTokenGenerator{},
				func() time.Time { return now },
				DefaultSessionTTL,
			)
			if err != nil {
				t.Fatalf("NewLoginService() error = %v", err)
			}

			_, err = service.Login(
				context.Background(),
				LoginInput{Identifier: "user@example.com", Password: "Example123"},
			)
			if !errors.Is(err, ErrInvalidCredentials) {
				t.Fatalf("Login() error = %v, want %v", err, ErrInvalidCredentials)
			}
			if hasher.verifyCalls != 1 {
				t.Fatalf("Verify() calls = %d, want 1 for timing consistency", hasher.verifyCalls)
			}
			if test.repository.createCalls != 0 {
				t.Fatal("invalid credentials created a session")
			}
		})
	}
}

// fakeLoginRepository 记录登录服务发出的账户查询和 Session 创建命令。
type fakeLoginRepository struct {
	account         LoginAccount
	findErr         error
	foundIdentifier string
	createdSession  authdomain.Session
	createErr       error
	createdRecord   SessionRecord
	createCalls     int
}

func (r *fakeLoginRepository) FindLoginAccount(
	_ context.Context,
	identifier string,
) (LoginAccount, error) {
	r.foundIdentifier = identifier
	return r.account, r.findErr
}

func (r *fakeLoginRepository) CreateSession(
	_ context.Context,
	record SessionRecord,
) (authdomain.Session, error) {
	r.createCalls++
	r.createdRecord = record
	return r.createdSession, r.createErr
}
