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
	"time"

	"github.com/gin-gonic/gin"

	answerapplication "rag-reasoning-platform/backend/internal/application/answer"
	accessdomain "rag-reasoning-platform/backend/internal/domain/access"
)

// fakeAnswerJobService 是 Handler 测试使用的可控插头。
// 三个函数字段分别模拟 Application 的创建、查询和取消用例。
type fakeAnswerJobService struct {
	queueFunc  func(context.Context, accessdomain.OwnerScope, answerapplication.Input) (answerapplication.Job, error)
	getFunc    func(context.Context, accessdomain.OwnerScope, int64) (answerapplication.Job, error)
	cancelFunc func(context.Context, accessdomain.OwnerScope, int64) (answerapplication.Job, error)
}

func (f *fakeAnswerJobService) Queue(
	ctx context.Context,
	scope accessdomain.OwnerScope,
	input answerapplication.Input,
) (answerapplication.Job, error) {
	return f.queueFunc(ctx, scope, input)
}

func (f *fakeAnswerJobService) GetByID(
	ctx context.Context,
	scope accessdomain.OwnerScope,
	jobID int64,
) (answerapplication.Job, error) {
	return f.getFunc(ctx, scope, jobID)
}

func (f *fakeAnswerJobService) Cancel(
	ctx context.Context,
	scope accessdomain.OwnerScope,
	jobID int64,
) (answerapplication.Job, error) {
	return f.cancelFunc(ctx, scope, jobID)
}

func newAnswerJobTestRouter(service answerJobService) *gin.Engine {
	gin.SetMode(gin.TestMode)
	router := gin.New()
	useTestAuthenticatedIdentity(router)
	logger := slog.New(slog.NewTextHandler(io.Discard, nil))
	NewAnswerJobHandler(service, logger).RegisterRoutes(router)
	return router
}

func TestAnswerJobHandlerQueuesJob(t *testing.T) {
	createdAt := time.Date(2026, 8, 24, 10, 0, 0, 0, time.UTC)
	service := &fakeAnswerJobService{
		queueFunc: func(
			_ context.Context,
			scope accessdomain.OwnerScope,
			input answerapplication.Input,
		) (answerapplication.Job, error) {
			if scope.OwnerUserID() != testAPIOwnerUserID {
				t.Fatalf("owner = %d, want %d", scope.OwnerUserID(), testAPIOwnerUserID)
			}
			if input.Query != "control stability" || input.TopK != defaultAnswerTopK {
				t.Fatalf("input = %+v, want query and default top_k", input)
			}
			return answerapplication.Job{
				ID:                        51,
				OwnerUserID:               testAPIOwnerUserID,
				Query:                     input.Query,
				TopK:                      input.TopK,
				RequestedResponseLanguage: answerapplication.ResponseLanguageAuto,
				Status:                    answerapplication.JobStatusQueued,
				NextAttemptAt:             createdAt,
				CreatedAt:                 createdAt,
				UpdatedAt:                 createdAt,
			}, nil
		},
	}

	request := httptest.NewRequest(
		http.MethodPost,
		"/answer-jobs",
		strings.NewReader(`{"query":"control stability"}`),
	)
	request.Header.Set("Content-Type", "application/json")
	response := httptest.NewRecorder()
	newAnswerJobTestRouter(service).ServeHTTP(response, request)

	if response.Code != http.StatusAccepted {
		t.Fatalf("status = %d, want 202; body=%s", response.Code, response.Body.String())
	}
	var body answerJobResponse
	if err := json.Unmarshal(response.Body.Bytes(), &body); err != nil {
		t.Fatalf("decode response: %v", err)
	}
	if body.ID != 51 || body.Status != answerapplication.JobStatusQueued || !body.Cancelable {
		t.Fatalf("response = %+v, want queued and cancelable", body)
	}
	if strings.Contains(response.Body.String(), "owner_user_id") {
		t.Fatalf("response leaks owner identity: %s", response.Body.String())
	}
}

func TestAnswerJobHandlerReturnsCompletedResult(t *testing.T) {
	title := "Control paper"
	page := 4
	completedAt := time.Now().UTC()
	service := &fakeAnswerJobService{
		getFunc: func(
			_ context.Context,
			scope accessdomain.OwnerScope,
			jobID int64,
		) (answerapplication.Job, error) {
			if scope.OwnerUserID() != testAPIOwnerUserID || jobID != 52 {
				t.Fatalf("scope/job = %d/%d, want %d/52", scope.OwnerUserID(), jobID, testAPIOwnerUserID)
			}
			return answerapplication.Job{
				ID:                        52,
				OwnerUserID:               testAPIOwnerUserID,
				Query:                     "control",
				TopK:                      5,
				RequestedResponseLanguage: answerapplication.ResponseLanguageEnglish,
				Status:                    answerapplication.JobStatusSucceeded,
				NextAttemptAt:             completedAt,
				CreatedAt:                 completedAt,
				UpdatedAt:                 completedAt,
				CompletedAt:               &completedAt,
				Result: &answerapplication.JobResult{
					Answer:           "Stable control.[1]",
					ResponseLanguage: answerapplication.ResponseLanguageEnglish,
					Sources: []answerapplication.Source{{
						Citation:     1,
						ChunkID:      100,
						DocumentID:   20,
						ChunkIndex:   3,
						Title:        &title,
						OriginalName: "control.pdf",
						PageStart:    &page,
						PageEnd:      &page,
						Similarity:   0.91,
					}},
					PromptTokens:     10,
					CompletionTokens: 4,
					TotalTokens:      14,
				},
			}, nil
		},
	}

	request := httptest.NewRequest(http.MethodGet, "/answer-jobs/52", nil)
	response := httptest.NewRecorder()
	newAnswerJobTestRouter(service).ServeHTTP(response, request)

	if response.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200; body=%s", response.Code, response.Body.String())
	}
	var body answerJobResponse
	if err := json.Unmarshal(response.Body.Bytes(), &body); err != nil {
		t.Fatalf("decode response: %v", err)
	}
	if body.Cancelable || body.Result == nil || body.Result.Answer != "Stable control.[1]" ||
		len(body.Result.Sources) != 1 || body.Result.Usage.TotalTokens != 14 {
		t.Fatalf("response = %+v, want completed result", body)
	}
}

func TestAnswerJobHandlerMapsQueueCapacity(t *testing.T) {
	tests := []struct {
		name       string
		err        error
		wantStatus int
		wantCode   string
	}{
		{name: "owner", err: answerapplication.ErrAnswerOwnerQueueCapacity, wantStatus: http.StatusTooManyRequests, wantCode: errorCodeAnswerJobOwnerQueueCapacity},
		{name: "global", err: answerapplication.ErrAnswerGlobalQueueCapacity, wantStatus: http.StatusServiceUnavailable, wantCode: errorCodeAnswerJobGlobalQueueCapacity},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			service := &fakeAnswerJobService{
				queueFunc: func(context.Context, accessdomain.OwnerScope, answerapplication.Input) (answerapplication.Job, error) {
					return answerapplication.Job{}, errors.Join(errors.New("queue failed"), test.err)
				},
			}
			request := httptest.NewRequest(http.MethodPost, "/answer-jobs", strings.NewReader(`{"query":"control"}`))
			request.Header.Set("Content-Type", "application/json")
			response := httptest.NewRecorder()
			newAnswerJobTestRouter(service).ServeHTTP(response, request)

			if response.Code != test.wantStatus || response.Header().Get("Retry-After") != "5" {
				t.Fatalf("status/retry = %d/%q, want %d/5", response.Code, response.Header().Get("Retry-After"), test.wantStatus)
			}
			var body errorResponse
			if err := json.Unmarshal(response.Body.Bytes(), &body); err != nil {
				t.Fatalf("decode response: %v", err)
			}
			if body.Code != test.wantCode {
				t.Fatalf("code = %q, want %q", body.Code, test.wantCode)
			}
		})
	}
}

func TestAnswerJobHandlerMapsCancelConflicts(t *testing.T) {
	tests := []struct {
		name     string
		err      error
		wantCode string
	}{
		{name: "processing", err: answerapplication.ErrAnswerJobProcessingCannotCancel, wantCode: errorCodeAnswerJobProcessing},
		{name: "terminal", err: answerapplication.ErrAnswerJobTerminalCannotCancel, wantCode: errorCodeAnswerJobTerminal},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			service := &fakeAnswerJobService{
				cancelFunc: func(context.Context, accessdomain.OwnerScope, int64) (answerapplication.Job, error) {
					return answerapplication.Job{}, errors.Join(errors.New("cancel failed"), test.err)
				},
			}
			request := httptest.NewRequest(http.MethodPost, "/answer-jobs/53/cancel", nil)
			response := httptest.NewRecorder()
			newAnswerJobTestRouter(service).ServeHTTP(response, request)

			if response.Code != http.StatusConflict {
				t.Fatalf("status = %d, want 409", response.Code)
			}
			var body errorResponse
			if err := json.Unmarshal(response.Body.Bytes(), &body); err != nil {
				t.Fatalf("decode response: %v", err)
			}
			if body.Code != test.wantCode {
				t.Fatalf("code = %q, want %q", body.Code, test.wantCode)
			}
		})
	}
}

func TestAnswerJobHandlerRejectsInvalidJobID(t *testing.T) {
	service := &fakeAnswerJobService{}
	request := httptest.NewRequest(http.MethodGet, "/answer-jobs/not-a-number", nil)
	response := httptest.NewRecorder()
	newAnswerJobTestRouter(service).ServeHTTP(response, request)

	if response.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, want 400", response.Code)
	}
	var body errorResponse
	if err := json.Unmarshal(response.Body.Bytes(), &body); err != nil {
		t.Fatalf("decode response: %v", err)
	}
	if body.Code != errorCodeInvalidAnswerJobID {
		t.Fatalf("code = %q, want %q", body.Code, errorCodeInvalidAnswerJobID)
	}
}
