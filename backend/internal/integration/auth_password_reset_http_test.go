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
	authapplication "rag-reasoning-platform/backend/internal/application/auth"
	verificationapplication "rag-reasoning-platform/backend/internal/application/verification"
	"rag-reasoning-platform/backend/internal/config"
	"rag-reasoning-platform/backend/internal/infrastructure/database"
	passwordinfrastructure "rag-reasoning-platform/backend/internal/infrastructure/password"
	postgresrepository "rag-reasoning-platform/backend/internal/infrastructure/postgres"
	"rag-reasoning-platform/backend/internal/infrastructure/ratelimit"
	sessioninfrastructure "rag-reasoning-platform/backend/internal/infrastructure/session"
	verificationinfrastructure "rag-reasoning-platform/backend/internal/infrastructure/verification"
	"rag-reasoning-platform/backend/migrations"
)

// TestPasswordResetHTTPWithPostgreSQL 验证公开 HTTP、Application、Argon2id 与
// PostgreSQL 事务能够共同完成密码更新、验证码消费和旧 Session 撤销。
func TestPasswordResetHTTPWithPostgreSQL(t *testing.T) {
	if os.Getenv("RUN_DATABASE_TESTS") != "1" {
		t.Skip("set RUN_DATABASE_TESTS=1 to run PostgreSQL integration tests")
	}

	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	databaseConfig, err := config.LoadDatabase()
	if err != nil {
		t.Fatalf("load database configuration: %v", err)
	}
	pool, err := database.Open(ctx, databaseConfig.ConnectionString())
	if err != nil {
		t.Fatalf("open database: %v", err)
	}
	t.Cleanup(pool.Close)
	if err := database.Migrate(ctx, pool, migrations.Files); err != nil {
		t.Fatalf("migrate database: %v", err)
	}

	destination := fmt.Sprintf("password-reset-http-%d@example.com", time.Now().UnixNano())
	t.Cleanup(func() {
		cleanupContext, cleanupCancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cleanupCancel()
		_, _ = pool.Exec(cleanupContext, "DELETE FROM users WHERE email = $1", destination)
		_, _ = pool.Exec(cleanupContext, "DELETE FROM verification_challenges WHERE destination = $1", destination)
	})

	passwordHasher, err := passwordinfrastructure.NewArgon2idHasher(
		passwordinfrastructure.DefaultParameters(),
	)
	if err != nil {
		t.Fatalf("create password hasher: %v", err)
	}
	oldPasswordHash, err := passwordHasher.Hash("Original123")
	if err != nil {
		t.Fatalf("hash original password: %v", err)
	}
	now := time.Now().UTC().Truncate(time.Microsecond)
	var userID int64
	if err := pool.QueryRow(
		ctx,
		`
			INSERT INTO users (
				email, email_verified_at, display_name, password_hash,
				created_at, updated_at
			)
			VALUES ($1, $2, 'Password Reset HTTP User', $3, $2, $2)
			RETURNING id
		`,
		destination,
		now,
		oldPasswordHash,
	).Scan(&userID); err != nil {
		t.Fatalf("insert password reset HTTP user: %v", err)
	}

	sessionTokenManager := sessioninfrastructure.NewTokenGenerator()
	oldSession, err := sessionTokenManager.Generate()
	if err != nil {
		t.Fatalf("generate old session: %v", err)
	}
	if _, err := pool.Exec(
		ctx,
		`
			INSERT INTO user_sessions (
				user_id, token_hash, expires_at, created_at
			)
			VALUES ($1, $2, $3, $4)
		`,
		userID,
		oldSession.Hash,
		now.Add(7*24*time.Hour),
		now,
	); err != nil {
		t.Fatalf("insert old session: %v", err)
	}

	challengeRepository := postgresrepository.NewVerificationChallengeRepository(pool)
	passwordResetRepository := postgresrepository.NewAuthPasswordResetRepository(pool)
	sessionRepository := postgresrepository.NewAuthSessionRepository(pool)
	codeHasher, err := verificationinfrastructure.NewHMACCodeHasher(
		[]byte("0123456789abcdef0123456789abcdef"),
	)
	if err != nil {
		t.Fatalf("create verification code hasher: %v", err)
	}
	sender := verificationinfrastructure.NewFakeSender()
	verificationService := verificationapplication.NewService(
		challengeRepository,
		verificationinfrastructure.NewRandomCodeGenerator(),
		codeHasher,
		sender,
		time.Now,
		verificationapplication.DefaultChallengeTTL,
		verificationapplication.DefaultResendCooldown,
	)
	passwordResetService, err := authapplication.NewPasswordResetService(
		passwordResetRepository,
		passwordHasher,
		codeHasher,
		time.Now,
	)
	if err != nil {
		t.Fatalf("create password reset service: %v", err)
	}
	loginService, err := authapplication.NewLoginService(
		sessionRepository,
		passwordHasher,
		sessionTokenManager,
		time.Now,
		authapplication.DefaultSessionTTL,
	)
	if err != nil {
		t.Fatalf("create login service: %v", err)
	}
	sessionService, err := authapplication.NewSessionService(
		sessionRepository,
		sessionTokenManager,
		time.Now,
	)
	if err != nil {
		t.Fatalf("create session service: %v", err)
	}
	verificationLimiter, _ := ratelimit.NewSlidingWindowLimiter(time.Minute, 10, 100)
	authLimiter, _ := ratelimit.NewSlidingWindowLimiter(time.Minute, 10, 100)
	logger := slog.New(slog.NewJSONHandler(io.Discard, nil))
	gin.SetMode(gin.TestMode)
	router := api.NewRouter(logger)
	api.NewVerificationHandler(verificationService, verificationLimiter, logger).RegisterRoutes(router)
	api.NewAuthPasswordResetHandler(passwordResetService, authLimiter, logger, false).RegisterRoutes(router)
	api.NewAuthLoginHandler(loginService, authLimiter, logger, false).RegisterRoutes(router)
	users := router.Group("/users")
	users.Use(api.NewAuthMiddleware(sessionService, logger).Require)
	api.NewCurrentUserHandler().RegisterRoutes(users)

	verificationResponse := performJSONRequest(
		router,
		"/auth/verification-codes",
		fmt.Sprintf(
			`{"channel":"email","destination":%q,"purpose":"password_reset"}`,
			destination,
		),
	)
	if verificationResponse.Code != http.StatusAccepted {
		t.Fatalf("verification status=%d body=%s", verificationResponse.Code, verificationResponse.Body.String())
	}
	var verificationBody struct {
		VerificationID int64 `json:"verification_id"`
	}
	if err := json.Unmarshal(verificationResponse.Body.Bytes(), &verificationBody); err != nil {
		t.Fatalf("decode verification response: %v", err)
	}
	messages := sender.Messages()
	if len(messages) != 1 || messages[0].Purpose != "password_reset" {
		t.Fatalf("password reset messages = %+v", messages)
	}

	resetRequest := httptest.NewRequest(
		http.MethodPost,
		"/auth/password-reset",
		strings.NewReader(fmt.Sprintf(
			`{"verification_id":%d,"verification_code":%q,"new_password":"Changed123"}`,
			verificationBody.VerificationID,
			messages[0].Code,
		)),
	)
	resetRequest.Header.Set("Content-Type", "application/json")
	resetRequest.RemoteAddr = "198.51.100.42:54321"
	resetRequest.AddCookie(&http.Cookie{Name: "rag_session", Value: oldSession.Raw})
	resetResponse := httptest.NewRecorder()
	router.ServeHTTP(resetResponse, resetRequest)
	if resetResponse.Code != http.StatusNoContent {
		t.Fatalf("reset status=%d body=%s", resetResponse.Code, resetResponse.Body.String())
	}
	resetCookies := resetResponse.Result().Cookies()
	if len(resetCookies) != 1 || resetCookies[0].Name != "rag_session" || resetCookies[0].MaxAge != -1 {
		t.Fatalf("reset cookies=%+v, want deleted session cookie", resetCookies)
	}

	var consumedAt *time.Time
	var activeSessions int
	if err := pool.QueryRow(
		ctx,
		"SELECT consumed_at FROM verification_challenges WHERE id = $1",
		verificationBody.VerificationID,
	).Scan(&consumedAt); err != nil {
		t.Fatalf("query consumed reset challenge: %v", err)
	}
	if err := pool.QueryRow(
		ctx,
		"SELECT COUNT(*) FROM user_sessions WHERE user_id = $1 AND revoked_at IS NULL",
		userID,
	).Scan(&activeSessions); err != nil {
		t.Fatalf("count active sessions after reset: %v", err)
	}
	if consumedAt == nil || activeSessions != 0 {
		t.Fatalf("reset state consumed=%v active sessions=%d", consumedAt, activeSessions)
	}

	oldSessionRequest := httptest.NewRequest(http.MethodGet, "/users/me", nil)
	oldSessionRequest.AddCookie(&http.Cookie{Name: "rag_session", Value: oldSession.Raw})
	oldSessionResponse := httptest.NewRecorder()
	router.ServeHTTP(oldSessionResponse, oldSessionRequest)
	if oldSessionResponse.Code != http.StatusUnauthorized {
		t.Fatalf("old session status=%d body=%s", oldSessionResponse.Code, oldSessionResponse.Body.String())
	}

	oldPasswordLogin := performJSONRequest(
		router,
		"/auth/login",
		fmt.Sprintf(`{"identifier":%q,"password":"Original123"}`, destination),
	)
	if oldPasswordLogin.Code != http.StatusUnauthorized {
		t.Fatalf("old password login status=%d body=%s", oldPasswordLogin.Code, oldPasswordLogin.Body.String())
	}
	newPasswordLogin := performJSONRequest(
		router,
		"/auth/login",
		fmt.Sprintf(`{"identifier":%q,"password":"Changed123"}`, destination),
	)
	if newPasswordLogin.Code != http.StatusOK {
		t.Fatalf("new password login status=%d body=%s", newPasswordLogin.Code, newPasswordLogin.Body.String())
	}
}
