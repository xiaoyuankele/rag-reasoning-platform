package api

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/gin-gonic/gin"

	embeddingapplication "rag-reasoning-platform/backend/internal/application/embedding"
	accessdomain "rag-reasoning-platform/backend/internal/domain/access"
	embeddingdomain "rag-reasoning-platform/backend/internal/domain/embedding"
)

type fakeEmbeddingJobCancelService struct {
	cancelFunc  func(context.Context, accessdomain.OwnerScope, int64) (embeddingdomain.Job, error)
	cancelCalls int
}

func (f *fakeEmbeddingJobCancelService) Cancel(
	ctx context.Context,
	scope accessdomain.OwnerScope,
	jobID int64,
) (embeddingdomain.Job, error) {
	f.cancelCalls++
	return f.cancelFunc(ctx, scope, jobID)
}

func newTestEmbeddingJobCancelRouter(service embeddingJobCancelService) *gin.Engine {
	gin.SetMode(gin.TestMode)
	router := gin.New()
	useTestAuthenticatedIdentity(router)
	logger := slog.New(slog.NewTextHandler(io.Discard, nil))
	NewEmbeddingJobCancelHandler(service, logger).RegisterRoutes(router)
	return router
}

func TestEmbeddingJobCancelHandlerRejectsInvalidID(t *testing.T) {
	service := &fakeEmbeddingJobCancelService{
		cancelFunc: func(context.Context, accessdomain.OwnerScope, int64) (embeddingdomain.Job, error) {
			t.Fatal("Cancel() must not be called for invalid ID")
			return embeddingdomain.Job{}, nil
		},
	}
	router := newTestEmbeddingJobCancelRouter(service)
	request := httptest.NewRequest(http.MethodPost, "/embedding-jobs/abc/cancel", nil)
	response := httptest.NewRecorder()

	router.ServeHTTP(response, request)

	assertEmbeddingCancelError(
		t,
		response,
		http.StatusBadRequest,
		errorCodeInvalidEmbeddingJobID,
		"embedding job ID must be a positive integer",
	)
	if service.cancelCalls != 0 {
		t.Fatalf("Cancel() calls = %d, want 0", service.cancelCalls)
	}
}

func TestEmbeddingJobCancelHandlerMapsServiceErrors(t *testing.T) {
	testCases := []struct {
		name       string
		err        error
		statusCode int
		code       string
		message    string
	}{
		{name: "invalid ID", err: embeddingapplication.ErrInvalidEmbeddingJobID, statusCode: http.StatusBadRequest, code: errorCodeInvalidEmbeddingJobID, message: "embedding job ID must be a positive integer"},
		{name: "not found", err: embeddingdomain.ErrJobNotFound, statusCode: http.StatusNotFound, code: errorCodeEmbeddingJobNotFound, message: "embedding job not found"},
		{name: "processing", err: embeddingdomain.ErrJobProcessingCannotCancel, statusCode: http.StatusConflict, code: errorCodeEmbeddingJobProcessing, message: "processing embedding job cannot be canceled"},
		{name: "terminal", err: embeddingdomain.ErrJobTerminalCannotCancel, statusCode: http.StatusConflict, code: errorCodeEmbeddingJobTerminal, message: "completed embedding job cannot be canceled"},
		{name: "internal", err: errors.New("database unavailable"), statusCode: http.StatusInternalServerError, code: errorCodeInternal, message: "internal server error"},
	}

	for _, testCase := range testCases {
		t.Run(testCase.name, func(t *testing.T) {
			service := &fakeEmbeddingJobCancelService{
				cancelFunc: func(
					context.Context,
					accessdomain.OwnerScope,
					int64,
				) (embeddingdomain.Job, error) {
					return embeddingdomain.Job{}, testCase.err
				},
			}
			response := httptest.NewRecorder()
			newTestEmbeddingJobCancelRouter(service).ServeHTTP(
				response,
				httptest.NewRequest(http.MethodPost, "/embedding-jobs/19/cancel", nil),
			)

			assertEmbeddingCancelError(t, response, testCase.statusCode, testCase.code, testCase.message)
		})
	}
}

func assertEmbeddingCancelError(
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
	var actual errorResponse
	if err := json.Unmarshal(response.Body.Bytes(), &actual); err != nil {
		t.Fatalf("decode error response: %v", err)
	}
	if actual.Code != wantCode || actual.Error != wantMessage {
		t.Fatalf("error response = %+v, want code=%q error=%q", actual, wantCode, wantMessage)
	}
}

func TestEmbeddingJobCancelHandlerReturnsCanceledJob(t *testing.T) {
	expectedJob := embeddingdomain.Job{
		ID:         19,
		DocumentID: 7,
		Status:     embeddingdomain.JobStatusCanceled,
	}
	service := &fakeEmbeddingJobCancelService{
		cancelFunc: func(
			_ context.Context,
			scope accessdomain.OwnerScope,
			jobID int64,
		) (embeddingdomain.Job, error) {
			if scope.OwnerUserID() != testAPIOwnerUserID || jobID != expectedJob.ID {
				t.Fatalf("Cancel() received owner=%d job=%d", scope.OwnerUserID(), jobID)
			}
			return expectedJob, nil
		},
	}
	response := httptest.NewRecorder()
	newTestEmbeddingJobCancelRouter(service).ServeHTTP(
		response,
		httptest.NewRequest(http.MethodPost, "/embedding-jobs/19/cancel", nil),
	)

	if response.Code != http.StatusOK {
		t.Fatalf("status = %d, want %d", response.Code, http.StatusOK)
	}
	var actual embeddingJobResponse
	if err := json.Unmarshal(response.Body.Bytes(), &actual); err != nil {
		t.Fatalf("decode response: %v", err)
	}
	if actual != newEmbeddingJobResponse(expectedJob) {
		t.Fatalf("response = %+v, want %+v", actual, newEmbeddingJobResponse(expectedJob))
	}
}
