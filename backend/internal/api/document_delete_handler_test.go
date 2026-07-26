package api

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/gin-gonic/gin"

	documentdomain "rag-reasoning-platform/backend/internal/domain/document"
)

type fakeDocumentDeleteService struct {
	deleteFunc  func(context.Context, int64) error
	deleteCalls int
}

func (f *fakeDocumentDeleteService) Delete(
	ctx context.Context,
	id int64,
) error {
	f.deleteCalls++

	return f.deleteFunc(ctx, id)
}

func newTestDocumentDeleteRouter(
	service documentDeleteService,
) *gin.Engine {
	gin.SetMode(gin.TestMode)

	router := gin.New()
	handler := NewDocumentDeleteHandler(service)
	handler.RegisterRoutes(router)

	return router
}

func TestDocumentDeleteHandlerRejectsInvalidID(t *testing.T) {
	testCases := []struct {
		name string
		path string
	}{
		{
			name: "non-numeric ID",
			path: "/documents/abc",
		},
		{
			name: "zero ID",
			path: "/documents/0",
		},
		{
			name: "negative ID",
			path: "/documents/-1",
		},
	}

	for _, testCase := range testCases {
		t.Run(testCase.name, func(t *testing.T) {
			service := &fakeDocumentDeleteService{
				deleteFunc: func(context.Context, int64) error {
					t.Fatal("Delete() must not be called for an invalid path ID")
					return nil
				},
			}
			router := newTestDocumentDeleteRouter(service)
			request := httptest.NewRequest(
				http.MethodDelete,
				testCase.path,
				nil,
			)
			response := httptest.NewRecorder()

			router.ServeHTTP(response, request)

			if response.Code != http.StatusBadRequest {
				t.Fatalf(
					"status = %d, want %d",
					response.Code,
					http.StatusBadRequest,
				)
			}

			expectedBody := `{"error":"document ID must be a positive integer"}`
			if response.Body.String() != expectedBody {
				t.Fatalf(
					"body = %s, want %s",
					response.Body.String(),
					expectedBody,
				)
			}

			if service.deleteCalls != 0 {
				t.Fatalf(
					"Delete() calls = %d, want 0",
					service.deleteCalls,
				)
			}
		})
	}
}

func TestDocumentDeleteHandlerReturnsNotFound(t *testing.T) {
	service := &fakeDocumentDeleteService{
		deleteFunc: func(_ context.Context, id int64) error {
			if id != 999 {
				t.Fatalf("Delete() id = %d, want 999", id)
			}

			return documentdomain.ErrNotFound
		},
	}
	router := newTestDocumentDeleteRouter(service)
	request := httptest.NewRequest(
		http.MethodDelete,
		"/documents/999",
		nil,
	)
	response := httptest.NewRecorder()

	router.ServeHTTP(response, request)

	if response.Code != http.StatusNotFound {
		t.Fatalf(
			"status = %d, want %d",
			response.Code,
			http.StatusNotFound,
		)
	}

	expectedBody := `{"error":"document not found"}`
	if response.Body.String() != expectedBody {
		t.Fatalf(
			"body = %s, want %s",
			response.Body.String(),
			expectedBody,
		)
	}

	if service.deleteCalls != 1 {
		t.Fatalf(
			"Delete() calls = %d, want 1",
			service.deleteCalls,
		)
	}
}

func TestDocumentDeleteHandlerReturnsInternalServerError(t *testing.T) {
	service := &fakeDocumentDeleteService{
		deleteFunc: func(context.Context, int64) error {
			return errors.New("file system unavailable")
		},
	}
	router := newTestDocumentDeleteRouter(service)
	request := httptest.NewRequest(
		http.MethodDelete,
		"/documents/42",
		nil,
	)
	response := httptest.NewRecorder()

	router.ServeHTTP(response, request)

	if response.Code != http.StatusInternalServerError {
		t.Fatalf(
			"status = %d, want %d",
			response.Code,
			http.StatusInternalServerError,
		)
	}

	expectedBody := `{"error":"internal server error"}`
	if response.Body.String() != expectedBody {
		t.Fatalf(
			"body = %s, want %s",
			response.Body.String(),
			expectedBody,
		)
	}

	if service.deleteCalls != 1 {
		t.Fatalf(
			"Delete() calls = %d, want 1",
			service.deleteCalls,
		)
	}
}

func TestDocumentDeleteHandlerReturnsNoContent(t *testing.T) {
	const expectedID int64 = 42

	service := &fakeDocumentDeleteService{
		deleteFunc: func(_ context.Context, id int64) error {
			if id != expectedID {
				t.Fatalf(
					"Delete() id = %d, want %d",
					id,
					expectedID,
				)
			}

			return nil
		},
	}
	router := newTestDocumentDeleteRouter(service)
	request := httptest.NewRequest(
		http.MethodDelete,
		"/documents/42",
		nil,
	)
	response := httptest.NewRecorder()

	router.ServeHTTP(response, request)

	if response.Code != http.StatusNoContent {
		t.Fatalf(
			"status = %d, want %d",
			response.Code,
			http.StatusNoContent,
		)
	}

	if response.Body.Len() != 0 {
		t.Fatalf(
			"body = %q, want empty body",
			response.Body.String(),
		)
	}

	if service.deleteCalls != 1 {
		t.Fatalf(
			"Delete() calls = %d, want 1",
			service.deleteCalls,
		)
	}
}
