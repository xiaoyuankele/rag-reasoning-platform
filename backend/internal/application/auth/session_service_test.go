package auth

import (
	"context"
	"errors"
	"testing"
	"time"

	authdomain "rag-reasoning-platform/backend/internal/domain/auth"
	userdomain "rag-reasoning-platform/backend/internal/domain/user"
)

func TestSessionServiceAuthenticatesValidIdentity(t *testing.T) {
	now := time.Date(2026, 8, 16, 11, 0, 0, 0, time.UTC)
	repository := &fakeSessionAuthenticationRepository{
		identity: AuthenticatedIdentity{
			Actor: Actor{UserID: 42, SessionID: 9},
			User: userdomain.User{
				ID:     42,
				Status: userdomain.StatusActive,
			},
			Session: authdomain.Session{
				ID:        9,
				UserID:    42,
				TokenHash: "token-hash",
				ExpiresAt: now.Add(time.Hour),
				CreatedAt: now.Add(-time.Hour),
			},
		},
	}
	service, err := NewSessionService(
		repository,
		&fakeSessionTokenHasher{hash: "token-hash"},
		func() time.Time { return now },
	)
	if err != nil {
		t.Fatalf("NewSessionService() error = %v", err)
	}

	identity, err := service.Authenticate(context.Background(), "raw-token")
	if err != nil {
		t.Fatalf("Authenticate() error = %v", err)
	}
	if identity.Actor.UserID != 42 || identity.Actor.SessionID != 9 ||
		repository.foundHash != "token-hash" || !repository.foundAt.Equal(now) {
		t.Fatalf("Authenticate() identity=%+v repository=%+v", identity, repository)
	}
}

func TestSessionServiceRejectsMissingMalformedAndUnknownTokens(t *testing.T) {
	tests := []struct {
		name       string
		rawToken   string
		hashErr    error
		repository *fakeSessionAuthenticationRepository
	}{
		{name: "missing token", rawToken: "", repository: &fakeSessionAuthenticationRepository{}},
		{name: "malformed token", rawToken: "bad", hashErr: errors.New("bad token"), repository: &fakeSessionAuthenticationRepository{}},
		{name: "unknown token", rawToken: "raw", repository: &fakeSessionAuthenticationRepository{findErr: ErrAuthenticationRequired}},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			service, err := NewSessionService(
				test.repository,
				&fakeSessionTokenHasher{hash: "hash", err: test.hashErr},
				time.Now,
			)
			if err != nil {
				t.Fatalf("NewSessionService() error = %v", err)
			}
			_, err = service.Authenticate(context.Background(), test.rawToken)
			if !errors.Is(err, ErrAuthenticationRequired) {
				t.Fatalf("Authenticate() error = %v, want %v", err, ErrAuthenticationRequired)
			}
		})
	}
}

func TestSessionServiceLogoutIsIdempotentAndRevokesValidToken(t *testing.T) {
	now := time.Date(2026, 8, 16, 11, 0, 0, 0, time.UTC)
	repository := &fakeSessionAuthenticationRepository{}
	service, err := NewSessionService(
		repository,
		&fakeSessionTokenHasher{hash: "token-hash"},
		func() time.Time { return now },
	)
	if err != nil {
		t.Fatalf("NewSessionService() error = %v", err)
	}

	if err := service.Logout(context.Background(), ""); err != nil {
		t.Fatalf("Logout(empty) error = %v", err)
	}
	if err := service.Logout(context.Background(), "raw-token"); err != nil {
		t.Fatalf("Logout(valid) error = %v", err)
	}
	if repository.revokeCalls != 1 || repository.revokedHash != "token-hash" ||
		!repository.revokedAt.Equal(now) {
		t.Fatalf("Logout() repository = %+v", repository)
	}
}

// fakeSessionTokenHasher 让测试控制 Cookie Token 的摘要结果。
type fakeSessionTokenHasher struct {
	hash string
	err  error
}

func (h *fakeSessionTokenHasher) Hash(_ string) (string, error) {
	return h.hash, h.err
}

// fakeSessionAuthenticationRepository 记录认证查询和撤销命令。
type fakeSessionAuthenticationRepository struct {
	identity    AuthenticatedIdentity
	findErr     error
	foundHash   string
	foundAt     time.Time
	revokeErr   error
	revokeCalls int
	revokedHash string
	revokedAt   time.Time
}

func (r *fakeSessionAuthenticationRepository) FindAuthenticatedIdentity(
	_ context.Context,
	tokenHash string,
	now time.Time,
) (AuthenticatedIdentity, error) {
	r.foundHash = tokenHash
	r.foundAt = now
	return r.identity, r.findErr
}

func (r *fakeSessionAuthenticationRepository) RevokeSession(
	_ context.Context,
	tokenHash string,
	revokedAt time.Time,
) error {
	r.revokeCalls++
	r.revokedHash = tokenHash
	r.revokedAt = revokedAt
	return r.revokeErr
}
