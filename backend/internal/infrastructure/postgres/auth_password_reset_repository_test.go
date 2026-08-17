package postgres_test

import (
	"context"
	"errors"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"

	authapplication "rag-reasoning-platform/backend/internal/application/auth"
	postgresrepository "rag-reasoning-platform/backend/internal/infrastructure/postgres"
)

func TestAuthPasswordResetRepositoryCommitsPasswordAndRevocations(t *testing.T) {
	if os.Getenv("RUN_DATABASE_TESTS") != "1" {
		t.Skip("set RUN_DATABASE_TESTS=1 to run PostgreSQL integration tests")
	}

	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	pool := openIsolatedDocumentTestPool(t, ctx)
	repository := postgresrepository.NewAuthPasswordResetRepository(pool)
	resetAt := time.Now().UTC().Truncate(time.Microsecond)
	fixture := insertPasswordResetFixture(t, ctx, pool, resetAt)

	err := repository.ResetPassword(
		ctx,
		authapplication.PasswordResetRecord{
			ChallengeID:               fixture.challengeID,
			ExpectedChallengeCodeHash: fixture.codeHash,
			PasswordHash:              "$argon2id$new-password-hash",
			ResetAt:                   resetAt,
		},
	)
	if err != nil {
		t.Fatalf("ResetPassword() error = %v", err)
	}

	var passwordHash string
	if err := pool.QueryRow(
		ctx,
		"SELECT password_hash FROM users WHERE id = $1",
		fixture.userID,
	).Scan(&passwordHash); err != nil {
		t.Fatalf("query reset password hash: %v", err)
	}
	if passwordHash != "$argon2id$new-password-hash" {
		t.Fatalf("password hash = %q, want new hash", passwordHash)
	}

	var consumedAt *time.Time
	if err := pool.QueryRow(
		ctx,
		"SELECT consumed_at FROM verification_challenges WHERE id = $1",
		fixture.challengeID,
	).Scan(&consumedAt); err != nil {
		t.Fatalf("query consumed password reset challenge: %v", err)
	}
	if consumedAt == nil || !consumedAt.Equal(resetAt) {
		t.Fatalf("consumed_at = %v, want %v", consumedAt, resetAt)
	}

	var activeSessions int
	if err := pool.QueryRow(
		ctx,
		"SELECT COUNT(*) FROM user_sessions WHERE user_id = $1 AND revoked_at IS NULL",
		fixture.userID,
	).Scan(&activeSessions); err != nil {
		t.Fatalf("count active sessions after reset: %v", err)
	}
	if activeSessions != 0 {
		t.Fatalf("active sessions = %d, want 0", activeSessions)
	}
}

func TestAuthPasswordResetRepositoryRollsBackMismatchedChallenge(t *testing.T) {
	if os.Getenv("RUN_DATABASE_TESTS") != "1" {
		t.Skip("set RUN_DATABASE_TESTS=1 to run PostgreSQL integration tests")
	}

	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	pool := openIsolatedDocumentTestPool(t, ctx)
	repository := postgresrepository.NewAuthPasswordResetRepository(pool)
	resetAt := time.Now().UTC().Truncate(time.Microsecond)
	fixture := insertPasswordResetFixture(t, ctx, pool, resetAt)

	err := repository.ResetPassword(
		ctx,
		authapplication.PasswordResetRecord{
			ChallengeID:               fixture.challengeID,
			ExpectedChallengeCodeHash: strings.Repeat("f", 64),
			PasswordHash:              "$argon2id$new-password-hash",
			ResetAt:                   resetAt,
		},
	)
	if !errors.Is(err, authapplication.ErrVerificationCodeInvalid) {
		t.Fatalf("ResetPassword() error = %v, want invalid code", err)
	}

	var passwordHash string
	var consumedAt *time.Time
	var activeSessions int
	if err := pool.QueryRow(
		ctx,
		"SELECT password_hash FROM users WHERE id = $1",
		fixture.userID,
	).Scan(&passwordHash); err != nil {
		t.Fatalf("query rolled-back password hash: %v", err)
	}
	if err := pool.QueryRow(
		ctx,
		"SELECT consumed_at FROM verification_challenges WHERE id = $1",
		fixture.challengeID,
	).Scan(&consumedAt); err != nil {
		t.Fatalf("query rolled-back challenge: %v", err)
	}
	if err := pool.QueryRow(
		ctx,
		"SELECT COUNT(*) FROM user_sessions WHERE user_id = $1 AND revoked_at IS NULL",
		fixture.userID,
	).Scan(&activeSessions); err != nil {
		t.Fatalf("count rolled-back sessions: %v", err)
	}
	if passwordHash != "$argon2id$old-password-hash" || consumedAt != nil || activeSessions != 2 {
		t.Fatalf(
			"rollback state hash=%q consumed=%v active sessions=%d",
			passwordHash,
			consumedAt,
			activeSessions,
		)
	}
}

type passwordResetFixture struct {
	userID      int64
	challengeID int64
	codeHash    string
}

func insertPasswordResetFixture(
	t *testing.T,
	ctx context.Context,
	pool *pgxpool.Pool,
	now time.Time,
) passwordResetFixture {
	t.Helper()
	createdAt := now.Add(-time.Minute)
	destination := "password-reset@example.com"
	var userID int64
	if err := pool.QueryRow(
		ctx,
		`
			INSERT INTO users (
				email, email_verified_at, display_name, password_hash,
				created_at, updated_at
			)
			VALUES ($1, $2, 'Password Reset User', '$argon2id$old-password-hash', $2, $2)
			RETURNING id
		`,
		destination,
		createdAt,
	).Scan(&userID); err != nil {
		t.Fatalf("insert password reset user: %v", err)
	}

	for index, character := range []string{"a", "b"} {
		if _, err := pool.Exec(
			ctx,
			`
				INSERT INTO user_sessions (
					user_id, token_hash, expires_at, created_at
				)
				VALUES ($1, $2, $3, $4)
			`,
			userID,
			strings.Repeat(character, 64),
			now.Add(time.Duration(index+1)*time.Hour),
			createdAt,
		); err != nil {
			t.Fatalf("insert password reset session %d: %v", index+1, err)
		}
	}

	codeHash := strings.Repeat("c", 64)
	var challengeID int64
	if err := pool.QueryRow(
		ctx,
		`
			INSERT INTO verification_challenges (
				channel, destination, purpose, code_hash, expires_at,
				send_count, last_sent_at, created_at, updated_at
			)
			VALUES ('email', $1, 'password_reset', $2, $3, 1, $4, $4, $4)
			RETURNING id
		`,
		destination,
		codeHash,
		now.Add(10*time.Minute),
		createdAt,
	).Scan(&challengeID); err != nil {
		t.Fatalf("insert password reset challenge: %v", err)
	}

	return passwordResetFixture{
		userID:      userID,
		challengeID: challengeID,
		codeHash:    codeHash,
	}
}
