package api

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"reflect"
	"testing"
	"time"

	"github.com/gin-gonic/gin"

	embeddingapplication "rag-reasoning-platform/backend/internal/application/embedding"
	accessdomain "rag-reasoning-platform/backend/internal/domain/access"
	embeddingdomain "rag-reasoning-platform/backend/internal/domain/embedding"
)

// fakeEmbeddingJobQueryService 模拟 Application 查询服务。
// Handler 测试只验证 HTTP 边界，不连接真实数据库。
type fakeEmbeddingJobQueryService struct {
	getByIDFunc  func(context.Context, accessdomain.OwnerScope, int64) (embeddingdomain.Job, error)
	getByIDCalls int
}

func (f *fakeEmbeddingJobQueryService) GetByID(
	ctx context.Context,
	scope accessdomain.OwnerScope,
	jobID int64,
) (embeddingdomain.Job, error) {
	f.getByIDCalls++
	return f.getByIDFunc(ctx, scope, jobID)
}

func newTestEmbeddingJobRouter(
	service embeddingJobQueryService,
) *gin.Engine {
	gin.SetMode(gin.TestMode)

	router := gin.New()
	useTestAuthenticatedIdentity(router)
	handler := NewEmbeddingJobHandler(service)
	handler.RegisterRoutes(router)

	return router
}

func TestEmbeddingJobHandlerRejectsInvalidID(t *testing.T) {
	testCases := []string{
		"/embedding-jobs/abc",
		"/embedding-jobs/0",
		"/embedding-jobs/-1",
	}

	for _, path := range testCases {
		t.Run(path, func(t *testing.T) {
			service := &fakeEmbeddingJobQueryService{
				getByIDFunc: func(
					context.Context,
					accessdomain.OwnerScope,
					int64,
				) (embeddingdomain.Job, error) {
					t.Fatal("GetByID() must not be called for invalid ID")
					return embeddingdomain.Job{}, nil
				},
			}
			router := newTestEmbeddingJobRouter(service)
			request := httptest.NewRequest(http.MethodGet, path, nil)
			response := httptest.NewRecorder()

			router.ServeHTTP(response, request)

			assertErrorResponse(
				t,
				response,
				http.StatusBadRequest,
				"embedding job ID must be a positive integer",
			)
			if service.getByIDCalls != 0 {
				t.Fatalf(
					"GetByID() calls = %d, want 0",
					service.getByIDCalls,
				)
			}
		})
	}
}

func TestEmbeddingJobHandlerMapsServiceErrors(t *testing.T) {
	testCases := []struct {
		name       string
		serviceErr error
		statusCode int
		message    string
	}{
		{
			name:       "invalid ID",
			serviceErr: embeddingapplication.ErrInvalidEmbeddingJobID,
			statusCode: http.StatusBadRequest,
			message:    "embedding job ID must be a positive integer",
		},
		{
			name:       "job not found",
			serviceErr: embeddingdomain.ErrJobNotFound,
			statusCode: http.StatusNotFound,
			message:    "embedding job not found",
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
			service := &fakeEmbeddingJobQueryService{
				getByIDFunc: func(
					context.Context,
					accessdomain.OwnerScope,
					int64,
				) (embeddingdomain.Job, error) {
					return embeddingdomain.Job{}, testCase.serviceErr
				},
			}
			router := newTestEmbeddingJobRouter(service)
			request := httptest.NewRequest(
				http.MethodGet,
				"/embedding-jobs/23",
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
			if service.getByIDCalls != 1 {
				t.Fatalf(
					"GetByID() calls = %d, want 1",
					service.getByIDCalls,
				)
			}
		})
	}
}

func TestEmbeddingJobHandlerReturnsJob(t *testing.T) {
	errorMessage := "embedding API temporarily unavailable"
	promptTokens := 320
	totalTokens := 320
	createdAt := time.Date(2026, time.August, 14, 9, 0, 0, 0, time.UTC)
	nextAttemptAt := createdAt.Add(5 * time.Minute)
	startedAt := createdAt.Add(time.Minute)
	expectedJob := embeddingdomain.Job{
		ID:            23,
		DocumentID:    7,
		ModelName:     "text-embedding-v4",
		Dimensions:    1024,
		Status:        embeddingdomain.JobStatusQueued,
		AttemptCount:  1,
		ErrorMessage:  &errorMessage,
		NextAttemptAt: nextAttemptAt,
		PromptTokens:  &promptTokens,
		TotalTokens:   &totalTokens,
		CreatedAt:     createdAt,
		UpdatedAt:     nextAttemptAt,
		StartedAt:     &startedAt,
	}
	service := &fakeEmbeddingJobQueryService{
		getByIDFunc: func(
			_ context.Context,
			scope accessdomain.OwnerScope,
			jobID int64,
		) (embeddingdomain.Job, error) {
			if scope.OwnerUserID() != testAPIOwnerUserID {
				t.Fatalf("GetByID() scope owner = %d, want %d", scope.OwnerUserID(), testAPIOwnerUserID)
			}
			if jobID != expectedJob.ID {
				t.Fatalf(
					"GetByID() jobID = %d, want %d",
					jobID,
					expectedJob.ID,
				)
			}
			return expectedJob, nil
		},
	}
	router := newTestEmbeddingJobRouter(service)
	request := httptest.NewRequest(
		http.MethodGet,
		"/embedding-jobs/23",
		nil,
	)
	response := httptest.NewRecorder()

	router.ServeHTTP(response, request)

	if response.Code != http.StatusOK {
		t.Fatalf("status = %d, want %d", response.Code, http.StatusOK)
	}

	var actualResponse embeddingJobResponse
	if err := json.Unmarshal(response.Body.Bytes(), &actualResponse); err != nil {
		t.Fatalf("decode response JSON: %v", err)
	}
	expectedResponse := newEmbeddingJobResponse(expectedJob)
	if !reflect.DeepEqual(actualResponse, expectedResponse) {
		t.Fatalf(
			"response = %+v, want %+v",
			actualResponse,
			expectedResponse,
		)
	}
	if service.getByIDCalls != 1 {
		t.Fatalf("GetByID() calls = %d, want 1", service.getByIDCalls)
	}
}
