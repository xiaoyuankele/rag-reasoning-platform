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
	accessdomain "rag-reasoning-platform/backend/internal/domain/access"
	documentdomain "rag-reasoning-platform/backend/internal/domain/document"
)

// fakeDocumentUploadService 是上传 Handler 测试使用的假应用服务。
type fakeDocumentUploadService struct {
	uploadFunc  func(context.Context, accessdomain.OwnerScope, applicationdocument.UploadInput) (applicationdocument.UploadResult, error)
	uploadCalls int
}

func (f *fakeDocumentUploadService) Upload(
	ctx context.Context,
	scope accessdomain.OwnerScope,
	input applicationdocument.UploadInput,
) (applicationdocument.UploadResult, error) {
	f.uploadCalls++

	return f.uploadFunc(ctx, scope, input)
}

// newTestDocumentUploadRouter 创建只注册上传接口的测试路由。
func newTestDocumentUploadRouter(
	service documentUploadService,
) *gin.Engine {
	gin.SetMode(gin.TestMode)

	router := gin.New()
	useTestAuthenticatedIdentity(router)
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
			accessdomain.OwnerScope,
			applicationdocument.UploadInput,
		) (applicationdocument.UploadResult, error) {
			t.Fatal("Upload must not be called when file field is missing")

			return applicationdocument.UploadResult{}, nil
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
			scope accessdomain.OwnerScope,
			input applicationdocument.UploadInput,
		) (applicationdocument.UploadResult, error) {
			if scope.OwnerUserID() != testAPIOwnerUserID {
				t.Fatalf("Upload() scope owner = %d, want %d", scope.OwnerUserID(), testAPIOwnerUserID)
			}
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

			return applicationdocument.UploadResult{
				Document: expectedDocument,
			}, nil
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

	var responseBody documentUploadResponse
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
	if responseBody.Duplicate {
		t.Fatal("expected duplicate=false for newly created document")
	}

	if service.uploadCalls != 1 {
		t.Fatalf(
			"expected one Upload call, got %d",
			service.uploadCalls,
		)
	}
}

// TestDocumentUploadHandlerReturnsExistingDuplicate 验证相同内容不是请求错误：
// Handler 返回已有文档、200 OK 和 duplicate=true，供前端显示明确提示。
func TestDocumentUploadHandlerReturnsExistingDuplicate(t *testing.T) {
	existingDocument := documentdomain.Document{
		ID:           22,
		OriginalName: "first-upload.pdf",
		MIMEType:     "application/pdf",
		SizeBytes:    16,
		SHA256:       "existing-sha256",
		Status:       documentdomain.StatusReady,
	}
	service := &fakeDocumentUploadService{
		uploadFunc: func(
			context.Context,
			accessdomain.OwnerScope,
			applicationdocument.UploadInput,
		) (applicationdocument.UploadResult, error) {
			return applicationdocument.UploadResult{
				Document:  existingDocument,
				Duplicate: true,
			}, nil
		},
	}

	var requestBody bytes.Buffer
	multipartWriter := multipart.NewWriter(&requestBody)
	fileWriter, err := multipartWriter.CreateFormFile("file", "renamed-copy.pdf")
	if err != nil {
		t.Fatalf("create multipart file field: %v", err)
	}
	if _, err := fileWriter.Write([]byte("%PDF-1.7\ncopy")); err != nil {
		t.Fatalf("write multipart file content: %v", err)
	}
	if err := multipartWriter.Close(); err != nil {
		t.Fatalf("close multipart writer: %v", err)
	}

	request := httptest.NewRequest(http.MethodPost, "/documents", &requestBody)
	request.Header.Set("Content-Type", multipartWriter.FormDataContentType())
	response := httptest.NewRecorder()
	newTestDocumentUploadRouter(service).ServeHTTP(response, request)

	if response.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200; body=%s", response.Code, response.Body.String())
	}
	var responseBody documentUploadResponse
	if err := json.Unmarshal(response.Body.Bytes(), &responseBody); err != nil {
		t.Fatalf("decode response JSON: %v", err)
	}
	if !responseBody.Duplicate || responseBody.ID != existingDocument.ID {
		t.Fatalf("response = %+v, want existing document with duplicate=true", responseBody)
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
					accessdomain.OwnerScope,
					applicationdocument.UploadInput,
				) (applicationdocument.UploadResult, error) {
					return applicationdocument.UploadResult{}, fmt.Errorf(
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

func TestDocumentUploadHandlerMapsAdmissionErrors(t *testing.T) {
	testCases := []struct {
		name       string
		serviceErr error
		statusCode int
		code       string
		message    string
	}{
		{
			name:       "owner capacity",
			serviceErr: applicationdocument.ErrUploadOwnerCapacityExhausted,
			statusCode: http.StatusTooManyRequests,
			code:       errorCodeUploadOwnerCapacity,
			message:    "another upload is already in progress for this user",
		},
		{
			name:       "global capacity",
			serviceErr: applicationdocument.ErrUploadGlobalCapacityExhausted,
			statusCode: http.StatusServiceUnavailable,
			code:       errorCodeUploadGlobalCapacity,
			message:    "upload service is busy; try again later",
		},
	}

	for _, testCase := range testCases {
		t.Run(testCase.name, func(t *testing.T) {
			service := &fakeDocumentUploadService{
				uploadFunc: func(
					context.Context,
					accessdomain.OwnerScope,
					applicationdocument.UploadInput,
				) (applicationdocument.UploadResult, error) {
					return applicationdocument.UploadResult{}, testCase.serviceErr
				},
			}

			var requestBody bytes.Buffer
			multipartWriter := multipart.NewWriter(&requestBody)
			fileWriter, err := multipartWriter.CreateFormFile(
				"file",
				"capacity-test.pdf",
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
			newTestDocumentUploadRouter(service).ServeHTTP(response, request)

			if response.Code != testCase.statusCode {
				t.Fatalf(
					"status = %d, want %d; body=%s",
					response.Code,
					testCase.statusCode,
					response.Body.String(),
				)
			}
			if retryAfter := response.Header().Get("Retry-After"); retryAfter != "2" {
				t.Fatalf("Retry-After = %q, want 2", retryAfter)
			}

			var actual errorResponse
			if err := json.Unmarshal(response.Body.Bytes(), &actual); err != nil {
				t.Fatalf("decode error response: %v", err)
			}
			if actual.Code != testCase.code || actual.Error != testCase.message {
				t.Fatalf(
					"error response = %+v, want code=%q message=%q",
					actual,
					testCase.code,
					testCase.message,
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
			_ accessdomain.OwnerScope,
			input applicationdocument.UploadInput,
		) (applicationdocument.UploadResult, error) {
			// 必须持续读取 input.Content，MaxBytesReader 才会在越过上限时
			// 返回 *http.MaxBytesError。仅仅收到 io.Reader 并不会触发限制。
			if _, err := io.Copy(io.Discard, input.Content); err != nil {
				return applicationdocument.UploadResult{}, fmt.Errorf(
					"read uploaded content: %w",
					err,
				)
			}

			t.Fatal("expected reading oversized request body to fail")
			return applicationdocument.UploadResult{}, nil
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
	useTestAuthenticatedIdentity(router)
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
