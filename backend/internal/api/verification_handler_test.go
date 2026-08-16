package api

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/gin-gonic/gin"

	verificationapplication "rag-reasoning-platform/backend/internal/application/verification"
	authdomain "rag-reasoning-platform/backend/internal/domain/auth"
)

// fakeVerificationRequestService 记录 Handler 传入的 DTO，并返回测试安排的结果。
type fakeVerificationRequestService struct {
	output        verificationapplication.RequestOutput
	err           error
	receivedInput verificationapplication.RequestInput
	callCount     int
}

// RequestCode 模拟 Application 的验证码申请用例。
func (f *fakeVerificationRequestService) RequestCode(
	_ context.Context,
	input verificationapplication.RequestInput,
) (verificationapplication.RequestOutput, error) {
	f.callCount++
	f.receivedInput = input
	return f.output, f.err
}

// fakeVerificationRequestLimiter 允许测试控制请求是继续还是返回 429。
type fakeVerificationRequestLimiter struct {
	allowed           bool
	retryAt           time.Time
	receivedClientKey string
	callCount         int
}

// Allow 模拟单实例限流判断。
func (f *fakeVerificationRequestLimiter) Allow(
	clientKey string,
	_ time.Time,
) (time.Time, bool) {
	f.callCount++
	f.receivedClientKey = clientKey
	return f.retryAt, f.allowed
}

func newVerificationTestRouter(
	service verificationRequestService,
	limiter verificationRequestLimiter,
	logger *slog.Logger,
) *gin.Engine {
	gin.SetMode(gin.TestMode)
	router := gin.New()
	handler := NewVerificationHandler(service, limiter, logger)
	handler.RegisterRoutes(router)
	return router
}

func performVerificationRequest(
	t *testing.T,
	service verificationRequestService,
	limiter verificationRequestLimiter,
	logger *slog.Logger,
	body string,
) *httptest.ResponseRecorder {
	t.Helper()

	request := httptest.NewRequest(
		http.MethodPost,
		"/auth/verification-codes",
		strings.NewReader(body),
	)
	request.Header.Set("Content-Type", "application/json")
	request.RemoteAddr = "203.0.113.20:54321"

	response := httptest.NewRecorder()
	newVerificationTestRouter(service, limiter, logger).ServeHTTP(response, request)
	return response
}

func TestVerificationHandlerReturnsAcceptedChallenge(t *testing.T) {
	chinaStandardTime := time.FixedZone("CST", 8*60*60)
	expiresAt := time.Date(2026, time.August, 16, 18, 10, 0, 0, chinaStandardTime)
	resendAfter := time.Date(2026, time.August, 16, 10, 1, 0, 0, time.UTC)
	service := &fakeVerificationRequestService{
		output: verificationapplication.RequestOutput{
			ChallengeID: 42,
			ExpiresAt:   expiresAt,
			ResendAfter: resendAfter,
		},
	}
	limiter := &fakeVerificationRequestLimiter{allowed: true}

	response := performVerificationRequest(
		t,
		service,
		limiter,
		slog.New(slog.NewTextHandler(io.Discard, nil)),
		`{"channel":"email","destination":" Owner@Example.COM ","purpose":"register"}`,
	)

	if response.Code != http.StatusAccepted {
		t.Fatalf("status = %d, want %d; body = %s", response.Code, http.StatusAccepted, response.Body.String())
	}
	if service.callCount != 1 {
		t.Fatalf("service call count = %d, want 1", service.callCount)
	}
	if service.receivedInput.Channel != authdomain.VerificationChannelEmail ||
		service.receivedInput.Destination != " Owner@Example.COM " ||
		service.receivedInput.Purpose != authdomain.VerificationPurposeRegister {
		t.Fatalf("received input = %+v, want raw HTTP values", service.receivedInput)
	}
	if limiter.receivedClientKey != "203.0.113.20" {
		t.Fatalf("limiter client key = %q, want remote IP", limiter.receivedClientKey)
	}

	var result verificationCodeResponse
	if err := json.Unmarshal(response.Body.Bytes(), &result); err != nil {
		t.Fatalf("decode response: %v", err)
	}
	if result.VerificationID != 42 ||
		!result.ExpiresAt.Equal(expiresAt) ||
		!result.ResendAfter.Equal(resendAfter) {
		t.Fatalf("response = %+v, want challenge metadata", result)
	}
	if result.ExpiresAt.Location() != time.UTC ||
		result.ResendAfter.Location() != time.UTC {
		t.Fatalf("response times must use UTC: %+v", result)
	}
	if strings.Contains(response.Body.String(), "code_hash") ||
		strings.Contains(response.Body.String(), "123456") {
		t.Fatalf("response leaked sensitive verification data: %s", response.Body.String())
	}
}

func TestVerificationHandlerRejectsInvalidJSON(t *testing.T) {
	service := &fakeVerificationRequestService{}
	response := performVerificationRequest(
		t,
		service,
		&fakeVerificationRequestLimiter{allowed: true},
		slog.New(slog.NewTextHandler(io.Discard, nil)),
		`{"channel":123}`,
	)

	assertVerificationErrorResponse(
		t,
		response,
		http.StatusBadRequest,
		errorCodeInvalidVerificationRequest,
		"request body must be valid JSON",
	)
	if service.callCount != 0 {
		t.Fatalf("service call count = %d, want 0", service.callCount)
	}
}

func TestVerificationHandlerMapsApplicationErrors(t *testing.T) {
	tests := []struct {
		name        string
		err         error
		wantStatus  int
		wantCode    string
		wantMessage string
		wantRetry   bool
	}{
		{
			name:        "invalid channel",
			err:         authdomain.ErrInvalidVerificationChannel,
			wantStatus:  http.StatusBadRequest,
			wantCode:    errorCodeInvalidVerificationRequest,
			wantMessage: "verification request is invalid",
		},
		{
			name:        "invalid destination",
			err:         authdomain.ErrInvalidVerificationDestination,
			wantStatus:  http.StatusBadRequest,
			wantCode:    errorCodeInvalidVerificationRequest,
			wantMessage: "verification request is invalid",
		},
		{
			name:        "invalid purpose",
			err:         authdomain.ErrInvalidVerificationPurpose,
			wantStatus:  http.StatusBadRequest,
			wantCode:    errorCodeInvalidVerificationRequest,
			wantMessage: "verification request is invalid",
		},
		{
			name: "cooldown",
			err: &verificationapplication.CooldownError{
				RetryAt: time.Now().UTC().Add(30 * time.Second),
			},
			wantStatus:  http.StatusTooManyRequests,
			wantCode:    errorCodeVerificationRequestThrottled,
			wantMessage: "verification requests are temporarily limited",
			wantRetry:   true,
		},
		{
			name: "delivery unavailable",
			err: fmtVerificationTestError(
				verificationapplication.ErrVerificationDeliveryUnavailable,
			),
			wantStatus:  http.StatusServiceUnavailable,
			wantCode:    errorCodeVerificationChannelUnavailable,
			wantMessage: "verification channel is temporarily unavailable",
		},
		{
			name:        "database failure",
			err:         errors.New("database unavailable"),
			wantStatus:  http.StatusInternalServerError,
			wantCode:    errorCodeInternal,
			wantMessage: "internal server error",
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			var logOutput bytes.Buffer
			service := &fakeVerificationRequestService{err: test.err}
			response := performVerificationRequest(
				t,
				service,
				&fakeVerificationRequestLimiter{allowed: true},
				slog.New(slog.NewJSONHandler(&logOutput, nil)),
				`{"channel":"email","destination":"owner@example.com","purpose":"register"}`,
			)

			assertVerificationErrorResponse(
				t,
				response,
				test.wantStatus,
				test.wantCode,
				test.wantMessage,
			)
			if test.wantRetry {
				retrySeconds, err := strconv.Atoi(response.Header().Get("Retry-After"))
				if err != nil || retrySeconds < 1 {
					t.Fatalf("Retry-After = %q, want positive seconds", response.Header().Get("Retry-After"))
				}
			}
			if strings.Contains(response.Body.String(), "database unavailable") {
				t.Fatalf("response leaked internal error: %s", response.Body.String())
			}
		})
	}
}

func TestVerificationHandlerAppliesClientRateLimitBeforeApplication(t *testing.T) {
	service := &fakeVerificationRequestService{}
	limiter := &fakeVerificationRequestLimiter{
		allowed: false,
		retryAt: time.Now().UTC().Add(time.Minute),
	}
	response := performVerificationRequest(
		t,
		service,
		limiter,
		slog.New(slog.NewTextHandler(io.Discard, nil)),
		`not even JSON`,
	)

	assertVerificationErrorResponse(
		t,
		response,
		http.StatusTooManyRequests,
		errorCodeVerificationRequestThrottled,
		"verification requests are temporarily limited",
	)
	if service.callCount != 0 {
		t.Fatalf("service call count = %d, want 0", service.callCount)
	}
}

func assertVerificationErrorResponse(
	t *testing.T,
	response *httptest.ResponseRecorder,
	wantStatus int,
	wantCode string,
	wantMessage string,
) {
	t.Helper()

	if response.Code != wantStatus {
		t.Fatalf("status = %d, want %d; body = %s", response.Code, wantStatus, response.Body.String())
	}
	var result errorResponse
	if err := json.Unmarshal(response.Body.Bytes(), &result); err != nil {
		t.Fatalf("decode error response: %v", err)
	}
	if result.Code != wantCode || result.Error != wantMessage {
		t.Fatalf("error response = %+v, want code=%q error=%q", result, wantCode, wantMessage)
	}
}

func fmtVerificationTestError(category error) error {
	return errors.Join(category, errors.New("test underlying failure"))
}
