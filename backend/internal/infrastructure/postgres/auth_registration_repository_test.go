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

func TestAuthRegistrationRepositoryCreatesAtomicRegistration(t *testing.T) {
	if os.Getenv("RUN_DATABASE_TESTS") != "1" {
		t.Skip("set RUN_DATABASE_TESTS=1 to run PostgreSQL integration tests")
	}

	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	pool := openIsolatedDocumentTestPool(t, ctx)
	repository := postgresrepository.NewAuthRegistrationRepository(pool)

	registeredAt := time.Now().UTC().Truncate(time.Microsecond)
	codeHash := strings.Repeat("a", 64)
	challengeID := insertSentRegistrationChallenge(
		t,
		ctx,
		pool,
		"atomic-register@example.com",
		codeHash,
		registeredAt,
	)

	result, err := repository.CreateRegistration(
		ctx,
		authapplication.RegistrationRecord{
			ChallengeID:               challengeID,
			ExpectedChallengeCodeHash: codeHash,
			DisplayName:               "Atomic User",
			PasswordHash:              "$argon2id$test-hash",
			SessionTokenHash:          strings.Repeat("b", 64),
			SessionExpiresAt:          registeredAt.Add(7 * 24 * time.Hour),
			RegisteredAt:              registeredAt,
		},
	)
	if err != nil {
		t.Fatalf("CreateRegistration() error = %v", err)
	}
	if result.User.ID <= 0 || result.User.Email == nil ||
		*result.User.Email != "atomic-register@example.com" ||
		result.Session.UserID != result.User.ID || !result.Session.IsActive(registeredAt) {
		t.Fatalf("CreateRegistration() = %+v, want linked user and session", result)
	}

	var consumedAt *time.Time
	if err := pool.QueryRow(
		ctx,
		"SELECT consumed_at FROM verification_challenges WHERE id = $1",
		challengeID,
	).Scan(&consumedAt); err != nil {
		t.Fatalf("query consumed challenge: %v", err)
	}
	if consumedAt == nil {
		t.Fatal("successful registration did not consume verification challenge")
	}
}

func TestAuthRegistrationRepositoryRollsBackDuplicateContact(t *testing.T) {
	if os.Getenv("RUN_DATABASE_TESTS") != "1" {
		t.Skip("set RUN_DATABASE_TESTS=1 to run PostgreSQL integration tests")
	}

	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	pool := openIsolatedDocumentTestPool(t, ctx)
	repository := postgresrepository.NewAuthRegistrationRepository(pool)
	registeredAt := time.Now().UTC().Truncate(time.Microsecond)
	destination := "duplicate-register@example.com"
	codeHash := strings.Repeat("c", 64)

	firstChallengeID := insertSentRegistrationChallenge(t, ctx, pool, destination, codeHash, registeredAt)
	_, err := repository.CreateRegistration(
		ctx,
		registrationRecordForTest(firstChallengeID, codeHash, "First User", "d", registeredAt),
	)
	if err != nil {
		t.Fatalf("first CreateRegistration() error = %v", err)
	}

	secondChallengeID := insertSentRegistrationChallenge(t, ctx, pool, destination, codeHash, registeredAt.Add(time.Second))
	_, err = repository.CreateRegistration(
		ctx,
		registrationRecordForTest(secondChallengeID, codeHash, "Second User", "e", registeredAt.Add(time.Second)),
	)
	if !errors.Is(err, authapplication.ErrContactAlreadyRegistered) {
		t.Fatalf("duplicate CreateRegistration() error = %v, want contact conflict", err)
	}

	var consumedAt *time.Time
	if err := pool.QueryRow(
		ctx,
		"SELECT consumed_at FROM verification_challenges WHERE id = $1",
		secondChallengeID,
	).Scan(&consumedAt); err != nil {
		t.Fatalf("query duplicate challenge: %v", err)
	}
	if consumedAt != nil {
		t.Fatal("rolled-back duplicate registration consumed its challenge")
	}
	var sessionCount int
	if err := pool.QueryRow(ctx, "SELECT COUNT(*) FROM user_sessions").Scan(&sessionCount); err != nil {
		t.Fatalf("count sessions: %v", err)
	}
	if sessionCount != 1 {
		t.Fatalf("session count = %d, want only first registration session", sessionCount)
	}
}

func registrationRecordForTest(
	challengeID int64,
	codeHash string,
	displayName string,
	tokenHashCharacter string,
	registeredAt time.Time,
) authapplication.RegistrationRecord {
	return authapplication.RegistrationRecord{
		ChallengeID:               challengeID,
		ExpectedChallengeCodeHash: codeHash,
		DisplayName:               displayName,
		PasswordHash:              "$argon2id$test-hash",
		SessionTokenHash:          strings.Repeat(tokenHashCharacter, 64),
		SessionExpiresAt:          registeredAt.Add(7 * 24 * time.Hour),
		RegisteredAt:              registeredAt,
	}
}

func insertSentRegistrationChallenge(
	t *testing.T,
	ctx context.Context,
	pool *pgxpool.Pool,
	destination string,
	codeHash string,
	createdAt time.Time,
) int64 {
	t.Helper()
	var challengeID int64
	err := pool.QueryRow(
		ctx,
		`
			INSERT INTO verification_challenges (
				channel, destination, purpose, code_hash, expires_at,
				send_count, last_sent_at, created_at, updated_at
			)
			VALUES ('email', $1, 'register', $2, $3, 1, $4, $4, $4)
			RETURNING id
		`,
		destination,
		codeHash,
		createdAt.Add(10*time.Minute),
		createdAt,
	).Scan(&challengeID)
	if err != nil {
		t.Fatalf("insert verification challenge: %v", err)
	}
	return challengeID
}
