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

// TestAuthRegistrationAndLoginHTTPWithPostgreSQL 验证真实的验证码申请、
// 注册、再次登录、Argon2id、PostgreSQL Session 和 Set-Cookie 纵向链路。
func TestAuthRegistrationAndLoginHTTPWithPostgreSQL(t *testing.T) {
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
	defer pool.Close()
	if err := database.Migrate(ctx, pool, migrations.Files); err != nil {
		t.Fatalf("migrate database: %v", err)
	}

	destination := fmt.Sprintf("register-http-%d@example.com", time.Now().UnixNano())
	t.Cleanup(func() {
		cleanupContext, cleanupCancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cleanupCancel()
		_, _ = pool.Exec(cleanupContext, "DELETE FROM users WHERE email = $1", destination)
		_, _ = pool.Exec(cleanupContext, "DELETE FROM verification_challenges WHERE destination = $1", destination)
	})

	challengeRepository := postgresrepository.NewVerificationChallengeRepository(pool)
	registrationRepository := postgresrepository.NewAuthRegistrationRepository(pool)
	sessionRepository := postgresrepository.NewAuthSessionRepository(pool)
	codeHasher, err := verificationinfrastructure.NewHMACCodeHasher(
		[]byte("0123456789abcdef0123456789abcdef"),
	)
	if err != nil {
		t.Fatalf("create code hasher: %v", err)
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
	passwordHasher, err := passwordinfrastructure.NewArgon2idHasher(
		passwordinfrastructure.DefaultParameters(),
	)
	if err != nil {
		t.Fatalf("create password hasher: %v", err)
	}
	registerService, err := authapplication.NewRegisterService(
		registrationRepository,
		passwordHasher,
		codeHasher,
		sessioninfrastructure.NewTokenGenerator(),
		time.Now,
		authapplication.DefaultSessionTTL,
	)
	if err != nil {
		t.Fatalf("create register service: %v", err)
	}
	loginService, err := authapplication.NewLoginService(
		sessionRepository,
		passwordHasher,
		sessioninfrastructure.NewTokenGenerator(),
		time.Now,
		authapplication.DefaultSessionTTL,
	)
	if err != nil {
		t.Fatalf("create login service: %v", err)
	}
	verificationLimiter, _ := ratelimit.NewSlidingWindowLimiter(time.Minute, 10, 100)
	authLimiter, _ := ratelimit.NewSlidingWindowLimiter(time.Minute, 10, 100)
	logger := slog.New(slog.NewJSONHandler(io.Discard, nil))
	gin.SetMode(gin.TestMode)
	router := api.NewRouter(logger)
	api.NewVerificationHandler(verificationService, verificationLimiter, logger).RegisterRoutes(router)
	api.NewAuthRegisterHandler(registerService, authLimiter, logger, false).RegisterRoutes(router)
	api.NewAuthLoginHandler(loginService, authLimiter, logger, false).RegisterRoutes(router)

	verificationResponse := performJSONRequest(
		router,
		"/auth/verification-codes",
		fmt.Sprintf(`{"channel":"email","destination":%q,"purpose":"register"}`, destination),
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
	if len(messages) != 1 {
		t.Fatalf("Fake Sender message count=%d, want 1", len(messages))
	}

	registrationResponse := performJSONRequest(
		router,
		"/auth/register",
		fmt.Sprintf(
			`{"verification_id":%d,"verification_code":%q,"display_name":"Integration User","password":"Example123"}`,
			verificationBody.VerificationID,
			messages[0].Code,
		),
	)
	if registrationResponse.Code != http.StatusCreated {
		t.Fatalf("registration status=%d body=%s", registrationResponse.Code, registrationResponse.Body.String())
	}
	cookies := registrationResponse.Result().Cookies()
	if len(cookies) != 1 || cookies[0].Name != "rag_session" || !cookies[0].HttpOnly {
		t.Fatalf("registration cookies=%+v, want HttpOnly rag_session", cookies)
	}
	if strings.Contains(registrationResponse.Body.String(), cookies[0].Value) {
		t.Fatal("registration JSON leaked raw session token")
	}

	var passwordHash string
	var consumedAt *time.Time
	var sessionCount int
	if err := pool.QueryRow(ctx, "SELECT password_hash FROM users WHERE email = $1", destination).Scan(&passwordHash); err != nil {
		t.Fatalf("query registered user: %v", err)
	}
	if err := pool.QueryRow(ctx, "SELECT consumed_at FROM verification_challenges WHERE id = $1", verificationBody.VerificationID).Scan(&consumedAt); err != nil {
		t.Fatalf("query consumed challenge: %v", err)
	}
	if err := pool.QueryRow(
		ctx,
		"SELECT COUNT(*) FROM user_sessions AS session JOIN users AS account ON account.id = session.user_id WHERE account.email = $1",
		destination,
	).Scan(&sessionCount); err != nil {
		t.Fatalf("count registered sessions: %v", err)
	}
	if passwordHash == "Example123" || !strings.HasPrefix(passwordHash, "$argon2id$") || consumedAt == nil || sessionCount != 1 {
		t.Fatalf("stored registration password_hash=%q consumed=%v sessions=%d", passwordHash, consumedAt, sessionCount)
	}

	invalidLoginResponse := performJSONRequest(
		router,
		"/auth/login",
		fmt.Sprintf(`{"identifier":%q,"password":"Wrong123"}`, destination),
	)
	if invalidLoginResponse.Code != http.StatusUnauthorized ||
		!strings.Contains(invalidLoginResponse.Body.String(), `"code":"invalid_credentials"`) {
		t.Fatalf("invalid login status=%d body=%s", invalidLoginResponse.Code, invalidLoginResponse.Body.String())
	}

	loginResponse := performJSONRequest(
		router,
		"/auth/login",
		fmt.Sprintf(`{"identifier":%q,"password":"Example123"}`, destination),
	)
	if loginResponse.Code != http.StatusOK {
		t.Fatalf("login status=%d body=%s", loginResponse.Code, loginResponse.Body.String())
	}
	loginCookies := loginResponse.Result().Cookies()
	if len(loginCookies) != 1 || loginCookies[0].Name != "rag_session" ||
		!loginCookies[0].HttpOnly || loginCookies[0].Value == cookies[0].Value {
		t.Fatalf("login cookies=%+v, want a new HttpOnly session", loginCookies)
	}
	if err := pool.QueryRow(
		ctx,
		"SELECT COUNT(*) FROM user_sessions AS session JOIN users AS account ON account.id = session.user_id WHERE account.email = $1",
		destination,
	).Scan(&sessionCount); err != nil {
		t.Fatalf("count sessions after login: %v", err)
	}
	if sessionCount != 2 {
		t.Fatalf("session count after login=%d, want registration plus login sessions", sessionCount)
	}
}

func performJSONRequest(router http.Handler, path string, body string) *httptest.ResponseRecorder {
	request := httptest.NewRequest(http.MethodPost, path, strings.NewReader(body))
	request.Header.Set("Content-Type", "application/json")
	request.RemoteAddr = "198.51.100.40:54321"
	response := httptest.NewRecorder()
	router.ServeHTTP(response, request)
	return response
}
