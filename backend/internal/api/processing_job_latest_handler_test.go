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
	"testing"

	"github.com/gin-gonic/gin"

	applicationdocument "rag-reasoning-platform/backend/internal/application/document"
	accessdomain "rag-reasoning-platform/backend/internal/domain/access"
	documentdomain "rag-reasoning-platform/backend/internal/domain/document"
)

type fakeProcessingJobLatestService struct {
	getFunc  func(context.Context, accessdomain.OwnerScope, []int64) (applicationdocument.LatestProcessingJobsOutput, error)
	getCalls int
}

func (f *fakeProcessingJobLatestService) GetLatestByDocumentIDs(
	ctx context.Context,
	scope accessdomain.OwnerScope,
	documentIDs []int64,
) (applicationdocument.LatestProcessingJobsOutput, error) {
	f.getCalls++
	return f.getFunc(ctx, scope, documentIDs)
}

func newTestProcessingJobLatestRouter(
	service processingJobLatestService,
) *gin.Engine {
	gin.SetMode(gin.TestMode)
	router := gin.New()
	router.Use(RequestIDMiddleware())
	useTestAuthenticatedIdentity(router)
	logger := slog.New(slog.NewTextHandler(io.Discard, nil))
	NewProcessingJobLatestHandler(service, logger).RegisterRoutes(router)
	return router
}

func TestProcessingJobLatestHandlerRejectsInvalidRequests(t *testing.T) {
	tests := []struct {
		name string
		body string
		err  error
		msg  string
	}{
		{name: "malformed JSON", body: `{`, msg: "request body must contain a valid document_ids array"},
		{name: "empty", body: `{"document_ids":[]}`, err: applicationdocument.ErrEmptyProcessingJobLookup, msg: "document_ids must contain at least one document ID"},
		{name: "too large", body: `{"document_ids":[1]}`, err: applicationdocument.ErrProcessingJobLookupTooLarge, msg: "document_ids must contain at most 100 document IDs"},
		{name: "invalid ID", body: `{"document_ids":[0]}`, err: applicationdocument.ErrInvalidProcessingJobDocumentID, msg: "every document ID must be a positive integer"},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			service := &fakeProcessingJobLatestService{
				getFunc: func(context.Context, accessdomain.OwnerScope, []int64) (applicationdocument.LatestProcessingJobsOutput, error) {
					return applicationdocument.LatestProcessingJobsOutput{}, test.err
				},
			}
			response := httptest.NewRecorder()
			newTestProcessingJobLatestRouter(service).ServeHTTP(
				response,
				httptest.NewRequest(http.MethodPost, "/processing-jobs/latest", bytes.NewBufferString(test.body)),
			)

			assertCodedErrorResponse(
				t,
				response,
				http.StatusBadRequest,
				test.msg,
				errorCodeInvalidProcessingJobLookup,
			)
			if test.name == "malformed JSON" && service.getCalls != 0 {
				t.Fatalf("service calls = %d, want 0", service.getCalls)
			}
		})
	}
}

func TestProcessingJobLatestHandlerReturnsItems(t *testing.T) {
	queuedJob := documentdomain.ProcessingJob{
		ID: 19, DocumentID: 7, Status: documentdomain.ProcessingJobStatusQueued,
	}
	service := &fakeProcessingJobLatestService{
		getFunc: func(
			_ context.Context,
			scope accessdomain.OwnerScope,
			documentIDs []int64,
		) (applicationdocument.LatestProcessingJobsOutput, error) {
			if scope.OwnerUserID() != testAPIOwnerUserID {
				t.Fatalf("owner = %d, want %d", scope.OwnerUserID(), testAPIOwnerUserID)
			}
			if len(documentIDs) != 2 || documentIDs[0] != 7 || documentIDs[1] != 9 {
				t.Fatalf("document IDs = %v, want [7 9]", documentIDs)
			}
			return applicationdocument.LatestProcessingJobsOutput{
				Items: []applicationdocument.LatestProcessingJobItem{
					{DocumentID: 7, Job: &queuedJob},
					{DocumentID: 9, Job: nil},
				},
			}, nil
		},
	}
	response := httptest.NewRecorder()
	newTestProcessingJobLatestRouter(service).ServeHTTP(
		response,
		httptest.NewRequest(http.MethodPost, "/processing-jobs/latest", bytes.NewBufferString(`{"document_ids":[7,9]}`)),
	)

	if response.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200; body=%s", response.Code, response.Body.String())
	}
	var actual processingJobsLatestResponse
	if err := json.Unmarshal(response.Body.Bytes(), &actual); err != nil {
		t.Fatalf("decode response: %v", err)
	}
	if len(actual.Items) != 2 || actual.Items[0].Job == nil || !actual.Items[0].Job.Cancelable {
		t.Fatalf("items = %+v, want queued cancelable job followed by nil job", actual.Items)
	}
	if actual.Items[1].DocumentID != 9 || actual.Items[1].Job != nil {
		t.Fatalf("item 1 = %+v, want document 9 nil job", actual.Items[1])
	}
}

func TestProcessingJobLatestHandlerMapsInternalError(t *testing.T) {
	service := &fakeProcessingJobLatestService{
		getFunc: func(context.Context, accessdomain.OwnerScope, []int64) (applicationdocument.LatestProcessingJobsOutput, error) {
			return applicationdocument.LatestProcessingJobsOutput{}, errors.New("database unavailable")
		},
	}
	response := httptest.NewRecorder()
	newTestProcessingJobLatestRouter(service).ServeHTTP(
		response,
		httptest.NewRequest(http.MethodPost, "/processing-jobs/latest", bytes.NewBufferString(`{"document_ids":[7]}`)),
	)
	assertCodedErrorResponse(t, response, http.StatusInternalServerError, "internal server error", errorCodeInternal)
}
