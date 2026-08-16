package postgres_test

import (
	"context"
	"errors"
	"os"
	"strings"
	"testing"
	"time"

	authapplication "rag-reasoning-platform/backend/internal/application/auth"
	postgresrepository "rag-reasoning-platform/backend/internal/infrastructure/postgres"
)

func TestAuthSessionRepositoryFindsAccountAndCreatesSession(t *testing.T) {
	if os.Getenv("RUN_DATABASE_TESTS") != "1" {
		t.Skip("set RUN_DATABASE_TESTS=1 to run PostgreSQL integration tests")
	}

	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	pool := openIsolatedDocumentTestPool(t, ctx)
	repository := postgresrepository.NewAuthSessionRepository(pool)
	now := time.Now().UTC().Truncate(time.Microsecond)

	var userID int64
	err := pool.QueryRow(
		ctx,
		`
			INSERT INTO users (
				email, email_verified_at, display_name,
				password_hash, status, created_at, updated_at
			)
			VALUES ($1, $2, 'Login User', '$argon2id$test-hash', 'active', $2, $2)
			RETURNING id
		`,
		"login-repository@example.com",
		now,
	).Scan(&userID)
	if err != nil {
		t.Fatalf("insert login user: %v", err)
	}

	account, err := repository.FindLoginAccount(ctx, "login-repository@example.com")
	if err != nil {
		t.Fatalf("FindLoginAccount() error = %v", err)
	}
	if account.User.ID != userID || account.PasswordHash != "$argon2id$test-hash" ||
		!account.User.Status.AllowsAuthentication() {
		t.Fatalf("FindLoginAccount() = %+v, want active account and hash", account)
	}

	createdSession, err := repository.CreateSession(
		ctx,
		authapplication.SessionRecord{
			UserID:    userID,
			TokenHash: strings.Repeat("f", 64),
			ExpiresAt: now.Add(7 * 24 * time.Hour),
			CreatedAt: now,
		},
	)
	if err != nil {
		t.Fatalf("CreateSession() error = %v", err)
	}
	if createdSession.UserID != userID || !createdSession.IsActive(now) {
		t.Fatalf("CreateSession() = %+v, want active linked session", createdSession)
	}

	identity, err := repository.FindAuthenticatedIdentity(
		ctx,
		strings.Repeat("f", 64),
		now,
	)
	if err != nil {
		t.Fatalf("FindAuthenticatedIdentity() error = %v", err)
	}
	if identity.Actor.UserID != userID ||
		identity.Actor.SessionID != createdSession.ID ||
		identity.User.DisplayName != "Login User" {
		t.Fatalf("FindAuthenticatedIdentity() = %+v, want linked identity", identity)
	}

	revokedAt := now.Add(time.Minute)
	if err := repository.RevokeSession(
		ctx,
		strings.Repeat("f", 64),
		revokedAt,
	); err != nil {
		t.Fatalf("RevokeSession() error = %v", err)
	}
	_, err = repository.FindAuthenticatedIdentity(
		ctx,
		strings.Repeat("f", 64),
		revokedAt,
	)
	if !errors.Is(err, authapplication.ErrAuthenticationRequired) {
		t.Fatalf("FindAuthenticatedIdentity() after revoke error = %v, want authentication required", err)
	}
}

func TestAuthSessionRepositoryRejectsMissingAndDisabledAccounts(t *testing.T) {
	if os.Getenv("RUN_DATABASE_TESTS") != "1" {
		t.Skip("set RUN_DATABASE_TESTS=1 to run PostgreSQL integration tests")
	}

	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	pool := openIsolatedDocumentTestPool(t, ctx)
	repository := postgresrepository.NewAuthSessionRepository(pool)

	_, err := repository.FindLoginAccount(ctx, "missing@example.com")
	if !errors.Is(err, authapplication.ErrLoginAccountNotFound) {
		t.Fatalf("FindLoginAccount() error = %v, want account not found", err)
	}

	now := time.Now().UTC().Truncate(time.Microsecond)
	var disabledUserID int64
	err = pool.QueryRow(
		ctx,
		`
			INSERT INTO users (
				email, email_verified_at, display_name,
				password_hash, status, created_at, updated_at
			)
			VALUES ($1, $2, 'Disabled User', '$argon2id$test-hash', 'disabled', $2, $2)
			RETURNING id
		`,
		"disabled-login@example.com",
		now,
	).Scan(&disabledUserID)
	if err != nil {
		t.Fatalf("insert disabled user: %v", err)
	}

	_, err = repository.CreateSession(
		ctx,
		authapplication.SessionRecord{
			UserID:    disabledUserID,
			TokenHash: strings.Repeat("e", 64),
			ExpiresAt: now.Add(time.Hour),
			CreatedAt: now,
		},
	)
	if !errors.Is(err, authapplication.ErrInvalidCredentials) {
		t.Fatalf("CreateSession() disabled error = %v, want invalid credentials", err)
	}
}
