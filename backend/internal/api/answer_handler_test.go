package api

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/gin-gonic/gin"

	answerapplication "rag-reasoning-platform/backend/internal/application/answer"
	embeddingapplication "rag-reasoning-platform/backend/internal/application/embedding"
	accessdomain "rag-reasoning-platform/backend/internal/domain/access"
	documentdomain "rag-reasoning-platform/backend/internal/domain/document"
	embeddingdomain "rag-reasoning-platform/backend/internal/domain/embedding"
	generationdomain "rag-reasoning-platform/backend/internal/domain/generation"
)

// fakeAnswerService 只记录 Handler 交给 Application 的输入，并返回测试预设结果。
// 它不检索数据库，也不调用 Embedding 或 Generation 远程 API。
type fakeAnswerService struct {
	output        answerapplication.Output
	err           error
	receivedInput answerapplication.Input
	receivedScope accessdomain.OwnerScope
	callCount     int
}

func (f *fakeAnswerService) Answer(
	_ context.Context,
	scope accessdomain.OwnerScope,
	input answerapplication.Input,
) (answerapplication.Output, error) {
	f.callCount++
	f.receivedScope = scope
	f.receivedInput = input
	return f.output, f.err
}

func newAnswerTestRouter(service answerService) *gin.Engine {
	gin.SetMode(gin.TestMode)
	router := gin.New()
	useTestAuthenticatedIdentity(router)
	handler := NewAnswerHandler(service)
	handler.RegisterRoutes(router)
	return router
}

// TestAnswerHandlerReturnsAnswerAndSources 验证请求 DTO、Application 输入、
// 来源 DTO 和 Token 用量之间的完整成功转换。
func TestAnswerHandlerReturnsAnswerAndSources(t *testing.T) {
	title := "磁悬浮系统稳定性研究"
	pageStart := 3
	pageEnd := 4
	documentID := int64(20)
	service := &fakeAnswerService{
		output: answerapplication.Output{
			Query:  "磁悬浮振动如何抑制？",
			Answer: "可使用反馈控制抑制振动。[1]",
			Sources: []answerapplication.Source{
				{
					Citation:     1,
					ChunkID:      101,
					DocumentID:   documentID,
					ChunkIndex:   2,
					Title:        &title,
					OriginalName: "maglev.pdf",
					PageStart:    &pageStart,
					PageEnd:      &pageEnd,
					Similarity:   0.93,
				},
			},
			PromptTokens:     120,
			CompletionTokens: 30,
			TotalTokens:      150,
			ResponseLanguage: answerapplication.ResponseLanguageEnglish,
		},
	}

	recorder := performAnswerRequest(
		t,
		service,
		`{"query":"  磁悬浮振动如何抑制？  ","document_id":20,"top_k":3,"response_language":"en"}`,
	)

	if service.receivedInput.ResponseLanguage != answerapplication.ResponseLanguageEnglish {
		t.Fatalf(
			"response language = %q, want %q",
			service.receivedInput.ResponseLanguage,
			answerapplication.ResponseLanguageEnglish,
		)
	}

	if recorder.Code != http.StatusOK {
		t.Fatalf(
			"status = %d, want %d; body = %s",
			recorder.Code,
			http.StatusOK,
			recorder.Body.String(),
		)
	}
	if service.callCount != 1 {
		t.Fatalf("service call count = %d, want 1", service.callCount)
	}
	if service.receivedScope.OwnerUserID() != testAPIOwnerUserID {
		t.Fatalf(
			"scope owner = %d, want %d",
			service.receivedScope.OwnerUserID(),
			testAPIOwnerUserID,
		)
	}
	if service.receivedInput.Query != "  磁悬浮振动如何抑制？  " {
		t.Fatalf("query = %q, want raw request query", service.receivedInput.Query)
	}
	if service.receivedInput.DocumentID == nil ||
		*service.receivedInput.DocumentID != documentID {
		t.Fatalf("document ID = %v, want %d", service.receivedInput.DocumentID, documentID)
	}
	if service.receivedInput.TopK != 3 {
		t.Fatalf("top_k = %d, want 3", service.receivedInput.TopK)
	}

	var response answerResponse
	if err := json.Unmarshal(recorder.Body.Bytes(), &response); err != nil {
		t.Fatalf("decode response: %v", err)
	}
	if response.ResponseLanguage != "en" {
		t.Fatalf(
			"response language = %q, want en",
			response.ResponseLanguage,
		)
	}
	if response.Query != "磁悬浮振动如何抑制？" ||
		response.Answer != "可使用反馈控制抑制振动。[1]" {
		t.Fatalf("unexpected answer response: %+v", response)
	}
	if len(response.Sources) != 1 ||
		response.Sources[0].Citation != 1 ||
		response.Sources[0].ChunkID != 101 ||
		response.Sources[0].Title == nil ||
		*response.Sources[0].Title != title {
		t.Fatalf("unexpected sources: %+v", response.Sources)
	}
	if response.Usage.PromptTokens != 120 ||
		response.Usage.CompletionTokens != 30 ||
		response.Usage.TotalTokens != 150 {
		t.Fatalf("unexpected usage: %+v", response.Usage)
	}
}

// TestAnswerHandlerRequiresAuthenticatedIdentity 验证无 Session 身份时，
// 不进入可能调用 Embedding 和 Generation 的问答 Application。
func TestAnswerHandlerRequiresAuthenticatedIdentity(t *testing.T) {
	service := &fakeAnswerService{}
	gin.SetMode(gin.TestMode)
	router := gin.New()
	NewAnswerHandler(service).RegisterRoutes(router)
	request := httptest.NewRequest(
		http.MethodPost,
		"/answers",
		strings.NewReader(`{"query":"control"}`),
	)
	request.Header.Set("Content-Type", "application/json")
	response := httptest.NewRecorder()
	router.ServeHTTP(response, request)

	if response.Code != http.StatusUnauthorized || service.callCount != 0 {
		t.Fatalf(
			"status=%d calls=%d want 401/0 body=%s",
			response.Code,
			service.callCount,
			response.Body.String(),
		)
	}
}

// TestAnswerHandlerUsesDefaultTopKAndEmptySources 验证省略 top_k 时使用默认值，
// 同时确保无证据的 sources 编码为 [] 而不是 null。
func TestAnswerHandlerUsesDefaultTopKAndEmptySources(t *testing.T) {
	service := &fakeAnswerService{
		output: answerapplication.Output{
			Query:   "unknown question",
			Answer:  answerapplication.InsufficientEvidenceAnswer,
			Sources: nil,
		},
	}
	recorder := performAnswerRequest(
		t,
		service,
		`{"query":"unknown question"}`,
	)

	if recorder.Code != http.StatusOK {
		t.Fatalf("status = %d, want %d", recorder.Code, http.StatusOK)
	}
	if service.receivedInput.TopK != defaultAnswerTopK {
		t.Fatalf(
			"top_k = %d, want default %d",
			service.receivedInput.TopK,
			defaultAnswerTopK,
		)
	}
	if !strings.Contains(recorder.Body.String(), `"sources":[]`) {
		t.Fatalf("body = %s, want non-nil empty sources array", recorder.Body.String())
	}
}

func TestAnswerHandlerPreservesExplicitZeroTopK(t *testing.T) {
	service := &fakeAnswerService{
		err: embeddingapplication.ErrInvalidSemanticSearchTopK,
	}
	recorder := performAnswerRequest(
		t,
		service,
		`{"query":"control","top_k":0}`,
	)

	assertAnswerErrorResponse(
		t,
		recorder,
		http.StatusBadRequest,
		"top_k must be between 1 and 20",
	)
	if service.receivedInput.TopK != 0 {
		t.Fatalf("top_k = %d, want explicit 0", service.receivedInput.TopK)
	}
}

func TestAnswerHandlerRejectsInvalidJSON(t *testing.T) {
	service := &fakeAnswerService{}
	recorder := performAnswerRequest(
		t,
		service,
		`{"query":"control","top_k":"five"}`,
	)

	assertAnswerErrorResponse(
		t,
		recorder,
		http.StatusBadRequest,
		"request body must be valid JSON",
	)
	if service.callCount != 0 {
		t.Fatalf("service call count = %d, want 0", service.callCount)
	}
}

// TestAnswerHandlerMapsServiceErrors 使用表驱动测试覆盖参数、Embedding、
// Generation 和未知内部错误的 HTTP 映射。
func TestAnswerHandlerMapsServiceErrors(t *testing.T) {
	tests := []struct {
		name       string
		err        error
		wantStatus int
		wantError  string
	}{
		{name: "document ID", err: embeddingapplication.ErrInvalidDocumentID, wantStatus: http.StatusBadRequest, wantError: "document_id must be a positive integer"},
		{name: "query required", err: embeddingapplication.ErrSemanticSearchQueryRequired, wantStatus: http.StatusBadRequest, wantError: "query is required"},
		{name: "query UTF-8", err: embeddingapplication.ErrSemanticSearchQueryInvalidUTF8, wantStatus: http.StatusBadRequest, wantError: "query must be valid UTF-8"},
		{name: "query too long", err: embeddingapplication.ErrSemanticSearchQueryTooLong, wantStatus: http.StatusBadRequest, wantError: "query must not exceed 1000 characters"},
		{name: "top k", err: embeddingapplication.ErrInvalidSemanticSearchTopK, wantStatus: http.StatusBadRequest, wantError: "top_k must be between 1 and 20"},
		{name: "document not found", err: documentdomain.ErrNotFound, wantStatus: http.StatusNotFound, wantError: "document not found"},
		{name: "document embeddings not ready", err: embeddingapplication.ErrDocumentEmbeddingsNotReady, wantStatus: http.StatusConflict, wantError: "document embeddings are not ready"},
		{name: "response language", err: answerapplication.ErrInvalidResponseLanguage, wantStatus: http.StatusBadRequest, wantError: "response_language must be auto, zh, or en"},
		{name: "embedding rejected", err: embeddingdomain.ErrEmbeddingRequestRejected, wantStatus: http.StatusBadGateway, wantError: "embedding provider returned an invalid response"},
		{name: "invalid embedding response", err: embeddingdomain.ErrInvalidEmbeddingResponse, wantStatus: http.StatusBadGateway, wantError: "embedding provider returned an invalid response"},
		{name: "generation rejected", err: generationdomain.ErrGenerationRequestRejected, wantStatus: http.StatusBadGateway, wantError: "generation provider returned an invalid response"},
		{name: "invalid generation response", err: generationdomain.ErrInvalidGenerationResponse, wantStatus: http.StatusBadGateway, wantError: "generation provider returned an invalid response"},
		{name: "embedding authentication", err: embeddingdomain.ErrEmbeddingAuthentication, wantStatus: http.StatusServiceUnavailable, wantError: "answer service is temporarily unavailable"},
		{name: "embedding rate limit", err: embeddingdomain.ErrEmbeddingRateLimited, wantStatus: http.StatusServiceUnavailable, wantError: "answer service is temporarily unavailable"},
		{name: "embedding quota", err: embeddingdomain.ErrEmbeddingQuotaExceeded, wantStatus: http.StatusServiceUnavailable, wantError: "answer service is temporarily unavailable"},
		{name: "embedding unavailable", err: embeddingdomain.ErrEmbeddingUnavailable, wantStatus: http.StatusServiceUnavailable, wantError: "answer service is temporarily unavailable"},
		{name: "generation authentication", err: generationdomain.ErrGenerationAuthentication, wantStatus: http.StatusServiceUnavailable, wantError: "answer service is temporarily unavailable"},
		{name: "generation rate limit", err: generationdomain.ErrGenerationRateLimited, wantStatus: http.StatusServiceUnavailable, wantError: "answer service is temporarily unavailable"},
		{name: "generation quota", err: generationdomain.ErrGenerationQuotaExceeded, wantStatus: http.StatusServiceUnavailable, wantError: "answer service is temporarily unavailable"},
		{name: "generation unavailable", err: generationdomain.ErrGenerationUnavailable, wantStatus: http.StatusServiceUnavailable, wantError: "answer service is temporarily unavailable"},
		{name: "database", err: errors.New("database unavailable"), wantStatus: http.StatusInternalServerError, wantError: "internal server error"},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			service := &fakeAnswerService{
				err: errors.Join(errors.New("answer failed"), test.err),
			}
			recorder := performAnswerRequest(
				t,
				service,
				`{"query":"control","top_k":5}`,
			)

			assertAnswerErrorResponse(
				t,
				recorder,
				test.wantStatus,
				test.wantError,
			)
		})
	}
}

func performAnswerRequest(
	t *testing.T,
	service answerService,
	body string,
) *httptest.ResponseRecorder {
	t.Helper()
	recorder := httptest.NewRecorder()
	request := httptest.NewRequest(
		http.MethodPost,
		"/answers",
		strings.NewReader(body),
	)
	request.Header.Set("Content-Type", "application/json")
	newAnswerTestRouter(service).ServeHTTP(recorder, request)
	return recorder
}

func assertAnswerErrorResponse(
	t *testing.T,
	recorder *httptest.ResponseRecorder,
	wantStatus int,
	wantError string,
) {
	t.Helper()
	if recorder.Code != wantStatus {
		t.Fatalf(
			"status = %d, want %d; body = %s",
			recorder.Code,
			wantStatus,
			recorder.Body.String(),
		)
	}

	var response errorResponse
	if err := json.Unmarshal(recorder.Body.Bytes(), &response); err != nil {
		t.Fatalf("decode error response: %v", err)
	}
	if response.Error != wantError {
		t.Fatalf("error = %q, want %q", response.Error, wantError)
	}
}
