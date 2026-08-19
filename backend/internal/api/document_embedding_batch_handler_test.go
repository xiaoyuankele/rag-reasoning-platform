package api

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/gin-gonic/gin"

	embeddingapplication "rag-reasoning-platform/backend/internal/application/embedding"
	accessdomain "rag-reasoning-platform/backend/internal/domain/access"
	documentdomain "rag-reasoning-platform/backend/internal/domain/document"
	embeddingdomain "rag-reasoning-platform/backend/internal/domain/embedding"
)

type fakeEmbeddingBatchQueueService struct {
	queueBatchFunc  func(context.Context, accessdomain.OwnerScope, []int64) (embeddingapplication.BatchQueueOutput, error)
	queueBatchCalls int
}

func (f *fakeEmbeddingBatchQueueService) QueueBatch(
	ctx context.Context,
	scope accessdomain.OwnerScope,
	documentIDs []int64,
) (embeddingapplication.BatchQueueOutput, error) {
	f.queueBatchCalls++
	return f.queueBatchFunc(ctx, scope, documentIDs)
}

func newTestEmbeddingBatchRouter(service embeddingBatchQueueService) *gin.Engine {
	gin.SetMode(gin.TestMode)
	router := gin.New()
	useTestAuthenticatedIdentity(router)
	logger := slog.New(slog.NewTextHandler(io.Discard, nil))
	NewDocumentEmbeddingBatchHandler(service, logger).RegisterRoutes(router)
	return router
}

func TestDocumentEmbeddingBatchHandlerRejectsInvalidRequest(t *testing.T) {
	testCases := []struct {
		name        string
		body        string
		serviceErr  error
		wantMessage string
		wantCalls   int
	}{
		{name: "invalid JSON", body: `{`, wantMessage: "request body must contain a valid document_ids array", wantCalls: 0},
		{name: "empty", body: `{"document_ids":[]}`, serviceErr: embeddingapplication.ErrEmptyEmbeddingBatch, wantMessage: "document_ids must contain at least one document ID", wantCalls: 1},
		{name: "invalid ID", body: `{"document_ids":[0]}`, serviceErr: embeddingapplication.ErrInvalidDocumentID, wantMessage: "every document ID must be a positive integer", wantCalls: 1},
		{name: "too many", body: `{"document_ids":[1]}`, serviceErr: embeddingapplication.ErrEmbeddingBatchTooLarge, wantMessage: "document_ids must contain at most 100 document IDs", wantCalls: 1},
	}

	for _, testCase := range testCases {
		t.Run(testCase.name, func(t *testing.T) {
			service := &fakeEmbeddingBatchQueueService{
				queueBatchFunc: func(context.Context, accessdomain.OwnerScope, []int64) (embeddingapplication.BatchQueueOutput, error) {
					return embeddingapplication.BatchQueueOutput{}, testCase.serviceErr
				},
			}
			response := httptest.NewRecorder()
			newTestEmbeddingBatchRouter(service).ServeHTTP(
				response,
				httptest.NewRequest(http.MethodPost, "/embedding-jobs/batch", strings.NewReader(testCase.body)),
			)

			assertEmbeddingCancelError(t, response, http.StatusBadRequest, errorCodeInvalidEmbeddingBatch, testCase.wantMessage)
			if service.queueBatchCalls != testCase.wantCalls {
				t.Fatalf("QueueBatch() calls = %d, want %d", service.queueBatchCalls, testCase.wantCalls)
			}
		})
	}
}

func TestDocumentEmbeddingBatchHandlerReturnsPerDocumentResults(t *testing.T) {
	internalErr := errors.New("database unavailable")
	service := &fakeEmbeddingBatchQueueService{
		queueBatchFunc: func(
			_ context.Context,
			scope accessdomain.OwnerScope,
			documentIDs []int64,
		) (embeddingapplication.BatchQueueOutput, error) {
			if scope.OwnerUserID() != testAPIOwnerUserID {
				t.Fatalf("owner = %d, want %d", scope.OwnerUserID(), testAPIOwnerUserID)
			}
			if len(documentIDs) != 4 {
				t.Fatalf("document IDs = %v, want four IDs", documentIDs)
			}
			return embeddingapplication.BatchQueueOutput{Items: []embeddingapplication.BatchQueueItem{
				{DocumentID: 1, Result: embeddingdomain.JobRequestResult{Job: embeddingdomain.Job{ID: 11, DocumentID: 1, Status: embeddingdomain.JobStatusQueued}, Created: true}},
				{DocumentID: 2, Result: embeddingdomain.JobRequestResult{Job: embeddingdomain.Job{ID: 12, DocumentID: 2, Status: embeddingdomain.JobStatusWaitingDocument}, Created: false}},
				{DocumentID: 3, Err: documentdomain.ErrNotFound},
				{DocumentID: 4, Err: internalErr},
			}}, nil
		},
	}
	response := httptest.NewRecorder()
	newTestEmbeddingBatchRouter(service).ServeHTTP(
		response,
		httptest.NewRequest(http.MethodPost, "/embedding-jobs/batch", strings.NewReader(`{"document_ids":[1,2,3,4]}`)),
	)

	if response.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200; body = %s", response.Code, response.Body.String())
	}
	var actual embeddingBatchResponse
	if err := json.Unmarshal(response.Body.Bytes(), &actual); err != nil {
		t.Fatalf("decode batch response: %v", err)
	}
	if len(actual.Items) != 4 {
		t.Fatalf("items = %d, want 4", len(actual.Items))
	}
	if actual.Items[0].Outcome != batchEmbeddingOutcomeCreated || actual.Items[0].Job == nil {
		t.Fatalf("created item = %+v", actual.Items[0])
	}
	if actual.Items[1].Outcome != batchEmbeddingOutcomeAlreadyActive || actual.Items[1].Job == nil {
		t.Fatalf("existing item = %+v", actual.Items[1])
	}
	if actual.Items[2].Outcome != batchEmbeddingOutcomeNotFound || actual.Items[2].Error == nil || actual.Items[2].Error.Code != errorCodeDocumentNotFound {
		t.Fatalf("not-found item = %+v", actual.Items[2])
	}
	if actual.Items[3].Outcome != batchEmbeddingOutcomeFailed || actual.Items[3].Error == nil || actual.Items[3].Error.Code != errorCodeInternal {
		t.Fatalf("failed item = %+v", actual.Items[3])
	}
}
