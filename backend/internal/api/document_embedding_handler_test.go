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

	embeddingapplication "rag-reasoning-platform/backend/internal/application/embedding"
	accessdomain "rag-reasoning-platform/backend/internal/domain/access"
	documentdomain "rag-reasoning-platform/backend/internal/domain/document"
	embeddingdomain "rag-reasoning-platform/backend/internal/domain/embedding"
)

type fakeEmbeddingQueueService struct {
	queueFunc  func(context.Context, accessdomain.OwnerScope, int64) (embeddingdomain.JobRequestResult, error)
	queueCalls int
}

func (f *fakeEmbeddingQueueService) Queue(
	ctx context.Context,
	scope accessdomain.OwnerScope,
	documentID int64,
) (embeddingdomain.JobRequestResult, error) {
	f.queueCalls++
	return f.queueFunc(ctx, scope, documentID)
}

func newTestEmbeddingRouter(service embeddingQueueService) *gin.Engine {
	gin.SetMode(gin.TestMode)

	router := gin.New()
	useTestAuthenticatedIdentity(router)
	handler := NewDocumentEmbeddingHandler(service)
	handler.RegisterRoutes(router)
	return router
}

func TestDocumentEmbeddingHandlerRejectsInvalidID(t *testing.T) {
	paths := []string{
		"/documents/abc/embeddings",
		"/documents/0/embeddings",
		"/documents/-1/embeddings",
	}

	for _, path := range paths {
		t.Run(path, func(t *testing.T) {
			service := &fakeEmbeddingQueueService{
				queueFunc: func(
					context.Context,
					accessdomain.OwnerScope,
					int64,
				) (embeddingdomain.JobRequestResult, error) {
					t.Fatal("Queue() must not be called for invalid ID")
					return embeddingdomain.JobRequestResult{}, nil
				},
			}
			router := newTestEmbeddingRouter(service)
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
				t.Fatalf("Queue() calls = %d, want 0", service.queueCalls)
			}
		})
	}
}

func TestDocumentEmbeddingHandlerMapsServiceErrors(t *testing.T) {
	testCases := []struct {
		name       string
		serviceErr error
		statusCode int
		message    string
	}{
		{
			name:       "invalid ID",
			serviceErr: embeddingapplication.ErrInvalidDocumentID,
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
			name:       "internal error",
			serviceErr: errors.New("database unavailable"),
			statusCode: http.StatusInternalServerError,
			message:    "internal server error",
		},
	}

	for _, testCase := range testCases {
		t.Run(testCase.name, func(t *testing.T) {
			service := &fakeEmbeddingQueueService{
				queueFunc: func(
					_ context.Context,
					scope accessdomain.OwnerScope,
					documentID int64,
				) (embeddingdomain.JobRequestResult, error) {
					if scope.OwnerUserID() != testAPIOwnerUserID {
						t.Fatalf("Queue() scope owner = %d, want %d", scope.OwnerUserID(), testAPIOwnerUserID)
					}
					if documentID != 7 {
						t.Fatalf("Queue() documentID = %d, want 7", documentID)
					}
					return embeddingdomain.JobRequestResult{}, testCase.serviceErr
				},
			}
			router := newTestEmbeddingRouter(service)
			request := httptest.NewRequest(
				http.MethodPost,
				"/documents/7/embeddings",
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
				t.Fatalf("Queue() calls = %d, want 1", service.queueCalls)
			}
		})
	}
}

func TestDocumentEmbeddingHandlerMapsAdmissionCapacityErrors(t *testing.T) {
	testCases := []struct {
		name       string
		serviceErr error
		statusCode int
		code       string
		message    string
	}{
		{
			name:       "owner limit",
			serviceErr: embeddingdomain.ErrOwnerActiveJobLimitExceeded,
			statusCode: http.StatusTooManyRequests,
			code:       errorCodeEmbeddingOwnerJobLimit,
			message:    "too many active embedding jobs for this user",
		},
		{
			name:       "global capacity",
			serviceErr: embeddingdomain.ErrGlobalActiveJobLimitExceeded,
			statusCode: http.StatusServiceUnavailable,
			code:       errorCodeEmbeddingQueueCapacity,
			message:    "embedding queue is temporarily full",
		},
	}

	for _, testCase := range testCases {
		t.Run(testCase.name, func(t *testing.T) {
			service := &fakeEmbeddingQueueService{
				queueFunc: func(
					context.Context,
					accessdomain.OwnerScope,
					int64,
				) (embeddingdomain.JobRequestResult, error) {
					return embeddingdomain.JobRequestResult{}, testCase.serviceErr
				},
			}
			response := httptest.NewRecorder()
			newTestEmbeddingRouter(service).ServeHTTP(
				response,
				httptest.NewRequest(http.MethodPost, "/documents/7/embeddings", nil),
			)

			if response.Code != testCase.statusCode {
				t.Fatalf("status = %d, want %d", response.Code, testCase.statusCode)
			}
			if response.Header().Get("Retry-After") != "5" {
				t.Fatalf("Retry-After = %q, want 5", response.Header().Get("Retry-After"))
			}
			var actual errorResponse
			if err := json.Unmarshal(response.Body.Bytes(), &actual); err != nil {
				t.Fatalf("decode error response: %v", err)
			}
			if actual.Code != testCase.code || actual.Error != testCase.message {
				t.Fatalf("error response = %+v, want code=%q message=%q", actual, testCase.code, testCase.message)
			}
		})
	}
}

func TestDocumentEmbeddingHandlerReturnsRequestedJob(t *testing.T) {
	expectedJob := embeddingdomain.Job{
		ID:           17,
		DocumentID:   7,
		ModelName:    "text-embedding-3-small",
		Dimensions:   1536,
		Status:       embeddingdomain.JobStatusWaitingDocument,
		AttemptCount: 0,
		CreatedAt: time.Date(
			2026,
			time.August,
			11,
			16,
			0,
			0,
			0,
			time.UTC,
		),
	}
	testCases := []struct {
		name       string
		created    bool
		statusCode int
	}{
		{name: "new job", created: true, statusCode: http.StatusAccepted},
		{name: "existing active job", created: false, statusCode: http.StatusOK},
	}

	for _, testCase := range testCases {
		t.Run(testCase.name, func(t *testing.T) {
			service := &fakeEmbeddingQueueService{
				queueFunc: func(
					_ context.Context,
					scope accessdomain.OwnerScope,
					documentID int64,
				) (embeddingdomain.JobRequestResult, error) {
					if scope.OwnerUserID() != testAPIOwnerUserID {
						t.Fatalf("Queue() scope owner = %d, want %d", scope.OwnerUserID(), testAPIOwnerUserID)
					}
					if documentID != expectedJob.DocumentID {
						t.Fatalf("Queue() documentID = %d, want %d", documentID, expectedJob.DocumentID)
					}
					return embeddingdomain.JobRequestResult{Job: expectedJob, Created: testCase.created}, nil
				},
			}
			router := newTestEmbeddingRouter(service)
			request := httptest.NewRequest(http.MethodPost, "/documents/7/embeddings", nil)
			response := httptest.NewRecorder()

			router.ServeHTTP(response, request)

			if response.Code != testCase.statusCode {
				t.Fatalf("status = %d, want %d", response.Code, testCase.statusCode)
			}
			var actualResponse embeddingJobResponse
			if err := json.Unmarshal(response.Body.Bytes(), &actualResponse); err != nil {
				t.Fatalf("decode response JSON: %v", err)
			}
			expectedResponse := newEmbeddingJobResponse(expectedJob)
			if actualResponse != expectedResponse {
				t.Fatalf("response = %+v, want %+v", actualResponse, expectedResponse)
			}
		})
	}
}
