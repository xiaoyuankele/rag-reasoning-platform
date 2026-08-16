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
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/gin-gonic/gin"

	applicationdocument "rag-reasoning-platform/backend/internal/application/document"
	accessdomain "rag-reasoning-platform/backend/internal/domain/access"
	documentdomain "rag-reasoning-platform/backend/internal/domain/document"
)

type fakeProcessingJobQueryService struct {
	getByIDFunc  func(context.Context, accessdomain.OwnerScope, int64) (documentdomain.ProcessingJob, error)
	getByIDCalls int
}

func (f *fakeProcessingJobQueryService) GetByID(
	ctx context.Context,
	scope accessdomain.OwnerScope,
	jobID int64,
) (documentdomain.ProcessingJob, error) {
	f.getByIDCalls++
	return f.getByIDFunc(ctx, scope, jobID)
}

func newTestProcessingJobRouter(
	service processingJobQueryService,
) *gin.Engine {
	return newTestProcessingJobRouterWithLogger(
		service,
		slog.New(slog.NewTextHandler(io.Discard, nil)),
	)
}

// newTestProcessingJobRouterWithLogger 允许测试注入可观察的日志输出，
// 用于验证 500 错误的内部诊断信息不会泄漏到 HTTP 响应中。
func newTestProcessingJobRouterWithLogger(
	service processingJobQueryService,
	logger *slog.Logger,
) *gin.Engine {
	gin.SetMode(gin.TestMode)

	router := gin.New()
	router.Use(RequestIDMiddleware())
	useTestAuthenticatedIdentity(router)
	handler := NewProcessingJobHandler(service, logger)
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
					accessdomain.OwnerScope,
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

			assertCodedErrorResponse(
				t,
				response,
				http.StatusBadRequest,
				"processing job ID must be a positive integer",
				errorCodeInvalidProcessingJobID,
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
		name            string
		serviceErr      error
		statusCode      int
		message         string
		errorCode       string
		wantInternalLog bool
		diagnosticCode  string
	}{
		{
			name:       "invalid ID",
			serviceErr: applicationdocument.ErrInvalidProcessingJobID,
			statusCode: http.StatusBadRequest,
			message:    "processing job ID must be a positive integer",
			errorCode:  errorCodeInvalidProcessingJobID,
		},
		{
			name:       "job not found",
			serviceErr: documentdomain.ErrProcessingJobNotFound,
			statusCode: http.StatusNotFound,
			message:    "processing job not found",
			errorCode:  errorCodeProcessingJobNotFound,
		},
		{
			name:            "unknown internal error",
			serviceErr:      errors.New("database unavailable"),
			statusCode:      http.StatusInternalServerError,
			message:         "internal server error",
			errorCode:       errorCodeInternal,
			wantInternalLog: true,
			diagnosticCode:  "processing_job_get_failed",
		},
	}

	for _, testCase := range testCases {
		t.Run(testCase.name, func(t *testing.T) {
			service := &fakeProcessingJobQueryService{
				getByIDFunc: func(
					context.Context,
					accessdomain.OwnerScope,
					int64,
				) (documentdomain.ProcessingJob, error) {
					return documentdomain.ProcessingJob{},
						testCase.serviceErr
				},
			}
			var logOutput bytes.Buffer
			logger := slog.New(slog.NewJSONHandler(&logOutput, nil))
			router := newTestProcessingJobRouterWithLogger(service, logger)
			request := httptest.NewRequest(
				http.MethodGet,
				"/processing-jobs/17",
				nil,
			)
			request.Header.Set(RequestIDHeader, "processing-job-error-17")
			response := httptest.NewRecorder()

			router.ServeHTTP(response, request)

			assertCodedErrorResponse(
				t,
				response,
				testCase.statusCode,
				testCase.message,
				testCase.errorCode,
			)
			if service.getByIDCalls != 1 {
				t.Fatalf(
					"GetByID() calls = %d, want 1",
					service.getByIDCalls,
				)
			}

			if !testCase.wantInternalLog {
				if logOutput.Len() != 0 {
					t.Fatalf(
						"expected no internal error log, got %s",
						logOutput.String(),
					)
				}
				return
			}

			var logEntry map[string]any
			if err := json.Unmarshal(
				bytes.TrimSpace(logOutput.Bytes()),
				&logEntry,
			); err != nil {
				t.Fatalf(
					"decode internal error log: %v; output = %q",
					err,
					logOutput.String(),
				)
			}
			assertLogField(t, logEntry, "event", "http_request_failed")
			assertLogField(t, logEntry, "request_id", "processing-job-error-17")
			assertLogField(t, logEntry, "public_error_code", errorCodeInternal)
			assertLogField(t, logEntry, "diagnostic_code", testCase.diagnosticCode)
			if !strings.Contains(
				logEntry["error"].(string),
				testCase.serviceErr.Error(),
			) {
				t.Fatalf(
					"internal log error = %#v, want original error",
					logEntry["error"],
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
			scope accessdomain.OwnerScope,
			jobID int64,
		) (documentdomain.ProcessingJob, error) {
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

// assertCodedErrorResponse 验证新错误契约同时包含人类可读消息和稳定错误码。
func assertCodedErrorResponse(
	t *testing.T,
	response *httptest.ResponseRecorder,
	expectedStatus int,
	expectedMessage string,
	expectedCode string,
) {
	t.Helper()

	if response.Code != expectedStatus {
		t.Fatalf(
			"status = %d, want %d",
			response.Code,
			expectedStatus,
		)
	}

	expectedBody := `{"error":"` + expectedMessage +
		`","code":"` + expectedCode + `"}`
	if response.Body.String() != expectedBody {
		t.Fatalf(
			"body = %s, want %s",
			response.Body.String(),
			expectedBody,
		)
	}
}
