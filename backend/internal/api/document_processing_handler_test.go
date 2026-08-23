package api

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/gin-gonic/gin"

	applicationdocument "rag-reasoning-platform/backend/internal/application/document"
	accessdomain "rag-reasoning-platform/backend/internal/domain/access"
	documentdomain "rag-reasoning-platform/backend/internal/domain/document"
)

type fakeDocumentProcessingQueueService struct {
	queueFunc  func(context.Context, accessdomain.OwnerScope, int64) (documentdomain.ProcessingJob, error)
	queueCalls int
}

func (f *fakeDocumentProcessingQueueService) Queue(
	ctx context.Context,
	scope accessdomain.OwnerScope,
	documentID int64,
) (documentdomain.ProcessingJob, error) {
	f.queueCalls++
	return f.queueFunc(ctx, scope, documentID)
}

func newTestDocumentProcessingRouter(
	service documentProcessingQueueService,
) *gin.Engine {
	gin.SetMode(gin.TestMode)

	router := gin.New()
	useTestAuthenticatedIdentity(router)
	handler := NewDocumentProcessingHandler(service)
	handler.RegisterRoutes(router)

	return router
}

func TestDocumentProcessingHandlerRejectsInvalidID(t *testing.T) {
	testCases := []string{
		"/documents/abc/process",
		"/documents/0/process",
		"/documents/-1/process",
	}

	for _, path := range testCases {
		t.Run(path, func(t *testing.T) {
			service := &fakeDocumentProcessingQueueService{
				queueFunc: func(
					context.Context,
					accessdomain.OwnerScope,
					int64,
				) (documentdomain.ProcessingJob, error) {
					t.Fatal("Queue() must not be called for invalid ID")
					return documentdomain.ProcessingJob{}, nil
				},
			}
			router := newTestDocumentProcessingRouter(service)
			request := httptest.NewRequest(http.MethodPost, path, nil)
			response := httptest.NewRecorder()

			router.ServeHTTP(response, request)

			assertErrorResponse(
				t,
				response,
				http.StatusBadRequest,
				"document ID must be a positive integer",
			)
			if service.queueCalls != 0 {
				t.Fatalf(
					"Queue() calls = %d, want 0",
					service.queueCalls,
				)
			}
		})
	}
}

func TestDocumentProcessingHandlerMapsServiceErrors(t *testing.T) {
	testCases := []struct {
		name       string
		serviceErr error
		statusCode int
		message    string
	}{
		{
			name:       "invalid ID",
			serviceErr: applicationdocument.ErrInvalidID,
			statusCode: http.StatusBadRequest,
			message:    "document ID must be a positive integer",
		},
		{
			name:       "document not found",
			serviceErr: documentdomain.ErrNotFound,
			statusCode: http.StatusNotFound,
			message:    "document not found",
		},
		{
			name:       "document status conflict",
			serviceErr: applicationdocument.ErrDocumentNotProcessable,
			statusCode: http.StatusConflict,
			message:    "document is not available for processing",
		},
		{
			name:       "active job conflict",
			serviceErr: documentdomain.ErrActiveProcessingJobExists,
			statusCode: http.StatusConflict,
			message:    "document processing is already queued",
		},
		{
			name:       "unknown internal error",
			serviceErr: errors.New("database unavailable"),
			statusCode: http.StatusInternalServerError,
			message:    "internal server error",
		},
	}

	for _, testCase := range testCases {
		t.Run(testCase.name, func(t *testing.T) {
			service := &fakeDocumentProcessingQueueService{
				queueFunc: func(
					_ context.Context,
					scope accessdomain.OwnerScope,
					documentID int64,
				) (documentdomain.ProcessingJob, error) {
					if scope.OwnerUserID() != testAPIOwnerUserID {
						t.Fatalf("Queue() scope owner = %d, want %d", scope.OwnerUserID(), testAPIOwnerUserID)
					}
					if documentID != 7 {
						t.Fatalf(
							"Queue() documentID = %d, want 7",
							documentID,
						)
					}

					return documentdomain.ProcessingJob{},
						testCase.serviceErr
				},
			}
			router := newTestDocumentProcessingRouter(service)
			request := httptest.NewRequest(
				http.MethodPost,
				"/documents/7/process",
				nil,
			)
			response := httptest.NewRecorder()

			router.ServeHTTP(response, request)

			assertErrorResponse(
				t,
				response,
				testCase.statusCode,
				testCase.message,
			)
			if service.queueCalls != 1 {
				t.Fatalf(
					"Queue() calls = %d, want 1",
					service.queueCalls,
				)
			}
		})
	}
}

func TestDocumentProcessingHandlerMapsAdmissionErrors(t *testing.T) {
	testCases := []struct {
		name       string
		serviceErr error
		statusCode int
		code       string
		message    string
	}{
		{
			name: "owner limit",
			serviceErr: documentdomain.
				ErrOwnerActiveProcessingJobLimitExceeded,
			statusCode: http.StatusTooManyRequests,
			code:       errorCodeProcessingOwnerJobLimit,
			message:    "too many active processing jobs for this user",
		},
		{
			name:       "global capacity",
			serviceErr: documentdomain.ErrGlobalProcessingJobLimitExceeded,
			statusCode: http.StatusServiceUnavailable,
			code:       errorCodeProcessingQueueCapacity,
			message:    "processing queue is temporarily full",
		},
	}

	for _, testCase := range testCases {
		t.Run(testCase.name, func(t *testing.T) {
			service := &fakeDocumentProcessingQueueService{
				queueFunc: func(
					context.Context,
					accessdomain.OwnerScope,
					int64,
				) (documentdomain.ProcessingJob, error) {
					return documentdomain.ProcessingJob{}, testCase.serviceErr
				},
			}
			response := httptest.NewRecorder()
			newTestDocumentProcessingRouter(service).ServeHTTP(
				response,
				httptest.NewRequest(
					http.MethodPost,
					"/documents/7/process",
					nil,
				),
			)

			if response.Code != testCase.statusCode {
				t.Fatalf(
					"status = %d, want %d",
					response.Code,
					testCase.statusCode,
				)
			}
			if retryAfter := response.Header().Get("Retry-After"); retryAfter != "5" {
				t.Fatalf("Retry-After = %q, want 5", retryAfter)
			}

			var actual errorResponse
			if err := json.Unmarshal(response.Body.Bytes(), &actual); err != nil {
				t.Fatalf("decode error response: %v", err)
			}
			if actual.Code != testCase.code || actual.Error != testCase.message {
				t.Fatalf(
					"error response = %+v, want code=%q message=%q",
					actual,
					testCase.code,
					testCase.message,
				)
			}
		})
	}
}

func TestDocumentProcessingHandlerReturnsAcceptedJob(t *testing.T) {
	expectedJob := documentdomain.ProcessingJob{
		ID:           13,
		DocumentID:   7,
		Status:       documentdomain.ProcessingJobStatusQueued,
		AttemptCount: 0,
		CreatedAt: time.Date(
			2026,
			time.July,
			26,
			16,
			30,
			0,
			0,
			time.UTC,
		),
	}
	service := &fakeDocumentProcessingQueueService{
		queueFunc: func(
			_ context.Context,
			scope accessdomain.OwnerScope,
			documentID int64,
		) (documentdomain.ProcessingJob, error) {
			if scope.OwnerUserID() != testAPIOwnerUserID {
				t.Fatalf("Queue() scope owner = %d, want %d", scope.OwnerUserID(), testAPIOwnerUserID)
			}
			if documentID != expectedJob.DocumentID {
				t.Fatalf(
					"Queue() documentID = %d, want %d",
					documentID,
					expectedJob.DocumentID,
				)
			}
			return expectedJob, nil
		},
	}
	router := newTestDocumentProcessingRouter(service)
	request := httptest.NewRequest(
		http.MethodPost,
		"/documents/7/process",
		nil,
	)
	response := httptest.NewRecorder()

	router.ServeHTTP(response, request)

	if response.Code != http.StatusAccepted {
		t.Fatalf(
			"status = %d, want %d",
			response.Code,
			http.StatusAccepted,
		)
	}

	var actualResponse processingJobResponse
	if err := json.Unmarshal(
		response.Body.Bytes(),
		&actualResponse,
	); err != nil {
		t.Fatalf("decode response JSON: %v", err)
	}

	expectedResponse := newProcessingJobResponse(expectedJob)
	if actualResponse != expectedResponse {
		t.Fatalf(
			"response = %+v, want %+v",
			actualResponse,
			expectedResponse,
		)
	}
	if service.queueCalls != 1 {
		t.Fatalf(
			"Queue() calls = %d, want 1",
			service.queueCalls,
		)
	}
}

func assertErrorResponse(
	t *testing.T,
	response *httptest.ResponseRecorder,
	expectedStatus int,
	expectedMessage string,
) {
	t.Helper()

	if response.Code != expectedStatus {
		t.Fatalf(
			"status = %d, want %d",
			response.Code,
			expectedStatus,
		)
	}

	expectedBody := `{"error":"` + expectedMessage + `"}`
	if response.Body.String() != expectedBody {
		t.Fatalf(
			"body = %s, want %s",
			response.Body.String(),
			expectedBody,
		)
	}
}
