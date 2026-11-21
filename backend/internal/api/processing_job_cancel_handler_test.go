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

	applicationdocument "rag-reasoning-platform/backend/internal/application/document"
	accessdomain "rag-reasoning-platform/backend/internal/domain/access"
	documentdomain "rag-reasoning-platform/backend/internal/domain/document"
)

type fakeProcessingJobCancelService struct {
	cancelFunc  func(context.Context, accessdomain.OwnerScope, int64) (documentdomain.ProcessingJob, error)
	cancelCalls int
}

func (f *fakeProcessingJobCancelService) Cancel(
	ctx context.Context,
	scope accessdomain.OwnerScope,
	jobID int64,
) (documentdomain.ProcessingJob, error) {
	f.cancelCalls++
	return f.cancelFunc(ctx, scope, jobID)
}

func newTestProcessingJobCancelRouter(
	service processingJobCancelService,
) *gin.Engine {
	gin.SetMode(gin.TestMode)
	router := gin.New()
	router.Use(RequestIDMiddleware())
	useTestAuthenticatedIdentity(router)
	logger := slog.New(slog.NewTextHandler(io.Discard, nil))
	NewProcessingJobCancelHandler(service, logger).RegisterRoutes(router)
	return router
}

func TestProcessingJobCancelHandlerRejectsInvalidID(t *testing.T) {
	service := &fakeProcessingJobCancelService{
		cancelFunc: func(context.Context, accessdomain.OwnerScope, int64) (documentdomain.ProcessingJob, error) {
			t.Fatal("Cancel() must not be called for invalid ID")
			return documentdomain.ProcessingJob{}, nil
		},
	}
	response := httptest.NewRecorder()
	newTestProcessingJobCancelRouter(service).ServeHTTP(
		response,
		httptest.NewRequest(http.MethodPost, "/processing-jobs/abc/cancel", nil),
	)

	assertCodedErrorResponse(
		t,
		response,
		http.StatusBadRequest,
		"processing job ID must be a positive integer",
		errorCodeInvalidProcessingJobID,
	)
	if service.cancelCalls != 0 {
		t.Fatalf("Cancel() calls = %d, want 0", service.cancelCalls)
	}
}

func TestProcessingJobCancelHandlerMapsServiceErrors(t *testing.T) {
	tests := []struct {
		name       string
		err        error
		statusCode int
		code       string
		message    string
	}{
		{name: "invalid ID", err: applicationdocument.ErrInvalidProcessingJobID, statusCode: http.StatusBadRequest, code: errorCodeInvalidProcessingJobID, message: "processing job ID must be a positive integer"},
		{name: "not found", err: documentdomain.ErrProcessingJobNotFound, statusCode: http.StatusNotFound, code: errorCodeProcessingJobNotFound, message: "processing job not found"},
		{name: "processing", err: documentdomain.ErrProcessingJobProcessingCannotCancel, statusCode: http.StatusConflict, code: errorCodeProcessingJobProcessing, message: "processing document job cannot be canceled"},
		{name: "terminal", err: documentdomain.ErrProcessingJobTerminalCannotCancel, statusCode: http.StatusConflict, code: errorCodeProcessingJobTerminal, message: "completed document processing job cannot be canceled"},
		{name: "internal", err: errors.New("database unavailable"), statusCode: http.StatusInternalServerError, code: errorCodeInternal, message: "internal server error"},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			service := &fakeProcessingJobCancelService{
				cancelFunc: func(context.Context, accessdomain.OwnerScope, int64) (documentdomain.ProcessingJob, error) {
					return documentdomain.ProcessingJob{}, test.err
				},
			}
			response := httptest.NewRecorder()
			newTestProcessingJobCancelRouter(service).ServeHTTP(
				response,
				httptest.NewRequest(http.MethodPost, "/processing-jobs/19/cancel", nil),
			)

			assertCodedErrorResponse(
				t,
				response,
				test.statusCode,
				test.message,
				test.code,
			)
		})
	}
}

func TestProcessingJobCancelHandlerReturnsCanceledJob(t *testing.T) {
	expectedJob := documentdomain.ProcessingJob{
		ID:         19,
		DocumentID: 7,
		Status:     documentdomain.ProcessingJobStatusCanceled,
	}
	service := &fakeProcessingJobCancelService{
		cancelFunc: func(
			_ context.Context,
			scope accessdomain.OwnerScope,
			jobID int64,
		) (documentdomain.ProcessingJob, error) {
			if scope.OwnerUserID() != testAPIOwnerUserID || jobID != expectedJob.ID {
				t.Fatalf("Cancel() received owner=%d job=%d", scope.OwnerUserID(), jobID)
			}
			return expectedJob, nil
		},
	}
	response := httptest.NewRecorder()
	newTestProcessingJobCancelRouter(service).ServeHTTP(
		response,
		httptest.NewRequest(http.MethodPost, "/processing-jobs/19/cancel", nil),
	)

	if response.Code != http.StatusOK {
		t.Fatalf("status = %d, want %d", response.Code, http.StatusOK)
	}
	var actual processingJobResponse
	if err := json.Unmarshal(response.Body.Bytes(), &actual); err != nil {
		t.Fatalf("decode response: %v", err)
	}
	if actual != newProcessingJobResponse(expectedJob) {
		t.Fatalf("response = %+v, want %+v", actual, newProcessingJobResponse(expectedJob))
	}
	if actual.Cancelable {
		t.Fatal("canceled job must not remain cancelable")
	}
}
