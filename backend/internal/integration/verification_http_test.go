package integration_test

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/gin-gonic/gin"

	"rag-reasoning-platform/backend/internal/api"
	verificationapplication "rag-reasoning-platform/backend/internal/application/verification"
	"rag-reasoning-platform/backend/internal/config"
	authdomain "rag-reasoning-platform/backend/internal/domain/auth"
	"rag-reasoning-platform/backend/internal/infrastructure/database"
	postgresrepository "rag-reasoning-platform/backend/internal/infrastructure/postgres"
	"rag-reasoning-platform/backend/internal/infrastructure/ratelimit"
	verificationinfrastructure "rag-reasoning-platform/backend/internal/infrastructure/verification"
	"rag-reasoning-platform/backend/migrations"
)

// TestVerificationCodeHTTPWithPostgreSQL 验证真实的
// HTTP → Handler → Application → PostgreSQL/Fake Sender 纵向链路。
func TestVerificationCodeHTTPWithPostgreSQL(t *testing.T) {
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
	// 后注册的数据 Cleanup 会先执行，连接池最后关闭。
	t.Cleanup(pool.Close)
	if err := database.Migrate(ctx, pool, migrations.Files); err != nil {
		t.Fatalf("migrate database: %v", err)
	}

	destination := fmt.Sprintf(
		"verification-http-%d@example.com",
		time.Now().UnixNano(),
	)
	t.Cleanup(func() {
		cleanupContext, cleanupCancel := context.WithTimeout(
			context.Background(),
			5*time.Second,
		)
		defer cleanupCancel()
		if _, err := pool.Exec(
			cleanupContext,
			"DELETE FROM verification_challenges WHERE destination = $1",
			destination,
		); err != nil {
			t.Errorf("delete verification HTTP challenge: %v", err)
		}
	})

	repository := postgresrepository.NewVerificationChallengeRepository(pool)
	hasher, err := verificationinfrastructure.NewHMACCodeHasher(
		[]byte("0123456789abcdef0123456789abcdef"),
	)
	if err != nil {
		t.Fatalf("create HMAC hasher: %v", err)
	}
	sender := verificationinfrastructure.NewFakeSender()
	service := verificationapplication.NewService(
		repository,
		verificationinfrastructure.NewRandomCodeGenerator(),
		hasher,
		sender,
		time.Now,
		verificationapplication.DefaultChallengeTTL,
		verificationapplication.DefaultResendCooldown,
	)
	limiter, err := ratelimit.NewSlidingWindowLimiter(time.Minute, 10, 100)
	if err != nil {
		t.Fatalf("create request limiter: %v", err)
	}

	logger := slog.New(slog.NewJSONHandler(io.Discard, nil))
	gin.SetMode(gin.TestMode)
	router := api.NewRouter(logger)
	api.NewVerificationHandler(service, limiter, logger).RegisterRoutes(router)

	requestBody := fmt.Sprintf(
		`{"channel":"email","destination":%q,"purpose":"register"}`,
		destination,
	)
	firstResponse := performVerificationIntegrationRequest(router, requestBody)
	if firstResponse.Code != http.StatusAccepted {
		t.Fatalf(
			"first request status = %d, want %d; body = %s",
			firstResponse.Code,
			http.StatusAccepted,
			firstResponse.Body.String(),
		)
	}

	var response struct {
		VerificationID int64     `json:"verification_id"`
		ExpiresAt      time.Time `json:"expires_at"`
		ResendAfter    time.Time `json:"resend_after"`
	}
	if err := json.Unmarshal(firstResponse.Body.Bytes(), &response); err != nil {
		t.Fatalf("decode response: %v", err)
	}
	if response.VerificationID <= 0 ||
		!response.ExpiresAt.After(time.Now().UTC()) ||
		!response.ResendAfter.Before(response.ExpiresAt) {
		t.Fatalf("unexpected verification response: %+v", response)
	}
	if response.ExpiresAt.Location() != time.UTC ||
		response.ResendAfter.Location() != time.UTC {
		t.Fatalf("HTTP response times must use UTC: %+v", response)
	}
	if strings.Contains(firstResponse.Body.String(), "code") ||
		strings.Contains(firstResponse.Body.String(), "hash") {
		t.Fatalf("HTTP response leaked verification secret: %s", firstResponse.Body.String())
	}

	messages := sender.Messages()
	if len(messages) != 1 || len(messages[0].Code) != 6 {
		t.Fatalf("Fake Sender messages = %+v, want one six-digit code", messages)
	}
	storedChallenge, err := repository.FindLatest(
		ctx,
		authdomain.VerificationChannelEmail,
		destination,
		authdomain.VerificationPurposeRegister,
	)
	if err != nil {
		t.Fatalf("find stored challenge: %v", err)
	}
	if storedChallenge.ID != response.VerificationID ||
		!storedChallenge.CanAttempt(time.Now().UTC()) ||
		!hasher.Matches(
			storedChallenge.CodeHash,
			storedChallenge.Channel,
			storedChallenge.Destination,
			storedChallenge.Purpose,
			messages[0].Code,
		) {
		t.Fatalf("stored challenge is not a usable match: %+v", storedChallenge)
	}

	secondResponse := performVerificationIntegrationRequest(router, requestBody)
	if secondResponse.Code != http.StatusTooManyRequests {
		t.Fatalf(
			"second request status = %d, want %d; body = %s",
			secondResponse.Code,
			http.StatusTooManyRequests,
			secondResponse.Body.String(),
		)
	}
	if secondResponse.Header().Get("Retry-After") == "" {
		t.Fatal("second request did not include Retry-After")
	}
	if len(sender.Messages()) != 1 {
		t.Fatalf("cooldown sent %d messages, want 1", len(sender.Messages()))
	}
}

func performVerificationIntegrationRequest(
	router http.Handler,
	body string,
) *httptest.ResponseRecorder {
	request := httptest.NewRequest(
		http.MethodPost,
		"/auth/verification-codes",
		strings.NewReader(body),
	)
	request.Header.Set("Content-Type", "application/json")
	request.RemoteAddr = "198.51.100.20:54321"

	response := httptest.NewRecorder()
	router.ServeHTTP(response, request)
	return response
}
