package api

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"reflect"
	"strings"
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
	getByIDFunc    func(context.Context, accessdomain.OwnerScope, int64) (embeddingdomain.Job, error)
	getLatestFunc  func(context.Context, accessdomain.OwnerScope, []int64) (embeddingapplication.LatestJobsOutput, error)
	getByIDCalls   int
	getLatestCalls int
}

func (f *fakeEmbeddingJobQueryService) GetLatestByDocumentIDs(
	ctx context.Context,
	scope accessdomain.OwnerScope,
	documentIDs []int64,
) (embeddingapplication.LatestJobsOutput, error) {
	f.getLatestCalls++
	return f.getLatestFunc(ctx, scope, documentIDs)
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

func TestEmbeddingJobHandlerReturnsLatestJobsByDocument(t *testing.T) {
	job := embeddingdomain.Job{
		ID:         52,
		DocumentID: 7,
		ModelName:  "text-embedding-v4",
		Dimensions: 1024,
		Status:     embeddingdomain.JobStatusSucceeded,
	}
	service := &fakeEmbeddingJobQueryService{
		getLatestFunc: func(
			_ context.Context,
			scope accessdomain.OwnerScope,
			documentIDs []int64,
		) (embeddingapplication.LatestJobsOutput, error) {
			if scope.OwnerUserID() != testAPIOwnerUserID {
				t.Fatalf("owner = %d, want %d", scope.OwnerUserID(), testAPIOwnerUserID)
			}
			if len(documentIDs) != 2 || documentIDs[0] != 7 || documentIDs[1] != 9 {
				t.Fatalf("document IDs = %v, want [7 9]", documentIDs)
			}
			return embeddingapplication.LatestJobsOutput{Items: []embeddingapplication.LatestJobItem{
				{DocumentID: 7, Job: &job},
				{DocumentID: 9},
			}}, nil
		},
	}
	request := httptest.NewRequest(
		http.MethodPost,
		"/embedding-jobs/latest",
		strings.NewReader(`{"document_ids":[7,9]}`),
	)
	request.Header.Set("Content-Type", "application/json")
	response := httptest.NewRecorder()
	newTestEmbeddingJobRouter(service).ServeHTTP(response, request)

	if response.Code != http.StatusOK {
		t.Fatalf("status=%d want=200 body=%s", response.Code, response.Body.String())
	}
	var actual embeddingJobsLatestResponse
	if err := json.Unmarshal(response.Body.Bytes(), &actual); err != nil {
		t.Fatalf("decode response: %v", err)
	}
	if len(actual.Items) != 2 || actual.Items[0].Job == nil || actual.Items[0].Job.ID != job.ID {
		t.Fatalf("items = %+v, want first job", actual.Items)
	}
	if actual.Items[1].DocumentID != 9 || actual.Items[1].Job != nil {
		t.Fatalf("second item = %+v, want document 9 null job", actual.Items[1])
	}
}

func TestEmbeddingJobHandlerLatestLookupRequiresAuthentication(t *testing.T) {
	service := &fakeEmbeddingJobQueryService{
		getLatestFunc: func(context.Context, accessdomain.OwnerScope, []int64) (embeddingapplication.LatestJobsOutput, error) {
			t.Fatal("GetLatestByDocumentIDs must not be called without authentication")
			return embeddingapplication.LatestJobsOutput{}, nil
		},
	}
	gin.SetMode(gin.TestMode)
	router := gin.New()
	NewEmbeddingJobHandler(service).RegisterRoutes(router)
	request := httptest.NewRequest(http.MethodPost, "/embedding-jobs/latest", strings.NewReader(`{"document_ids":[7]}`))
	request.Header.Set("Content-Type", "application/json")
	response := httptest.NewRecorder()
	router.ServeHTTP(response, request)

	if response.Code != http.StatusUnauthorized {
		t.Fatalf("status=%d want=401 body=%s", response.Code, response.Body.String())
	}
	if service.getLatestCalls != 0 {
		t.Fatalf("latest calls=%d want=0", service.getLatestCalls)
	}
}

func TestEmbeddingJobHandlerMapsLatestLookupErrors(t *testing.T) {
	tests := []struct {
		name       string
		body       string
		serviceErr error
		wantBody   string
	}{
		{name: "malformed JSON", body: `{`, wantBody: `{"error":"request body must contain a valid document_ids array","code":"invalid_embedding_job_lookup"}`},
		{name: "empty", body: `{"document_ids":[]}`, serviceErr: embeddingapplication.ErrEmptyEmbeddingJobLookup, wantBody: `{"error":"document_ids must contain at least one document ID","code":"invalid_embedding_job_lookup"}`},
		{name: "too large", body: `{"document_ids":[1]}`, serviceErr: embeddingapplication.ErrEmbeddingJobLookupTooLarge, wantBody: `{"error":"document_ids must contain at most 100 document IDs","code":"invalid_embedding_job_lookup"}`},
		{name: "invalid ID", body: `{"document_ids":[0]}`, serviceErr: embeddingapplication.ErrInvalidDocumentID, wantBody: `{"error":"every document ID must be a positive integer","code":"invalid_embedding_job_lookup"}`},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			service := &fakeEmbeddingJobQueryService{
				getLatestFunc: func(context.Context, accessdomain.OwnerScope, []int64) (embeddingapplication.LatestJobsOutput, error) {
					return embeddingapplication.LatestJobsOutput{}, test.serviceErr
				},
			}
			request := httptest.NewRequest(http.MethodPost, "/embedding-jobs/latest", strings.NewReader(test.body))
			request.Header.Set("Content-Type", "application/json")
			response := httptest.NewRecorder()
			newTestEmbeddingJobRouter(service).ServeHTTP(response, request)

			if response.Code != http.StatusBadRequest || response.Body.String() != test.wantBody {
				t.Fatalf("response = %d %s, want 400 %s", response.Code, response.Body.String(), test.wantBody)
			}
			if test.name == "malformed JSON" && service.getLatestCalls != 0 {
				t.Fatalf("latest calls = %d, want 0", service.getLatestCalls)
			}
		})
	}
}
