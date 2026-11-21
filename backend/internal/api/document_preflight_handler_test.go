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
	"strings"
	"testing"

	"github.com/gin-gonic/gin"

	applicationdocument "rag-reasoning-platform/backend/internal/application/document"
	accessdomain "rag-reasoning-platform/backend/internal/domain/access"
	documentdomain "rag-reasoning-platform/backend/internal/domain/document"
)

type fakeDocumentPreflightService struct {
	checkFunc  func(context.Context, accessdomain.OwnerScope, applicationdocument.PreflightInput) (applicationdocument.PreflightResult, error)
	checkCalls int
}

func (f *fakeDocumentPreflightService) Check(
	ctx context.Context,
	scope accessdomain.OwnerScope,
	input applicationdocument.PreflightInput,
) (applicationdocument.PreflightResult, error) {
	f.checkCalls++
	return f.checkFunc(ctx, scope, input)
}

func newTestDocumentPreflightRouter(service documentPreflightService) *gin.Engine {
	gin.SetMode(gin.TestMode)
	router := gin.New()
	useTestAuthenticatedIdentity(router)
	NewDocumentPreflightHandler(
		service,
		slog.New(slog.NewTextHandler(io.Discard, nil)),
	).RegisterRoutes(router)
	return router
}

func performDocumentPreflightRequest(
	t *testing.T,
	router http.Handler,
	body string,
) *httptest.ResponseRecorder {
	t.Helper()
	request := httptest.NewRequest(
		http.MethodPost,
		"/documents/preflight",
		strings.NewReader(body),
	)
	request.Header.Set("Content-Type", "application/json")
	response := httptest.NewRecorder()
	router.ServeHTTP(response, request)
	return response
}

func TestDocumentPreflightHandlerReturnsExistingDocument(t *testing.T) {
	expectedHash := strings.Repeat("a", 64)
	expectedDocument := documentdomain.Document{
		ID:           41,
		OwnerUserID:  testAPIOwnerUserID,
		OriginalName: "existing.pdf",
		MIMEType:     "application/pdf",
		SizeBytes:    2048,
		SHA256:       expectedHash,
		Status:       documentdomain.StatusReady,
	}
	service := &fakeDocumentPreflightService{
		checkFunc: func(
			_ context.Context,
			scope accessdomain.OwnerScope,
			input applicationdocument.PreflightInput,
		) (applicationdocument.PreflightResult, error) {
			if scope.OwnerUserID() != testAPIOwnerUserID {
				t.Fatalf("owner ID = %d, want %d", scope.OwnerUserID(), testAPIOwnerUserID)
			}
			if input.SHA256 != expectedHash || input.SizeBytes != 2048 {
				t.Fatalf("input = %+v, want expected hash and size", input)
			}
			return applicationdocument.PreflightResult{
				Exists:   true,
				Document: expectedDocument,
			}, nil
		},
	}

	response := performDocumentPreflightRequest(
		t,
		newTestDocumentPreflightRouter(service),
		`{"sha256":"`+expectedHash+`","size_bytes":2048}`,
	)
	if response.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200; body=%s", response.Code, response.Body.String())
	}

	var body documentPreflightResponse
	if err := json.Unmarshal(response.Body.Bytes(), &body); err != nil {
		t.Fatalf("decode response: %v", err)
	}
	if !body.Exists || body.Document == nil || body.Document.ID != expectedDocument.ID {
		t.Fatalf("response = %+v, want existing document", body)
	}
}

func TestDocumentPreflightHandlerReturnsNotExisting(t *testing.T) {
	service := &fakeDocumentPreflightService{
		checkFunc: func(context.Context, accessdomain.OwnerScope, applicationdocument.PreflightInput) (applicationdocument.PreflightResult, error) {
			return applicationdocument.PreflightResult{Exists: false}, nil
		},
	}

	response := performDocumentPreflightRequest(
		t,
		newTestDocumentPreflightRouter(service),
		`{"sha256":"`+strings.Repeat("b", 64)+`","size_bytes":1}`,
	)
	if response.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200; body=%s", response.Code, response.Body.String())
	}
	if response.Body.String() != `{"exists":false,"document":null}` {
		t.Fatalf("body = %s, want stable null document response", response.Body.String())
	}
}

func TestDocumentPreflightHandlerRejectsMalformedJSON(t *testing.T) {
	service := &fakeDocumentPreflightService{
		checkFunc: func(context.Context, accessdomain.OwnerScope, applicationdocument.PreflightInput) (applicationdocument.PreflightResult, error) {
			t.Fatal("Check must not be called for malformed JSON")
			return applicationdocument.PreflightResult{}, nil
		},
	}

	response := performDocumentPreflightRequest(
		t,
		newTestDocumentPreflightRouter(service),
		`{"sha256":`,
	)
	if response.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, want 400", response.Code)
	}
	var body errorResponse
	if err := json.Unmarshal(response.Body.Bytes(), &body); err != nil {
		t.Fatalf("decode error response: %v", err)
	}
	if body.Code != errorCodeInvalidDocumentPreflight {
		t.Fatalf("code = %q, want %q", body.Code, errorCodeInvalidDocumentPreflight)
	}
	if service.checkCalls != 0 {
		t.Fatalf("Check calls = %d, want 0", service.checkCalls)
	}
}

func TestDocumentPreflightHandlerMapsApplicationErrors(t *testing.T) {
	tests := []struct {
		name       string
		serviceErr error
		wantStatus int
		wantCode   string
	}{
		{name: "invalid hash", serviceErr: applicationdocument.ErrInvalidPreflightSHA256, wantStatus: http.StatusBadRequest, wantCode: errorCodeInvalidDocumentPreflight},
		{name: "invalid size", serviceErr: applicationdocument.ErrInvalidPreflightSize, wantStatus: http.StatusBadRequest, wantCode: errorCodeInvalidDocumentPreflight},
		{name: "file too large", serviceErr: applicationdocument.ErrFileTooLarge, wantStatus: http.StatusRequestEntityTooLarge, wantCode: errorCodeFileTooLarge},
		{name: "internal error", serviceErr: errors.New("database unavailable"), wantStatus: http.StatusInternalServerError, wantCode: errorCodeInternal},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			service := &fakeDocumentPreflightService{
				checkFunc: func(context.Context, accessdomain.OwnerScope, applicationdocument.PreflightInput) (applicationdocument.PreflightResult, error) {
					return applicationdocument.PreflightResult{}, test.serviceErr
				},
			}
			response := performDocumentPreflightRequest(
				t,
				newTestDocumentPreflightRouter(service),
				`{"sha256":"`+strings.Repeat("c", 64)+`","size_bytes":1}`,
			)
			if response.Code != test.wantStatus {
				t.Fatalf("status = %d, want %d; body=%s", response.Code, test.wantStatus, response.Body.String())
			}
			var body errorResponse
			if err := json.Unmarshal(response.Body.Bytes(), &body); err != nil {
				t.Fatalf("decode error response: %v", err)
			}
			if body.Code != test.wantCode {
				t.Fatalf("code = %q, want %q", body.Code, test.wantCode)
			}
		})
	}
}

func TestDocumentPreflightHandlerLogsInternalFailureWithoutExposingDetails(t *testing.T) {
	var logOutput bytes.Buffer
	internalError := errors.New("sensitive database detail")
	service := &fakeDocumentPreflightService{
		checkFunc: func(context.Context, accessdomain.OwnerScope, applicationdocument.PreflightInput) (applicationdocument.PreflightResult, error) {
			return applicationdocument.PreflightResult{}, internalError
		},
	}
	router := gin.New()
	useTestAuthenticatedIdentity(router)
	NewDocumentPreflightHandler(
		service,
		slog.New(slog.NewJSONHandler(&logOutput, nil)),
	).RegisterRoutes(router)

	response := performDocumentPreflightRequest(
		t,
		router,
		`{"sha256":"`+strings.Repeat("d", 64)+`","size_bytes":1024}`,
	)
	if response.Code != http.StatusInternalServerError {
		t.Fatalf("status = %d, want 500", response.Code)
	}
	if strings.Contains(response.Body.String(), internalError.Error()) {
		t.Fatalf("response exposed internal detail: %s", response.Body.String())
	}
	if !strings.Contains(logOutput.String(), "document_preflight_failed") ||
		!strings.Contains(logOutput.String(), internalError.Error()) {
		t.Fatalf("log output = %s, want diagnostic code and internal error", logOutput.String())
	}
}
