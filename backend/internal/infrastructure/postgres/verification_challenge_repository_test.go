package postgres_test

import (
	"context"
	"errors"
	"fmt"
	"os"
	"strings"
	"sync"
	"testing"
	"time"

	verificationapplication "rag-reasoning-platform/backend/internal/application/verification"
	"rag-reasoning-platform/backend/internal/config"
	authdomain "rag-reasoning-platform/backend/internal/domain/auth"
	"rag-reasoning-platform/backend/internal/infrastructure/database"
	postgresrepository "rag-reasoning-platform/backend/internal/infrastructure/postgres"
	"rag-reasoning-platform/backend/migrations"
)

func TestVerificationChallengeRepositoryLifecycle(t *testing.T) {
	if os.Getenv("RUN_DATABASE_TESTS") != "1" {
		t.Skip("set RUN_DATABASE_TESTS=1 to run PostgreSQL integration tests")
	}

	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()

	databaseConfig, err := config.LoadDatabase()
	if err != nil {
		t.Fatalf("load database configuration: %v", err)
	}

	pool, err := database.Open(ctx, databaseConfig.ConnectionString())
	if err != nil {
		t.Fatalf("open database: %v", err)
	}
	defer pool.Close()

	if err := database.Migrate(ctx, pool, migrations.Files); err != nil {
		t.Fatalf("migrate database: %v", err)
	}

	repository := postgresrepository.NewVerificationChallengeRepository(pool)
	uniqueDestination := fmt.Sprintf(
		"verification-%d@example.com",
		time.Now().UnixNano(),
	)
	concurrentDestination := fmt.Sprintf(
		"verification-concurrent-%d@example.com",
		time.Now().UnixNano(),
	)
	t.Cleanup(func() {
		cleanupContext, cleanupCancel := context.WithTimeout(
			context.Background(),
			5*time.Second,
		)
		defer cleanupCancel()
		_, _ = pool.Exec(
			cleanupContext,
			"DELETE FROM verification_challenges WHERE destination IN ($1, $2)",
			uniqueDestination,
			concurrentDestination,
		)
	})

	_, err = repository.FindLatest(
		ctx,
		authdomain.VerificationChannelEmail,
		uniqueDestination,
		authdomain.VerificationPurposeRegister,
	)
	if !errors.Is(err, authdomain.ErrVerificationChallengeNotFound) {
		t.Fatalf("FindLatest() missing error = %v, want not found", err)
	}

	createdAt := time.Now().UTC().Truncate(time.Microsecond)
	created, err := repository.Create(
		ctx,
		authdomain.VerificationChallenge{
			Channel:     authdomain.VerificationChannelEmail,
			Destination: uniqueDestination,
			Purpose:     authdomain.VerificationPurposeRegister,
			CodeHash:    strings.Repeat("a", 64),
			ExpiresAt:   createdAt.Add(10 * time.Minute),
			CreatedAt:   createdAt,
			UpdatedAt:   createdAt,
		},
		time.Minute,
	)
	if err != nil {
		t.Fatalf("Create() error = %v, want nil", err)
	}
	if created.ID <= 0 || created.SendCount != 0 || created.LastSentAt != nil {
		t.Fatalf("created challenge = %+v, want pending challenge", created)
	}

	found, err := repository.FindLatest(
		ctx,
		authdomain.VerificationChannelEmail,
		uniqueDestination,
		authdomain.VerificationPurposeRegister,
	)
	if err != nil {
		t.Fatalf("FindLatest() error = %v, want nil", err)
	}
	if found.ID != created.ID {
		t.Fatalf("FindLatest() ID = %d, want %d", found.ID, created.ID)
	}

	sentAt := createdAt.Add(time.Second)
	marked, err := repository.MarkSent(ctx, created.ID, sentAt)
	if err != nil {
		t.Fatalf("MarkSent() error = %v, want nil", err)
	}
	if marked.SendCount != 1 || marked.LastSentAt == nil ||
		!marked.LastSentAt.Equal(sentAt) {
		t.Fatalf("marked challenge = %+v, want sent state", marked)
	}
	if !marked.CanAttempt(sentAt) {
		t.Fatal("sent challenge should be available for verification")
	}

	_, err = repository.MarkSent(ctx, created.ID, sentAt.Add(time.Second))
	if !errors.Is(err, authdomain.ErrVerificationChallengeNotFound) {
		t.Fatalf("second MarkSent() error = %v, want unavailable challenge", err)
	}

	// 两个请求同时为同一邮箱预留发送名额时，只允许一个成功。
	type createResult struct {
		challenge authdomain.VerificationChallenge
		err       error
	}
	start := make(chan struct{})
	results := make(chan createResult, 2)
	var waitGroup sync.WaitGroup
	for index := 0; index < 2; index++ {
		waitGroup.Add(1)
		go func(codeCharacter byte) {
			defer waitGroup.Done()
			<-start

			challenge, createErr := repository.Create(
				ctx,
				authdomain.VerificationChallenge{
					Channel:     authdomain.VerificationChannelEmail,
					Destination: concurrentDestination,
					Purpose:     authdomain.VerificationPurposeRegister,
					CodeHash:    strings.Repeat(string(codeCharacter), 64),
					ExpiresAt:   createdAt.Add(10 * time.Minute),
					CreatedAt:   createdAt,
					UpdatedAt:   createdAt,
				},
				time.Minute,
			)
			results <- createResult{challenge: challenge, err: createErr}
		}(byte('b' + index))
	}
	close(start)
	waitGroup.Wait()
	close(results)

	var successfulCreates int
	var cooldownRejections int
	for result := range results {
		switch {
		case result.err == nil && result.challenge.ID > 0:
			successfulCreates++
		case errors.Is(result.err, verificationapplication.ErrVerificationCooldown):
			cooldownRejections++
		default:
			t.Fatalf("unexpected concurrent Create() result: %+v", result)
		}
	}
	if successfulCreates != 1 || cooldownRejections != 1 {
		t.Fatalf(
			"concurrent Create() successes=%d cooldowns=%d, want 1 and 1",
			successfulCreates,
			cooldownRejections,
		)
	}
}
