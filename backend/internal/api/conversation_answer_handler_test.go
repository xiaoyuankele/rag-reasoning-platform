package api

import (
	"context"
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/gin-gonic/gin"

	answerapplication "rag-reasoning-platform/backend/internal/application/answer"
	conversationapplication "rag-reasoning-platform/backend/internal/application/conversation"
	accessdomain "rag-reasoning-platform/backend/internal/domain/access"
	conversationdomain "rag-reasoning-platform/backend/internal/domain/conversation"
)

type fakeConversationTurnService struct {
	result             conversationdomain.StartTurnResult
	err                error
	input              conversationapplication.StartTurnInput
	cleanupInput       conversationapplication.FailAssistantIfUnqueuedInput
	cleanupCalls       int
	cleanupContextErr  error
	cleanupHasDeadline bool
	cleanupValue       any
	cleanupCompleted   bool
}

type cleanupContextValueKey struct{}

func (f *fakeConversationTurnService) StartTurn(
	_ context.Context,
	_ accessdomain.OwnerScope,
	input conversationapplication.StartTurnInput,
) (conversationdomain.StartTurnResult, error) {
	f.input = input
	return f.result, f.err
}

func (f *fakeConversationTurnService) FailAssistantIfUnqueued(
	ctx context.Context,
	_ accessdomain.OwnerScope,
	input conversationapplication.FailAssistantIfUnqueuedInput,
) (conversationdomain.Message, error) {
	f.cleanupCalls++
	f.cleanupInput = input
	f.cleanupContextErr = ctx.Err()
	_, f.cleanupHasDeadline = ctx.Deadline()
	f.cleanupValue = ctx.Value(cleanupContextValueKey{})
	f.cleanupCompleted = true
	return f.result.AssistantMessage, f.err
}

type fakeConversationAnswerJobService struct {
	job        answerapplication.Job
	err        error
	input      answerapplication.ConversationQueueInput
	queueCalls int
	activeID   int64
	latestID   int64
	scope      accessdomain.OwnerScope
}

func (f *fakeConversationAnswerJobService) QueueConversation(
	_ context.Context,
	_ accessdomain.OwnerScope,
	input answerapplication.ConversationQueueInput,
) (answerapplication.Job, error) {
	f.queueCalls++
	f.input = input
	return f.job, f.err
}

func (f *fakeConversationAnswerJobService) GetActiveConversationJob(
	_ context.Context,
	scope accessdomain.OwnerScope,
	conversationID int64,
) (answerapplication.Job, error) {
	f.scope = scope
	f.activeID = conversationID
	return f.job, f.err
}

func (f *fakeConversationAnswerJobService) GetLatestConversationJob(
	_ context.Context,
	scope accessdomain.OwnerScope,
	conversationID int64,
) (answerapplication.Job, error) {
	f.scope = scope
	f.latestID = conversationID
	return f.job, f.err
}

func newConversationAnswerTestRouter(
	turns conversationTurnService,
	answer conversationAnswerJobService,
) *gin.Engine {
	gin.SetMode(gin.TestMode)
	router := gin.New()
	useTestAuthenticatedIdentity(router)
	NewConversationAnswerHandler(
		turns,
		answer,
		slog.New(slog.NewTextHandler(io.Discard, nil)),
	).RegisterRoutes(router)
	return router
}

func TestConversationAnswerHandlerStartsTurnAndQueuesLinkedJob(t *testing.T) {
	now := time.Unix(1, 0).UTC()
	turns := &fakeConversationTurnService{
		result: conversationdomain.StartTurnResult{
			Created: true,
			UserMessage: conversationdomain.Message{
				ID: 21, ConversationID: 11, SequenceNo: 1, Role: conversationdomain.RoleUser,
				Content: "explain", Status: conversationdomain.MessageStatusCompleted,
				CreatedAt: now, UpdatedAt: now,
			},
			AssistantMessage: conversationdomain.Message{
				ID: 22, ConversationID: 11, SequenceNo: 2, Role: conversationdomain.RoleAssistant,
				Content: conversationdomain.AssistantGeneratingPlaceholder, Status: conversationdomain.MessageStatusGenerating,
				CreatedAt: now, UpdatedAt: now,
			},
		},
	}
	answer := &fakeConversationAnswerJobService{
		job: answerapplication.Job{
			ID: 31, OwnerUserID: testAPIOwnerUserID, ConversationID: int64Pointer(11),
			UserMessageID: int64Pointer(21), AssistantMessageID: int64Pointer(22),
			Query: "explain", TopK: 5, RequestedResponseLanguage: answerapplication.ResponseLanguageChinese,
			Status: answerapplication.JobStatusQueued, NextAttemptAt: now, CreatedAt: now, UpdatedAt: now,
		},
	}

	recorder := httptest.NewRecorder()
	request := httptest.NewRequest(
		http.MethodPost,
		"/conversations/11/answer-jobs",
		strings.NewReader(`{"content":"explain","client_message_id":"client-1","top_k":5,"response_language":"zh"}`),
	)
	request.Header.Set("Content-Type", "application/json")
	newConversationAnswerTestRouter(turns, answer).ServeHTTP(recorder, request)

	if recorder.Code != http.StatusAccepted {
		t.Fatalf("status = %d, want 202: %s", recorder.Code, recorder.Body.String())
	}
	if turns.input.ConversationID != 11 || turns.input.ClientMessageID == nil || *turns.input.ClientMessageID != "client-1" {
		t.Fatalf("turn input = %+v", turns.input)
	}
	if answer.input.ConversationID != 11 || answer.input.UserMessageID != 21 || answer.input.AssistantMessageID != 22 ||
		answer.input.Input.Query != "explain" || answer.input.Input.TopK != 5 {
		t.Fatalf("answer input = %+v", answer.input)
	}
	if !strings.Contains(recorder.Body.String(), `"assistant_message"`) || !strings.Contains(recorder.Body.String(), `"status":"queued"`) {
		t.Fatalf("body = %s", recorder.Body.String())
	}
	if turns.cleanupCalls != 0 {
		t.Fatalf("cleanup calls = %d, want 0 after successful queue", turns.cleanupCalls)
	}
}

func TestConversationAnswerHandlerConditionallyFailsPlaceholderAfterQueueError(t *testing.T) {
	now := time.Unix(1, 0).UTC()
	turns := &fakeConversationTurnService{
		result: conversationdomain.StartTurnResult{
			Created:     true,
			UserMessage: conversationdomain.Message{ID: 21, ConversationID: 11, Role: conversationdomain.RoleUser},
			AssistantMessage: conversationdomain.Message{
				ID: 22, ConversationID: 11, Role: conversationdomain.RoleAssistant,
				Content: conversationdomain.AssistantGeneratingPlaceholder,
				Status:  conversationdomain.MessageStatusGenerating, CreatedAt: now, UpdatedAt: now,
			},
		},
	}
	answer := &fakeConversationAnswerJobService{err: answerapplication.ErrAnswerOwnerQueueCapacity}

	recorder := httptest.NewRecorder()
	request := httptest.NewRequest(
		http.MethodPost,
		"/conversations/11/answer-jobs",
		strings.NewReader(`{"content":"explain","client_message_id":"client-1","top_k":5,"response_language":"zh"}`),
	)
	request.Header.Set("Content-Type", "application/json")
	newConversationAnswerTestRouter(turns, answer).ServeHTTP(recorder, request)

	if recorder.Code != http.StatusTooManyRequests {
		t.Fatalf("status/body = %d / %s, want 429", recorder.Code, recorder.Body.String())
	}
	if turns.cleanupCalls != 1 || turns.cleanupInput.ConversationID != 11 || turns.cleanupInput.MessageID != 22 {
		t.Fatalf("cleanup calls/input = %d / %+v", turns.cleanupCalls, turns.cleanupInput)
	}
}

func TestConversationAnswerHandlerRejectsArchivedConversation(t *testing.T) {
	turns := &fakeConversationTurnService{err: conversationdomain.ErrArchived}
	answer := &fakeConversationAnswerJobService{}
	recorder := httptest.NewRecorder()
	request := httptest.NewRequest(
		http.MethodPost,
		"/conversations/11/answer-jobs",
		strings.NewReader(`{"content":"explain","client_message_id":"client-1","top_k":5,"response_language":"zh"}`),
	)
	request.Header.Set("Content-Type", "application/json")
	newConversationAnswerTestRouter(turns, answer).ServeHTTP(recorder, request)

	if recorder.Code != http.StatusConflict || answer.queueCalls != 0 ||
		!strings.Contains(recorder.Body.String(), `"code":"conversation_archived"`) {
		t.Fatalf("status/queue/body = %d / %d / %s", recorder.Code, answer.queueCalls, recorder.Body.String())
	}
}

func TestConversationAnswerHandlerMapsQueueConversationStateErrors(t *testing.T) {
	tests := []struct {
		name       string
		err        error
		wantStatus int
		wantCode   string
	}{
		{name: "archived after turn", err: conversationdomain.ErrArchived, wantStatus: http.StatusConflict, wantCode: "conversation_archived"},
		{name: "deleted after turn", err: conversationdomain.ErrNotFound, wantStatus: http.StatusNotFound, wantCode: "conversation_not_found"},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			turns := &fakeConversationTurnService{result: validConversationAnswerTurn(time.Unix(1, 0).UTC())}
			answer := &fakeConversationAnswerJobService{err: test.err}
			recorder := httptest.NewRecorder()
			request := httptest.NewRequest(
				http.MethodPost,
				"/conversations/11/answer-jobs",
				strings.NewReader(`{"content":"explain","client_message_id":"client-1","top_k":5,"response_language":"zh"}`),
			)
			request.Header.Set("Content-Type", "application/json")
			newConversationAnswerTestRouter(turns, answer).ServeHTTP(recorder, request)

			if recorder.Code != test.wantStatus || turns.cleanupCalls != 1 ||
				!strings.Contains(recorder.Body.String(), `"code":"`+test.wantCode+`"`) {
				t.Fatalf("status/cleanup/body = %d / %d / %s", recorder.Code, turns.cleanupCalls, recorder.Body.String())
			}
		})
	}
}

func TestConversationAnswerHandlerCleanupSurvivesCanceledRequestContext(t *testing.T) {
	now := time.Unix(1, 0).UTC()
	turns := &fakeConversationTurnService{result: validConversationAnswerTurn(now)}
	answer := &fakeConversationAnswerJobService{err: answerapplication.ErrAnswerOwnerQueueCapacity}

	recorder := httptest.NewRecorder()
	request := httptest.NewRequest(
		http.MethodPost,
		"/conversations/11/answer-jobs",
		strings.NewReader(`{"content":"explain","client_message_id":"client-1","top_k":5,"response_language":"zh"}`),
	)
	request.Header.Set("Content-Type", "application/json")
	requestContext := context.WithValue(request.Context(), cleanupContextValueKey{}, "preserved")
	requestContext, cancelRequest := context.WithCancel(requestContext)
	cancelRequest()
	request = request.WithContext(requestContext)

	newConversationAnswerTestRouter(turns, answer).ServeHTTP(recorder, request)

	if recorder.Code != http.StatusTooManyRequests || turns.cleanupCalls != 1 || !turns.cleanupCompleted {
		t.Fatalf("status/cleanup = %d / %d / %t", recorder.Code, turns.cleanupCalls, turns.cleanupCompleted)
	}
	if turns.cleanupContextErr != nil || !turns.cleanupHasDeadline || turns.cleanupValue != "preserved" {
		t.Fatalf(
			"cleanup context = err:%v deadline:%t value:%v",
			turns.cleanupContextErr,
			turns.cleanupHasDeadline,
			turns.cleanupValue,
		)
	}
}

func TestConversationAnswerHandlerReturnsGeneratingAssistantAfterNoJobRetry(t *testing.T) {
	now := time.Unix(1, 0).UTC()
	turns := &fakeConversationTurnService{
		result: conversationdomain.StartTurnResult{
			Created:     false,
			UserMessage: conversationdomain.Message{ID: 21, ConversationID: 11, Role: conversationdomain.RoleUser},
			AssistantMessage: conversationdomain.Message{
				ID: 22, ConversationID: 11, Role: conversationdomain.RoleAssistant,
				Content: "queue failed", Status: conversationdomain.MessageStatusFailed,
				CreatedAt: now, UpdatedAt: now,
			},
		},
	}
	conversationID := int64(11)
	answer := &fakeConversationAnswerJobService{job: answerapplication.Job{
		ID: 31, ConversationID: &conversationID, UserMessageID: int64Pointer(21), AssistantMessageID: int64Pointer(22),
		Status:        answerapplication.JobStatusQueued,
		NextAttemptAt: now, CreatedAt: now, UpdatedAt: now,
	}}
	recorder := httptest.NewRecorder()
	request := httptest.NewRequest(
		http.MethodPost,
		"/conversations/11/answer-jobs",
		strings.NewReader(`{"content":"explain","client_message_id":"client-1","top_k":5,"response_language":"zh"}`),
	)
	request.Header.Set("Content-Type", "application/json")
	newConversationAnswerTestRouter(turns, answer).ServeHTTP(recorder, request)

	var response conversationAnswerResponse
	if err := json.Unmarshal(recorder.Body.Bytes(), &response); err != nil {
		t.Fatalf("decode response: %v", err)
	}
	if recorder.Code != http.StatusOK ||
		response.Assistant.Status != string(conversationdomain.MessageStatusGenerating) ||
		response.Assistant.Content != conversationdomain.AssistantGeneratingPlaceholder {
		t.Fatalf("status/assistant = %d / %+v", recorder.Code, response.Assistant)
	}
}

func TestConversationAnswerHandlerRejectsInconsistentTurnMappings(t *testing.T) {
	testCases := []struct {
		name   string
		mutate func(*conversationdomain.StartTurnResult)
	}{
		{name: "user conversation", mutate: func(turn *conversationdomain.StartTurnResult) { turn.UserMessage.ConversationID = 12 }},
		{name: "assistant conversation", mutate: func(turn *conversationdomain.StartTurnResult) { turn.AssistantMessage.ConversationID = 12 }},
		{name: "user role", mutate: func(turn *conversationdomain.StartTurnResult) {
			turn.UserMessage.Role = conversationdomain.RoleAssistant
		}},
		{name: "assistant role", mutate: func(turn *conversationdomain.StartTurnResult) {
			turn.AssistantMessage.Role = conversationdomain.RoleUser
		}},
	}

	for _, testCase := range testCases {
		t.Run(testCase.name, func(t *testing.T) {
			now := time.Unix(1, 0).UTC()
			turn := validConversationAnswerTurn(now)
			testCase.mutate(&turn)
			turns := &fakeConversationTurnService{result: turn}
			answer := &fakeConversationAnswerJobService{job: validConversationAnswerJob(now)}
			recorder := httptest.NewRecorder()
			request := httptest.NewRequest(
				http.MethodPost,
				"/conversations/11/answer-jobs",
				strings.NewReader(`{"content":"explain","client_message_id":"client-1","top_k":5,"response_language":"zh"}`),
			)
			request.Header.Set("Content-Type", "application/json")
			newConversationAnswerTestRouter(turns, answer).ServeHTTP(recorder, request)

			if recorder.Code != http.StatusInternalServerError || answer.queueCalls != 0 {
				t.Fatalf("status/queue calls = %d / %d, want 500 / 0", recorder.Code, answer.queueCalls)
			}
		})
	}
}

func TestConversationAnswerHandlerRejectsInconsistentJobMappings(t *testing.T) {
	testCases := []struct {
		name   string
		mutate func(*answerapplication.Job)
	}{
		{name: "conversation", mutate: func(job *answerapplication.Job) { job.ConversationID = int64Pointer(12) }},
		{name: "user message", mutate: func(job *answerapplication.Job) { job.UserMessageID = int64Pointer(23) }},
		{name: "assistant message", mutate: func(job *answerapplication.Job) { job.AssistantMessageID = int64Pointer(24) }},
		{name: "missing association", mutate: func(job *answerapplication.Job) { job.UserMessageID = nil }},
	}

	for _, testCase := range testCases {
		t.Run(testCase.name, func(t *testing.T) {
			now := time.Unix(1, 0).UTC()
			job := validConversationAnswerJob(now)
			testCase.mutate(&job)
			turns := &fakeConversationTurnService{result: validConversationAnswerTurn(now)}
			answer := &fakeConversationAnswerJobService{job: job}
			recorder := httptest.NewRecorder()
			request := httptest.NewRequest(
				http.MethodPost,
				"/conversations/11/answer-jobs",
				strings.NewReader(`{"content":"explain","client_message_id":"client-1","top_k":5,"response_language":"zh"}`),
			)
			request.Header.Set("Content-Type", "application/json")
			newConversationAnswerTestRouter(turns, answer).ServeHTTP(recorder, request)

			if recorder.Code != http.StatusInternalServerError || answer.queueCalls != 1 || turns.cleanupCalls != 0 {
				t.Fatalf(
					"status/queue/cleanup = %d / %d / %d, want 500 / 1 / 0",
					recorder.Code,
					answer.queueCalls,
					turns.cleanupCalls,
				)
			}
		})
	}
}

func TestConversationAnswerHandlerGetsActiveJob(t *testing.T) {
	now := time.Unix(2, 0).UTC()
	conversationID := int64(11)
	answer := &fakeConversationAnswerJobService{
		job: answerapplication.Job{
			ID: 31, ConversationID: &conversationID, Status: answerapplication.JobStatusProcessing,
			NextAttemptAt: now, CreatedAt: now, UpdatedAt: now,
		},
	}

	recorder := httptest.NewRecorder()
	request := httptest.NewRequest(http.MethodGet, "/conversations/11/answer-jobs/active", nil)
	newConversationAnswerTestRouter(&fakeConversationTurnService{}, answer).ServeHTTP(recorder, request)

	if recorder.Code != http.StatusOK || !strings.Contains(recorder.Body.String(), `"status":"processing"`) {
		t.Fatalf("status/body = %d / %s, want active processing job", recorder.Code, recorder.Body.String())
	}
	if answer.activeID != 11 || answer.scope.OwnerUserID() != testAPIOwnerUserID {
		t.Fatalf("active lookup = conversation %d scope %+v", answer.activeID, answer.scope)
	}
}

func TestConversationAnswerHandlerGetsLatestTerminalJobSnapshot(t *testing.T) {
	now := time.Unix(3, 0).UTC()
	conversationID := int64(11)
	answer := &fakeConversationAnswerJobService{job: answerapplication.Job{
		ID: 32, ConversationID: &conversationID, Status: answerapplication.JobStatusSucceeded,
		NextAttemptAt: now, CreatedAt: now, UpdatedAt: now,
		Result: &answerapplication.JobResult{
			Answer: "latest answer", ResponseLanguage: answerapplication.ResponseLanguageEnglish,
			Sources:      []answerapplication.Source{{Citation: 1, ChunkID: 7, DocumentID: 8, OriginalName: "source.pdf"}},
			PromptTokens: 10, CompletionTokens: 4, TotalTokens: 14,
		},
	}}

	recorder := httptest.NewRecorder()
	request := httptest.NewRequest(http.MethodGet, "/conversations/11/answer-jobs/latest", nil)
	newConversationAnswerTestRouter(&fakeConversationTurnService{}, answer).ServeHTTP(recorder, request)

	body := recorder.Body.String()
	if recorder.Code != http.StatusOK || answer.latestID != 11 || answer.scope.OwnerUserID() != testAPIOwnerUserID ||
		!strings.Contains(body, `"answer":"latest answer"`) ||
		!strings.Contains(body, `"original_name":"source.pdf"`) ||
		!strings.Contains(body, `"total_tokens":14`) {
		t.Fatalf("status/latest/body = %d / %d / %s", recorder.Code, answer.latestID, body)
	}
}

func TestConversationAnswerHandlerMapsLatestJobAbsenceAndConversationNotFound(t *testing.T) {
	testCases := []struct {
		name       string
		err        error
		wantStatus int
		wantBody   string
	}{
		{name: "no retained job", err: answerapplication.ErrAnswerJobNotFound, wantStatus: http.StatusNoContent},
		{name: "conversation not found", err: conversationdomain.ErrNotFound, wantStatus: http.StatusNotFound, wantBody: `"code":"conversation_not_found"`},
	}

	for _, testCase := range testCases {
		t.Run(testCase.name, func(t *testing.T) {
			recorder := httptest.NewRecorder()
			request := httptest.NewRequest(http.MethodGet, "/conversations/11/answer-jobs/latest", nil)
			newConversationAnswerTestRouter(
				&fakeConversationTurnService{},
				&fakeConversationAnswerJobService{err: testCase.err},
			).ServeHTTP(recorder, request)

			if recorder.Code != testCase.wantStatus ||
				(testCase.wantBody != "" && !strings.Contains(recorder.Body.String(), testCase.wantBody)) {
				t.Fatalf("status/body = %d / %s, want %d containing %q", recorder.Code, recorder.Body.String(), testCase.wantStatus, testCase.wantBody)
			}
		})
	}
}

func TestConversationAnswerHandlerMapsActiveJobAbsenceAndConversationNotFound(t *testing.T) {
	testCases := []struct {
		name       string
		err        error
		wantStatus int
		wantBody   string
	}{
		{name: "no active job", err: answerapplication.ErrAnswerJobNotFound, wantStatus: http.StatusNoContent},
		{name: "conversation not found", err: conversationdomain.ErrNotFound, wantStatus: http.StatusNotFound, wantBody: `"code":"conversation_not_found"`},
	}

	for _, testCase := range testCases {
		t.Run(testCase.name, func(t *testing.T) {
			recorder := httptest.NewRecorder()
			request := httptest.NewRequest(http.MethodGet, "/conversations/11/answer-jobs/active", nil)
			newConversationAnswerTestRouter(
				&fakeConversationTurnService{},
				&fakeConversationAnswerJobService{err: testCase.err},
			).ServeHTTP(recorder, request)

			if recorder.Code != testCase.wantStatus ||
				(testCase.wantBody != "" && !strings.Contains(recorder.Body.String(), testCase.wantBody)) {
				t.Fatalf("status/body = %d / %s, want %d containing %q", recorder.Code, recorder.Body.String(), testCase.wantStatus, testCase.wantBody)
			}
		})
	}
}

func TestConversationAnswerHandlerRequiresAuthenticationForActiveLookup(t *testing.T) {
	gin.SetMode(gin.TestMode)
	router := gin.New()
	answer := &fakeConversationAnswerJobService{}
	NewConversationAnswerHandler(
		&fakeConversationTurnService{},
		answer,
		slog.New(slog.NewTextHandler(io.Discard, nil)),
	).RegisterRoutes(router)

	recorder := httptest.NewRecorder()
	request := httptest.NewRequest(http.MethodGet, "/conversations/11/answer-jobs/active", nil)
	router.ServeHTTP(recorder, request)

	if recorder.Code != http.StatusUnauthorized || answer.activeID != 0 {
		t.Fatalf("status/lookup ID = %d / %d, want 401 and no repository call", recorder.Code, answer.activeID)
	}
}

func TestConversationAnswerHandlerRequiresAuthenticationForLatestLookup(t *testing.T) {
	gin.SetMode(gin.TestMode)
	router := gin.New()
	answer := &fakeConversationAnswerJobService{}
	NewConversationAnswerHandler(
		&fakeConversationTurnService{},
		answer,
		slog.New(slog.NewTextHandler(io.Discard, nil)),
	).RegisterRoutes(router)

	recorder := httptest.NewRecorder()
	request := httptest.NewRequest(http.MethodGet, "/conversations/11/answer-jobs/latest", nil)
	router.ServeHTTP(recorder, request)

	if recorder.Code != http.StatusUnauthorized || answer.latestID != 0 {
		t.Fatalf("status/lookup ID = %d / %d, want 401 and no repository call", recorder.Code, answer.latestID)
	}
}

func validConversationAnswerTurn(now time.Time) conversationdomain.StartTurnResult {
	return conversationdomain.StartTurnResult{
		Created: true,
		UserMessage: conversationdomain.Message{
			ID: 21, ConversationID: 11, SequenceNo: 1, Role: conversationdomain.RoleUser,
			Content: "explain", Status: conversationdomain.MessageStatusCompleted,
			CreatedAt: now, UpdatedAt: now,
		},
		AssistantMessage: conversationdomain.Message{
			ID: 22, ConversationID: 11, SequenceNo: 2, Role: conversationdomain.RoleAssistant,
			Content: conversationdomain.AssistantGeneratingPlaceholder, Status: conversationdomain.MessageStatusGenerating,
			CreatedAt: now, UpdatedAt: now,
		},
	}
}

func validConversationAnswerJob(now time.Time) answerapplication.Job {
	return answerapplication.Job{
		ID: 31, OwnerUserID: testAPIOwnerUserID, ConversationID: int64Pointer(11),
		UserMessageID: int64Pointer(21), AssistantMessageID: int64Pointer(22),
		Query: "explain", TopK: 5, RequestedResponseLanguage: answerapplication.ResponseLanguageChinese,
		Status: answerapplication.JobStatusQueued, NextAttemptAt: now, CreatedAt: now, UpdatedAt: now,
	}
}

func int64Pointer(value int64) *int64 { return &value }
