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
	accessdomain "rag-reasoning-platform/backend/internal/domain/access"
	documentdomain "rag-reasoning-platform/backend/internal/domain/document"
)

type fakeDocumentChunkListService struct {
	listFunc  func(context.Context, accessdomain.OwnerScope, applicationdocument.ChunkListInput) (applicationdocument.ChunkListOutput, error)
	listCalls int
}

func (f *fakeDocumentChunkListService) List(
	ctx context.Context,
	scope accessdomain.OwnerScope,
	input applicationdocument.ChunkListInput,
) (applicationdocument.ChunkListOutput, error) {
	f.listCalls++
	return f.listFunc(ctx, scope, input)
}

func newTestDocumentChunkRouter(
	service documentChunkListService,
) *gin.Engine {
	gin.SetMode(gin.TestMode)
	router := gin.New()
	useTestAuthenticatedIdentity(router)
	handler := NewDocumentChunkHandler(service)
	handler.RegisterRoutes(router)
	return router
}

func TestDocumentChunkHandlerRejectsInvalidHTTPParameters(t *testing.T) {
	testCases := []struct {
		name    string
		path    string
		message string
	}{
		{name: "non-numeric ID", path: "/documents/abc/chunks", message: "document ID must be a positive integer"},
		{name: "zero ID", path: "/documents/0/chunks", message: "document ID must be a positive integer"},
		{name: "invalid page", path: "/documents/7/chunks?page=0", message: "page must be a positive integer"},
		{name: "invalid page size", path: "/documents/7/chunks?page_size=abc", message: "page_size must be a positive integer"},
	}

	for _, testCase := range testCases {
		t.Run(testCase.name, func(t *testing.T) {
			service := &fakeDocumentChunkListService{
				listFunc: func(
					context.Context,
					accessdomain.OwnerScope,
					applicationdocument.ChunkListInput,
				) (applicationdocument.ChunkListOutput, error) {
					t.Fatal("List() must not be called for invalid HTTP input")
					return applicationdocument.ChunkListOutput{}, nil
				},
			}
			router := newTestDocumentChunkRouter(service)
			request := httptest.NewRequest(http.MethodGet, testCase.path, nil)
			response := httptest.NewRecorder()

			router.ServeHTTP(response, request)

			assertErrorResponse(
				t,
				response,
				http.StatusBadRequest,
				testCase.message,
			)
			if service.listCalls != 0 {
				t.Fatalf("List() calls = %d, want 0", service.listCalls)
			}
		})
	}
}

func TestDocumentChunkHandlerMapsApplicationErrors(t *testing.T) {
	testCases := []struct {
		name       string
		serviceErr error
		statusCode int
		message    string
	}{
		{name: "invalid ID", serviceErr: applicationdocument.ErrInvalidID, statusCode: http.StatusBadRequest, message: "document ID must be a positive integer"},
		{name: "invalid page", serviceErr: applicationdocument.ErrInvalidPage, statusCode: http.StatusBadRequest, message: "page must be a positive integer"},
		{name: "invalid page size", serviceErr: applicationdocument.ErrInvalidPageSize, statusCode: http.StatusBadRequest, message: "page_size must be between 1 and 100"},
		{name: "document missing", serviceErr: documentdomain.ErrNotFound, statusCode: http.StatusNotFound, message: "document not found"},
		{name: "chunks not ready", serviceErr: applicationdocument.ErrDocumentChunksNotReady, statusCode: http.StatusConflict, message: "document chunks are not ready"},
		{name: "internal error", serviceErr: errors.New("database unavailable"), statusCode: http.StatusInternalServerError, message: "internal server error"},
	}

	for _, testCase := range testCases {
		t.Run(testCase.name, func(t *testing.T) {
			service := &fakeDocumentChunkListService{
				listFunc: func(
					context.Context,
					accessdomain.OwnerScope,
					applicationdocument.ChunkListInput,
				) (applicationdocument.ChunkListOutput, error) {
					return applicationdocument.ChunkListOutput{}, testCase.serviceErr
				},
			}
			router := newTestDocumentChunkRouter(service)
			request := httptest.NewRequest(
				http.MethodGet,
				"/documents/7/chunks",
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
			if service.listCalls != 1 {
				t.Fatalf("List() calls = %d, want 1", service.listCalls)
			}
		})
	}
}

func TestDocumentChunkHandlerReturnsPage(t *testing.T) {
	pageStart := 3
	pageEnd := 4
	createdAt := time.Date(2026, time.August, 14, 10, 0, 0, 0, time.UTC)
	expectedOutput := applicationdocument.ChunkListOutput{
		DocumentID: 7,
		Chunks: []documentdomain.TextChunk{
			{
				ID:         12,
				DocumentID: 7,
				Index:      2,
				Content:    "third chunk",
				PageStart:  &pageStart,
				PageEnd:    &pageEnd,
				CreatedAt:  createdAt,
			},
		},
		Page:       2,
		PageSize:   2,
		Total:      5,
		TotalPages: 3,
	}
	service := &fakeDocumentChunkListService{
		listFunc: func(
			_ context.Context,
			scope accessdomain.OwnerScope,
			input applicationdocument.ChunkListInput,
		) (applicationdocument.ChunkListOutput, error) {
			if scope.OwnerUserID() != testAPIOwnerUserID {
				t.Fatalf("List() scope owner = %d, want %d", scope.OwnerUserID(), testAPIOwnerUserID)
			}
			expectedInput := applicationdocument.ChunkListInput{
				DocumentID: 7,
				Page:       2,
				PageSize:   2,
			}
			if input != expectedInput {
				t.Fatalf("List() input = %+v, want %+v", input, expectedInput)
			}
			return expectedOutput, nil
		},
	}
	router := newTestDocumentChunkRouter(service)
	request := httptest.NewRequest(
		http.MethodGet,
		"/documents/7/chunks?page=2&page_size=2",
		nil,
	)
	response := httptest.NewRecorder()

	router.ServeHTTP(response, request)

	if response.Code != http.StatusOK {
		t.Fatalf("status = %d, want %d", response.Code, http.StatusOK)
	}

	var actual documentChunkListResponse
	if err := json.Unmarshal(response.Body.Bytes(), &actual); err != nil {
		t.Fatalf("decode response JSON: %v", err)
	}
	want := documentChunkListResponse{
		DocumentID: expectedOutput.DocumentID,
		Chunks: []documentChunkResponse{
			newDocumentChunkResponse(expectedOutput.Chunks[0]),
		},
		Pagination: paginationResponse{
			Page:       2,
			PageSize:   2,
			Total:      5,
			TotalPages: 3,
		},
	}
	if !reflect.DeepEqual(actual, want) {
		t.Fatalf("response = %+v, want %+v", actual, want)
	}
}

func TestDocumentChunkHandlerReturnsEmptyArray(t *testing.T) {
	service := &fakeDocumentChunkListService{
		listFunc: func(
			context.Context,
			accessdomain.OwnerScope,
			applicationdocument.ChunkListInput,
		) (applicationdocument.ChunkListOutput, error) {
			return applicationdocument.ChunkListOutput{
				DocumentID: 7,
				Page:       1,
				PageSize:   20,
			}, nil
		},
	}
	router := newTestDocumentChunkRouter(service)
	request := httptest.NewRequest(
		http.MethodGet,
		"/documents/7/chunks",
		nil,
	)
	response := httptest.NewRecorder()

	router.ServeHTTP(response, request)

	var actual documentChunkListResponse
	if err := json.Unmarshal(response.Body.Bytes(), &actual); err != nil {
		t.Fatalf("decode response JSON: %v", err)
	}
	if actual.Chunks == nil || len(actual.Chunks) != 0 {
		t.Fatalf("chunks = %#v, want non-nil empty array", actual.Chunks)
	}
}
