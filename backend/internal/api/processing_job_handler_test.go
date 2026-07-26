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

	applicationdocument "rag-reasoning-platform/backend/internal/application/document"
	documentdomain "rag-reasoning-platform/backend/internal/domain/document"
)

type fakeProcessingJobQueryService struct {
	getByIDFunc  func(context.Context, int64) (documentdomain.ProcessingJob, error)
	getByIDCalls int
}

func (f *fakeProcessingJobQueryService) GetByID(
	ctx context.Context,
	jobID int64,
) (documentdomain.ProcessingJob, error) {
	f.getByIDCalls++
	return f.getByIDFunc(ctx, jobID)
}

func newTestProcessingJobRouter(
	service processingJobQueryService,
) *gin.Engine {
	gin.SetMode(gin.TestMode)

	router := gin.New()
	handler := NewProcessingJobHandler(service)
	handler.RegisterRoutes(router)

	return router
}

func TestProcessingJobHandlerRejectsInvalidID(t *testing.T) {
	testCases := []string{
		"/processing-jobs/abc",
		"/processing-jobs/0",
		"/processing-jobs/-1",
	}

	for _, path := range testCases {
		t.Run(path, func(t *testing.T) {
			service := &fakeProcessingJobQueryService{
				getByIDFunc: func(
					context.Context,
					int64,
				) (documentdomain.ProcessingJob, error) {
					t.Fatal("GetByID() must not be called for invalid ID")
					return documentdomain.ProcessingJob{}, nil
				},
			}
			router := newTestProcessingJobRouter(service)
			request := httptest.NewRequest(http.MethodGet, path, nil)
			response := httptest.NewRecorder()

			router.ServeHTTP(response, request)

			assertErrorResponse(
				t,
				response,
				http.StatusBadRequest,
				"processing job ID must be a positive integer",
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

func TestProcessingJobHandlerMapsServiceErrors(t *testing.T) {
	testCases := []struct {
		name       string
		serviceErr error
		statusCode int
		message    string
	}{
		{
			name:       "invalid ID",
			serviceErr: applicationdocument.ErrInvalidProcessingJobID,
			statusCode: http.StatusBadRequest,
			message:    "processing job ID must be a positive integer",
		},
		{
			name:       "job not found",
			serviceErr: documentdomain.ErrProcessingJobNotFound,
			statusCode: http.StatusNotFound,
			message:    "processing job not found",
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
			service := &fakeProcessingJobQueryService{
				getByIDFunc: func(
					context.Context,
					int64,
				) (documentdomain.ProcessingJob, error) {
					return documentdomain.ProcessingJob{},
						testCase.serviceErr
				},
			}
			router := newTestProcessingJobRouter(service)
			request := httptest.NewRequest(
				http.MethodGet,
				"/processing-jobs/17",
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

func TestProcessingJobHandlerReturnsJob(t *testing.T) {
	errorMessage := "parser failed"
	startedAt := time.Date(
		2026, time.July, 27,
		9, 0, 0, 0,
		time.UTC,
	)
	completedAt := startedAt.Add(time.Minute)
	expectedJob := documentdomain.ProcessingJob{
		ID:           17,
		DocumentID:   7,
		Status:       documentdomain.ProcessingJobStatusFailed,
		AttemptCount: 1,
		ErrorMessage: &errorMessage,
		CreatedAt:    startedAt.Add(-time.Minute),
		UpdatedAt:    completedAt,
		StartedAt:    &startedAt,
		CompletedAt:  &completedAt,
	}
	service := &fakeProcessingJobQueryService{
		getByIDFunc: func(
			_ context.Context,
			jobID int64,
		) (documentdomain.ProcessingJob, error) {
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
	router := newTestProcessingJobRouter(service)
	request := httptest.NewRequest(
		http.MethodGet,
		"/processing-jobs/17",
		nil,
	)
	response := httptest.NewRecorder()

	router.ServeHTTP(response, request)

	if response.Code != http.StatusOK {
		t.Fatalf(
			"status = %d, want %d",
			response.Code,
			http.StatusOK,
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
	if !reflect.DeepEqual(actualResponse, expectedResponse) {
		t.Fatalf(
			"response = %+v, want %+v",
			actualResponse,
			expectedResponse,
		)
	}
	if service.getByIDCalls != 1 {
		t.Fatalf(
			"GetByID() calls = %d, want 1",
			service.getByIDCalls,
		)
	}
}
