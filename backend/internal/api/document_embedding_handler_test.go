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
	documentdomain "rag-reasoning-platform/backend/internal/domain/document"
	embeddingdomain "rag-reasoning-platform/backend/internal/domain/embedding"
)

type fakeEmbeddingQueueService struct {
	queueFunc  func(context.Context, int64) (embeddingdomain.Job, error)
	queueCalls int
}

func (f *fakeEmbeddingQueueService) Queue(
	ctx context.Context,
	documentID int64,
) (embeddingdomain.Job, error) {
	f.queueCalls++
	return f.queueFunc(ctx, documentID)
}

func newTestEmbeddingRouter(service embeddingQueueService) *gin.Engine {
	gin.SetMode(gin.TestMode)

	router := gin.New()
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
					int64,
				) (embeddingdomain.Job, error) {
					t.Fatal("Queue() must not be called for invalid ID")
					return embeddingdomain.Job{}, nil
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
			name:       "document not ready",
			serviceErr: embeddingapplication.ErrDocumentNotReady,
			statusCode: http.StatusConflict,
			message:    "document is not ready for embedding",
		},
		{
			name:       "active embedding job",
			serviceErr: embeddingdomain.ErrActiveJobExists,
			statusCode: http.StatusConflict,
			message:    "document embedding is already queued",
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
					documentID int64,
				) (embeddingdomain.Job, error) {
					if documentID != 7 {
						t.Fatalf("Queue() documentID = %d, want 7", documentID)
					}
					return embeddingdomain.Job{}, testCase.serviceErr
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

func TestDocumentEmbeddingHandlerReturnsAcceptedJob(t *testing.T) {
	expectedJob := embeddingdomain.Job{
		ID:           17,
		DocumentID:   7,
		ModelName:    "text-embedding-3-small",
		Dimensions:   1536,
		Status:       embeddingdomain.JobStatusQueued,
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
	service := &fakeEmbeddingQueueService{
		queueFunc: func(
			_ context.Context,
			documentID int64,
		) (embeddingdomain.Job, error) {
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
	router := newTestEmbeddingRouter(service)
	request := httptest.NewRequest(
		http.MethodPost,
		"/documents/7/embeddings",
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

	var actualResponse embeddingJobResponse
	if err := json.Unmarshal(response.Body.Bytes(), &actualResponse); err != nil {
		t.Fatalf("decode response JSON: %v", err)
	}
	expectedResponse := newEmbeddingJobResponse(expectedJob)
	if actualResponse != expectedResponse {
		t.Fatalf(
			"response = %+v, want %+v",
			actualResponse,
			expectedResponse,
		)
	}
}
