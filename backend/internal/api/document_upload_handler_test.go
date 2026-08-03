package api

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"mime/multipart"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/gin-gonic/gin"

	applicationdocument "rag-reasoning-platform/backend/internal/application/document"
	documentdomain "rag-reasoning-platform/backend/internal/domain/document"
)

// fakeDocumentUploadService 是上传 Handler 测试使用的假应用服务。
type fakeDocumentUploadService struct {
	uploadFunc  func(context.Context, applicationdocument.UploadInput) (documentdomain.Document, error)
	uploadCalls int
}

func (f *fakeDocumentUploadService) Upload(
	ctx context.Context,
	input applicationdocument.UploadInput,
) (documentdomain.Document, error) {
	f.uploadCalls++

	return f.uploadFunc(ctx, input)
}

// newTestDocumentUploadRouter 创建只注册上传接口的测试路由。
func newTestDocumentUploadRouter(
	service documentUploadService,
) *gin.Engine {
	gin.SetMode(gin.TestMode)

	router := gin.New()
	handler := NewDocumentUploadHandler(service, 1024)
	handler.RegisterRoutes(router)

	return router
}

// TestDocumentUploadHandlerRejectsMissingFile 验证 multipart 请求中
// 没有 file 字段时返回 400，且不会调用应用服务。
func TestDocumentUploadHandlerRejectsMissingFile(t *testing.T) {
	service := &fakeDocumentUploadService{
		uploadFunc: func(
			context.Context,
			applicationdocument.UploadInput,
		) (documentdomain.Document, error) {
			t.Fatal("Upload must not be called when file field is missing")

			return documentdomain.Document{}, nil
		},
	}

	var requestBody bytes.Buffer
	multipartWriter := multipart.NewWriter(&requestBody)
	if err := multipartWriter.Close(); err != nil {
		t.Fatalf("close multipart writer: %v", err)
	}

	request := httptest.NewRequest(
		http.MethodPost,
		"/documents",
		&requestBody,
	)
	request.Header.Set(
		"Content-Type",
		multipartWriter.FormDataContentType(),
	)

	response := httptest.NewRecorder()
	newTestDocumentUploadRouter(service).ServeHTTP(response, request)

	if response.Code != http.StatusBadRequest {
		t.Fatalf(
			"expected status %d, got %d",
			http.StatusBadRequest,
			response.Code,
		)
	}

	expectedBody := `{"error":"file field is required"}`
	if response.Body.String() != expectedBody {
		t.Fatalf(
			"expected body %s, got %s",
			expectedBody,
			response.Body.String(),
		)
	}

	if service.uploadCalls != 0 {
		t.Fatalf(
			"expected no Upload calls, got %d",
			service.uploadCalls,
		)
	}
}

// TestDocumentUploadHandlerReturnsCreatedDocument 验证成功上传会把文件流
// 交给应用服务，并返回 201 和创建后的文档信息。
func TestDocumentUploadHandlerReturnsCreatedDocument(t *testing.T) {
	const originalName = "example.pdf"
	fileContent := []byte("%PDF-1.7\ntest document")
	expectedDocument := documentdomain.Document{
		ID:           21,
		OriginalName: originalName,
		StoragePath:  "documents/internal-file.pdf",
		MIMEType:     "application/pdf",
		SizeBytes:    int64(len(fileContent)),
		SHA256:       "test-sha256",
		Status:       documentdomain.StatusUploaded,
	}

	service := &fakeDocumentUploadService{
		uploadFunc: func(
			_ context.Context,
			input applicationdocument.UploadInput,
		) (documentdomain.Document, error) {
			if input.OriginalName != originalName {
				t.Fatalf(
					"expected name %q, got %q",
					originalName,
					input.OriginalName,
				)
			}

			receivedContent, err := io.ReadAll(input.Content)
			if err != nil {
				t.Fatalf("read uploaded content: %v", err)
			}

			if !bytes.Equal(receivedContent, fileContent) {
				t.Fatalf(
					"expected content %q, got %q",
					string(fileContent),
					string(receivedContent),
				)
			}

			return expectedDocument, nil
		},
	}

	var requestBody bytes.Buffer
	multipartWriter := multipart.NewWriter(&requestBody)
	fileWriter, err := multipartWriter.CreateFormFile(
		"file",
		originalName,
	)
	if err != nil {
		t.Fatalf("create multipart file field: %v", err)
	}

	if _, err := fileWriter.Write(fileContent); err != nil {
		t.Fatalf("write multipart file content: %v", err)
	}

	if err := multipartWriter.Close(); err != nil {
		t.Fatalf("close multipart writer: %v", err)
	}

	request := httptest.NewRequest(
		http.MethodPost,
		"/documents",
		&requestBody,
	)
	request.Header.Set(
		"Content-Type",
		multipartWriter.FormDataContentType(),
	)

	response := httptest.NewRecorder()
	newTestDocumentUploadRouter(service).ServeHTTP(response, request)

	if response.Code != http.StatusCreated {
		t.Fatalf(
			"expected status %d, got %d: %s",
			http.StatusCreated,
			response.Code,
			response.Body.String(),
		)
	}

	var responseBody documentResponse
	if err := json.Unmarshal(response.Body.Bytes(), &responseBody); err != nil {
		t.Fatalf("decode response JSON: %v", err)
	}

	if responseBody.ID != expectedDocument.ID ||
		responseBody.OriginalName != expectedDocument.OriginalName ||
		responseBody.Status != expectedDocument.Status {
		t.Fatalf(
			"unexpected response body: %+v",
			responseBody,
		)
	}

	if service.uploadCalls != 1 {
		t.Fatalf(
			"expected one Upload call, got %d",
			service.uploadCalls,
		)
	}
}

// TestDocumentUploadHandlerMapsApplicationErrors 验证应用层错误会被转换为
// 稳定的 HTTP 状态码和对外错误信息。
func TestDocumentUploadHandlerMapsApplicationErrors(t *testing.T) {
	tests := []struct {
		name           string
		serviceError   error
		expectedStatus int
		expectedBody   string
	}{
		{
			name:           "original name required",
			serviceError:   applicationdocument.ErrOriginalNameRequired,
			expectedStatus: http.StatusBadRequest,
			expectedBody:   `{"error":"file name is required"}`,
		},
		{
			name:           "file content required",
			serviceError:   applicationdocument.ErrFileContentRequired,
			expectedStatus: http.StatusBadRequest,
			expectedBody:   `{"error":"file content is required"}`,
		},
		{
			name:           "invalid PDF content",
			serviceError:   applicationdocument.ErrInvalidPDFContent,
			expectedStatus: http.StatusUnsupportedMediaType,
			expectedBody:   `{"error":"file content must be a PDF"}`,
		},
		{
			name:           "unsupported file type",
			serviceError:   applicationdocument.ErrUnsupportedFileType,
			expectedStatus: http.StatusUnsupportedMediaType,
			expectedBody:   `{"error":"file type must be PDF, Markdown, or plain text"}`,
		},
		{
			name:           "invalid UTF-8 text content",
			serviceError:   applicationdocument.ErrInvalidTextContent,
			expectedStatus: http.StatusUnsupportedMediaType,
			expectedBody:   `{"error":"text file content must be valid UTF-8"}`,
		},
		{
			name:           "file too large",
			serviceError:   applicationdocument.ErrFileTooLarge,
			expectedStatus: http.StatusRequestEntityTooLarge,
			expectedBody:   `{"error":"file exceeds maximum allowed size"}`,
		},
		{
			name:           "unknown internal error",
			serviceError:   errors.New("unexpected failure"),
			expectedStatus: http.StatusInternalServerError,
			expectedBody:   `{"error":"internal server error"}`,
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			service := &fakeDocumentUploadService{
				uploadFunc: func(
					context.Context,
					applicationdocument.UploadInput,
				) (documentdomain.Document, error) {
					return documentdomain.Document{}, fmt.Errorf(
						"upload document: %w",
						test.serviceError,
					)
				},
			}

			var requestBody bytes.Buffer
			multipartWriter := multipart.NewWriter(&requestBody)
			fileWriter, err := multipartWriter.CreateFormFile(
				"file",
				"example.pdf",
			)
			if err != nil {
				t.Fatalf("create multipart file field: %v", err)
			}

			if _, err := fileWriter.Write([]byte("%PDF-1.7")); err != nil {
				t.Fatalf("write multipart file content: %v", err)
			}

			if err := multipartWriter.Close(); err != nil {
				t.Fatalf("close multipart writer: %v", err)
			}

			request := httptest.NewRequest(
				http.MethodPost,
				"/documents",
				&requestBody,
			)
			request.Header.Set(
				"Content-Type",
				multipartWriter.FormDataContentType(),
			)

			response := httptest.NewRecorder()
			newTestDocumentUploadRouter(service).ServeHTTP(
				response,
				request,
			)

			if response.Code != test.expectedStatus {
				t.Fatalf(
					"expected status %d, got %d",
					test.expectedStatus,
					response.Code,
				)
			}

			if response.Body.String() != test.expectedBody {
				t.Fatalf(
					"expected body %s, got %s",
					test.expectedBody,
					response.Body.String(),
				)
			}
		})
	}
}

// TestDocumentUploadHandlerRejectsOversizedRequestBody 验证 HTTP 层会限制
// 整个 multipart 请求体，而不只是依赖文件存储层限制文件内容大小。
func TestDocumentUploadHandlerRejectsOversizedRequestBody(t *testing.T) {
	const maxFileSizeBytes int64 = 1

	service := &fakeDocumentUploadService{
		uploadFunc: func(
			_ context.Context,
			input applicationdocument.UploadInput,
		) (documentdomain.Document, error) {
			// 必须持续读取 input.Content，MaxBytesReader 才会在越过上限时
			// 返回 *http.MaxBytesError。仅仅收到 io.Reader 并不会触发限制。
			if _, err := io.Copy(io.Discard, input.Content); err != nil {
				return documentdomain.Document{}, fmt.Errorf(
					"read uploaded content: %w",
					err,
				)
			}

			t.Fatal("expected reading oversized request body to fail")
			return documentdomain.Document{}, nil
		},
	}

	var requestBody bytes.Buffer
	multipartWriter := multipart.NewWriter(&requestBody)
	fileWriter, err := multipartWriter.CreateFormFile("file", "large.pdf")
	if err != nil {
		t.Fatalf("create multipart file field: %v", err)
	}

	// Handler 会为 multipart 元数据额外预留 1 MiB。这里让文件内容本身
	// 超过“文件上限 + 预留空间”，确保整个 HTTP 请求体一定越界。
	oversizedContent := bytes.Repeat(
		[]byte("a"),
		int(maxFileSizeBytes+multipartOverheadAllowanceBytes+1),
	)
	if _, err := fileWriter.Write(oversizedContent); err != nil {
		t.Fatalf("write oversized multipart content: %v", err)
	}

	if err := multipartWriter.Close(); err != nil {
		t.Fatalf("close multipart writer: %v", err)
	}

	request := httptest.NewRequest(
		http.MethodPost,
		"/documents",
		&requestBody,
	)
	request.Header.Set(
		"Content-Type",
		multipartWriter.FormDataContentType(),
	)

	response := httptest.NewRecorder()
	router := gin.New()
	handler := NewDocumentUploadHandler(service, maxFileSizeBytes)
	handler.RegisterRoutes(router)
	router.ServeHTTP(response, request)

	if response.Code != http.StatusRequestEntityTooLarge {
		t.Fatalf(
			"expected status %d, got %d: %s",
			http.StatusRequestEntityTooLarge,
			response.Code,
			response.Body.String(),
		)
	}

	expectedBody := `{"error":"request body exceeds maximum allowed size"}`
	if response.Body.String() != expectedBody {
		t.Fatalf(
			"expected body %s, got %s",
			expectedBody,
			response.Body.String(),
		)
	}
}
