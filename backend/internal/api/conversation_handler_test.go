package api

import (
	"context"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/gin-gonic/gin"

	conversationapplication "rag-reasoning-platform/backend/internal/application/conversation"
	accessdomain "rag-reasoning-platform/backend/internal/domain/access"
	conversationdomain "rag-reasoning-platform/backend/internal/domain/conversation"
)

type fakeConversationService struct {
	conversation        conversationdomain.Conversation
	listOutput          conversationapplication.ListOutput
	messages            conversationapplication.MessageListOutput
	cursorOutput        conversationapplication.ConversationCursorOutput
	messageCursorOutput conversationapplication.MessageCursorOutput
	appendResult        conversationdomain.AppendUserMessageResult
	err                 error
	createInput         conversationapplication.CreateInput
	appendInput         conversationapplication.AppendUserMessageInput
	updateInput         conversationapplication.UpdateInput
	deleteID            int64
	messageInput        conversationapplication.MessageListInput
	cursorInput         conversationapplication.ConversationCursorInput
	messageCursorInput  conversationapplication.MessageCursorInput
	listCalls           int
	messageCalls        int
	cursorCalls         int
	messageCursorCalls  int
	appendCalls         int
	updateCalls         int
	deleteCalls         int
}

func (f *fakeConversationService) Create(_ context.Context, _ accessdomain.OwnerScope, input conversationapplication.CreateInput) (conversationdomain.Conversation, error) {
	f.createInput = input
	return f.conversation, f.err
}

func (f *fakeConversationService) List(_ context.Context, _ accessdomain.OwnerScope, _ conversationapplication.ListInput) (conversationapplication.ListOutput, error) {
	f.listCalls++
	return f.listOutput, f.err
}

func (f *fakeConversationService) ListCursor(_ context.Context, _ accessdomain.OwnerScope, input conversationapplication.ConversationCursorInput) (conversationapplication.ConversationCursorOutput, error) {
	f.cursorCalls++
	f.cursorInput = input
	return f.cursorOutput, f.err
}

func (f *fakeConversationService) Get(_ context.Context, _ accessdomain.OwnerScope, _ int64) (conversationdomain.Conversation, error) {
	return f.conversation, f.err
}

func (f *fakeConversationService) Update(_ context.Context, _ accessdomain.OwnerScope, input conversationapplication.UpdateInput) (conversationdomain.Conversation, error) {
	f.updateCalls++
	f.updateInput = input
	return f.conversation, f.err
}

func (f *fakeConversationService) Delete(_ context.Context, _ accessdomain.OwnerScope, conversationID int64) error {
	f.deleteCalls++
	f.deleteID = conversationID
	return f.err
}

func (f *fakeConversationService) ListMessages(_ context.Context, _ accessdomain.OwnerScope, input conversationapplication.MessageListInput) (conversationapplication.MessageListOutput, error) {
	f.messageCalls++
	f.messageInput = input
	return f.messages, f.err
}

func (f *fakeConversationService) ListMessagesCursor(_ context.Context, _ accessdomain.OwnerScope, input conversationapplication.MessageCursorInput) (conversationapplication.MessageCursorOutput, error) {
	f.messageCursorCalls++
	f.messageCursorInput = input
	return f.messageCursorOutput, f.err
}

func (f *fakeConversationService) AppendUserMessage(_ context.Context, _ accessdomain.OwnerScope, input conversationapplication.AppendUserMessageInput) (conversationdomain.AppendUserMessageResult, error) {
	f.appendCalls++
	f.appendInput = input
	return f.appendResult, f.err
}

func newConversationTestRouter(service conversationService) *gin.Engine {
	gin.SetMode(gin.TestMode)
	router := gin.New()
	useTestAuthenticatedIdentity(router)
	NewConversationHandler(service, slog.New(slog.NewTextHandler(io.Discard, nil))).RegisterRoutes(router)
	return router
}

func TestConversationHandlerCreatesWithDefaultTitle(t *testing.T) {
	service := &fakeConversationService{
		conversation: conversationdomain.Conversation{
			ID: 11, OwnerUserID: testAPIOwnerUserID, Title: conversationapplication.DefaultTitle,
			Status: conversationdomain.StatusActive, CreatedAt: time.Unix(1, 0).UTC(), UpdatedAt: time.Unix(1, 0).UTC(),
		},
	}
	recorder := httptest.NewRecorder()
	request := httptest.NewRequest(http.MethodPost, "/conversations", strings.NewReader(`{}`))
	request.Header.Set("Content-Type", "application/json")
	newConversationTestRouter(service).ServeHTTP(recorder, request)

	if recorder.Code != http.StatusCreated {
		t.Fatalf("status = %d, want 201: %s", recorder.Code, recorder.Body.String())
	}
	if service.createInput.Title != "" {
		t.Fatalf("handler should pass empty title to application defaulting, got %q", service.createInput.Title)
	}
	if !strings.Contains(recorder.Body.String(), `"title":"New conversation"`) {
		t.Fatalf("body = %s, want default title", recorder.Body.String())
	}
}

func TestConversationHandlerAcceptsEmptyCreateBody(t *testing.T) {
	service := &fakeConversationService{
		conversation: conversationdomain.Conversation{
			ID: 11, OwnerUserID: testAPIOwnerUserID, Title: conversationapplication.DefaultTitle,
			Status: conversationdomain.StatusActive, CreatedAt: time.Unix(1, 0).UTC(), UpdatedAt: time.Unix(1, 0).UTC(),
		},
	}
	recorder := httptest.NewRecorder()
	request := httptest.NewRequest(http.MethodPost, "/conversations", nil)
	newConversationTestRouter(service).ServeHTTP(recorder, request)

	if recorder.Code != http.StatusCreated {
		t.Fatalf("status = %d, want 201: %s", recorder.Code, recorder.Body.String())
	}
	if service.createInput.Title != "" {
		t.Fatalf("handler should pass empty title to application defaulting, got %q", service.createInput.Title)
	}
}

func TestConversationHandlerUpdatesConversation(t *testing.T) {
	service := &fakeConversationService{conversation: conversationdomain.Conversation{
		ID: 11, OwnerUserID: testAPIOwnerUserID, Title: "Renamed", Status: conversationdomain.StatusArchived,
		CreatedAt: time.Unix(1, 0).UTC(), UpdatedAt: time.Unix(2, 0).UTC(),
	}}
	recorder := httptest.NewRecorder()
	request := httptest.NewRequest(
		http.MethodPatch,
		"/conversations/11",
		strings.NewReader(`{"title":"Renamed","status":"archived"}`),
	)
	request.Header.Set("Content-Type", "application/json")
	newConversationTestRouter(service).ServeHTTP(recorder, request)

	if recorder.Code != http.StatusOK || service.updateCalls != 1 || service.updateInput.ConversationID != 11 ||
		service.updateInput.Title == nil || *service.updateInput.Title != "Renamed" ||
		service.updateInput.Status == nil || *service.updateInput.Status != conversationdomain.StatusArchived {
		t.Fatalf("status/calls/input/body = %d / %d / %+v / %s", recorder.Code, service.updateCalls, service.updateInput, recorder.Body.String())
	}
}

func TestConversationHandlerRejectsInvalidConversationUpdates(t *testing.T) {
	tests := []struct {
		name       string
		body       string
		serviceErr error
		wantCalls  int
	}{
		{name: "empty body", body: "", wantCalls: 0},
		{name: "empty object", body: `{}`, serviceErr: conversationapplication.ErrEmptyUpdate, wantCalls: 1},
		{name: "unknown field", body: `{"unknown":true}`, wantCalls: 0},
		{name: "blank title", body: `{"title":"   "}`, serviceErr: conversationapplication.ErrInvalidTitle, wantCalls: 1},
		{name: "invalid status", body: `{"status":"deleted"}`, serviceErr: conversationapplication.ErrInvalidStatus, wantCalls: 1},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			service := &fakeConversationService{err: test.serviceErr}
			recorder := httptest.NewRecorder()
			request := httptest.NewRequest(http.MethodPatch, "/conversations/11", strings.NewReader(test.body))
			request.Header.Set("Content-Type", "application/json")
			newConversationTestRouter(service).ServeHTTP(recorder, request)

			if recorder.Code != http.StatusBadRequest || service.updateCalls != test.wantCalls ||
				!strings.Contains(recorder.Body.String(), `"code":"invalid_conversation_request"`) {
				t.Fatalf("status/calls/body = %d / %d / %s", recorder.Code, service.updateCalls, recorder.Body.String())
			}
		})
	}
}

func TestConversationHandlerRejectsArchiveWithActiveAnswer(t *testing.T) {
	service := &fakeConversationService{err: conversationdomain.ErrAnswerActive}
	recorder := httptest.NewRecorder()
	request := httptest.NewRequest(
		http.MethodPatch,
		"/conversations/11",
		strings.NewReader(`{"status":"archived"}`),
	)
	request.Header.Set("Content-Type", "application/json")
	newConversationTestRouter(service).ServeHTTP(recorder, request)

	if recorder.Code != http.StatusConflict || service.updateCalls != 1 ||
		!strings.Contains(recorder.Body.String(), `"code":"conversation_answer_active"`) {
		t.Fatalf("status/calls/body = %d / %d / %s", recorder.Code, service.updateCalls, recorder.Body.String())
	}
}

func TestConversationHandlerDeletesConversationAndMapsConflicts(t *testing.T) {
	tests := []struct {
		name       string
		err        error
		wantStatus int
		wantCode   string
	}{
		{name: "deleted", wantStatus: http.StatusNoContent},
		{name: "not found", err: conversationdomain.ErrNotFound, wantStatus: http.StatusNotFound, wantCode: "conversation_not_found"},
		{name: "active answer", err: conversationdomain.ErrAnswerActive, wantStatus: http.StatusConflict, wantCode: "conversation_answer_active"},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			service := &fakeConversationService{err: test.err}
			recorder := httptest.NewRecorder()
			request := httptest.NewRequest(http.MethodDelete, "/conversations/11", nil)
			newConversationTestRouter(service).ServeHTTP(recorder, request)

			if recorder.Code != test.wantStatus || service.deleteCalls != 1 || service.deleteID != 11 ||
				(test.wantCode != "" && !strings.Contains(recorder.Body.String(), `"code":"`+test.wantCode+`"`)) {
				t.Fatalf("status/calls/id/body = %d / %d / %d / %s", recorder.Code, service.deleteCalls, service.deleteID, recorder.Body.String())
			}
		})
	}
}

func TestConversationHandlerRequiresAuthentication(t *testing.T) {
	service := &fakeConversationService{}
	router := gin.New()
	NewConversationHandler(service, slog.New(slog.NewTextHandler(io.Discard, nil))).RegisterRoutes(router)
	recorder := httptest.NewRecorder()
	request := httptest.NewRequest(http.MethodGet, "/conversations", nil)
	router.ServeHTTP(recorder, request)
	if recorder.Code != http.StatusUnauthorized || service.listCalls != 0 {
		t.Fatalf("status=%d calls=%d body=%s, want 401/0", recorder.Code, service.listCalls, recorder.Body.String())
	}
}

func TestConversationHandlerListsMessagesAndMapsPagination(t *testing.T) {
	service := &fakeConversationService{
		messages: conversationapplication.MessageListOutput{
			ConversationID: 11,
			Messages: []conversationdomain.Message{{
				ID: 1, ConversationID: 11, SequenceNo: 1, Role: conversationdomain.RoleUser,
				Content: "hello", Status: conversationdomain.MessageStatusCompleted,
				CreatedAt: time.Unix(1, 0).UTC(), UpdatedAt: time.Unix(1, 0).UTC(),
			}},
			Page: 2, PageSize: 5, Total: 6, TotalPages: 2,
		},
	}
	recorder := httptest.NewRecorder()
	request := httptest.NewRequest(http.MethodGet, "/conversations/11/messages?page=2&page_size=5", nil)
	newConversationTestRouter(service).ServeHTTP(recorder, request)
	if recorder.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200: %s", recorder.Code, recorder.Body.String())
	}
	if !strings.Contains(recorder.Body.String(), `"conversation_id":11`) ||
		!strings.Contains(recorder.Body.String(), `"sequence_no":1`) {
		t.Fatalf("body = %s, missing message fields", recorder.Body.String())
	}
	if service.messageInput.Order != conversationdomain.MessageOrderAscending {
		t.Fatalf("default message order = %q, want asc", service.messageInput.Order)
	}
}

func TestConversationHandlerPassesDescendingMessageOrder(t *testing.T) {
	service := &fakeConversationService{messages: conversationapplication.MessageListOutput{
		ConversationID: 11, Messages: []conversationdomain.Message{}, Page: 1, PageSize: 5,
	}}
	recorder := httptest.NewRecorder()
	request := httptest.NewRequest(http.MethodGet, "/conversations/11/messages?page=1&page_size=5&order=desc", nil)
	newConversationTestRouter(service).ServeHTTP(recorder, request)

	if recorder.Code != http.StatusOK || service.messageCalls != 1 ||
		service.messageInput.Order != conversationdomain.MessageOrderDescending {
		t.Fatalf("status/calls/input = %d / %d / %+v", recorder.Code, service.messageCalls, service.messageInput)
	}
}

func TestConversationHandlerRejectsInvalidMessageOrder(t *testing.T) {
	for _, query := range []string{"order=", "order=ASC", "order=newest", "order=asc&order=desc"} {
		t.Run(query, func(t *testing.T) {
			service := &fakeConversationService{}
			recorder := httptest.NewRecorder()
			request := httptest.NewRequest(http.MethodGet, "/conversations/11/messages?"+query, nil)
			newConversationTestRouter(service).ServeHTTP(recorder, request)

			if recorder.Code != http.StatusBadRequest || service.messageCalls != 0 ||
				!strings.Contains(recorder.Body.String(), `"code":"invalid_conversation_message_order"`) {
				t.Fatalf("query/status/calls/body = %q / %d / %d / %s", query, recorder.Code, service.messageCalls, recorder.Body.String())
			}
		})
	}
}

func TestConversationHandlerMapsOverflowingPageToStableBadRequest(t *testing.T) {
	for _, path := range []string{
		"/conversations?page=9223372036854775807&page_size=20",
		"/conversations/11/messages?page=9223372036854775807&page_size=20",
	} {
		t.Run(path, func(t *testing.T) {
			service := &fakeConversationService{err: conversationapplication.ErrInvalidPage}
			recorder := httptest.NewRecorder()
			request := httptest.NewRequest(http.MethodGet, path, nil)
			newConversationTestRouter(service).ServeHTTP(recorder, request)

			if recorder.Code != http.StatusBadRequest ||
				!strings.Contains(recorder.Body.String(), `"code":"invalid_conversation_page"`) {
				t.Fatalf("path/status/body = %s / %d / %s", path, recorder.Code, recorder.Body.String())
			}
		})
	}
}

func TestConversationHandlerUsesCursorDefaults(t *testing.T) {
	service := &fakeConversationService{
		cursorOutput: conversationapplication.ConversationCursorOutput{Conversations: []conversationdomain.Conversation{}},
		messageCursorOutput: conversationapplication.MessageCursorOutput{
			ConversationID: 11, Messages: []conversationdomain.Message{},
		},
	}
	conversationRecorder := httptest.NewRecorder()
	newConversationTestRouter(service).ServeHTTP(
		conversationRecorder,
		httptest.NewRequest(http.MethodGet, "/conversations/cursor", nil),
	)
	if conversationRecorder.Code != http.StatusOK || service.cursorCalls != 1 ||
		service.cursorInput.PageSize != conversationapplication.DefaultConversationCursorPageSize {
		t.Fatalf("conversation cursor status/calls/input = %d / %d / %+v", conversationRecorder.Code, service.cursorCalls, service.cursorInput)
	}

	messageRecorder := httptest.NewRecorder()
	newConversationTestRouter(service).ServeHTTP(
		messageRecorder,
		httptest.NewRequest(http.MethodGet, "/conversations/11/messages/cursor", nil),
	)
	if messageRecorder.Code != http.StatusOK || service.messageCursorCalls != 1 ||
		service.messageCursorInput.ConversationID != 11 ||
		service.messageCursorInput.PageSize != conversationapplication.DefaultMessageCursorPageSize {
		t.Fatalf("message cursor status/calls/input = %d / %d / %+v", messageRecorder.Code, service.messageCursorCalls, service.messageCursorInput)
	}
}

func TestConversationHandlerRejectsEmptyAndRepeatedCursor(t *testing.T) {
	tests := []struct {
		path string
		code string
	}{
		{path: "/conversations/cursor?cursor=", code: "invalid_conversation_cursor"},
		{path: "/conversations/cursor?cursor=a&cursor=b", code: "invalid_conversation_cursor"},
		{path: "/conversations/11/messages/cursor?cursor=", code: "invalid_conversation_message_cursor"},
		{path: "/conversations/11/messages/cursor?cursor=a&cursor=b", code: "invalid_conversation_message_cursor"},
	}
	for _, test := range tests {
		t.Run(test.path, func(t *testing.T) {
			service := &fakeConversationService{}
			recorder := httptest.NewRecorder()
			newConversationTestRouter(service).ServeHTTP(recorder, httptest.NewRequest(http.MethodGet, test.path, nil))
			if recorder.Code != http.StatusBadRequest ||
				!strings.Contains(recorder.Body.String(), `"code":"`+test.code+`"`) {
				t.Fatalf("path/status/body = %s / %d / %s", test.path, recorder.Code, recorder.Body.String())
			}
		})
	}
}

func TestConversationHandlerMapsIdempotentAppendStatus(t *testing.T) {
	clientID := "client-1"
	service := &fakeConversationService{
		appendResult: conversationdomain.AppendUserMessageResult{
			Created: false,
			Message: conversationdomain.Message{
				ID: 1, ConversationID: 11, SequenceNo: 1, Role: conversationdomain.RoleUser,
				Content: "hello", Status: conversationdomain.MessageStatusCompleted,
				CreatedAt: time.Unix(1, 0).UTC(), UpdatedAt: time.Unix(1, 0).UTC(),
			},
		},
	}
	recorder := httptest.NewRecorder()
	request := httptest.NewRequest(http.MethodPost, "/conversations/11/messages", strings.NewReader(`{"content":"hello","client_message_id":"client-1"}`))
	request.Header.Set("Content-Type", "application/json")
	newConversationTestRouter(service).ServeHTTP(recorder, request)
	if recorder.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200 for idempotent replay: %s", recorder.Code, recorder.Body.String())
	}
	if service.appendCalls != 1 || service.appendInput.ClientMessageID == nil || *service.appendInput.ClientMessageID != clientID {
		t.Fatalf("append input/calls = %+v/%d", service.appendInput, service.appendCalls)
	}
}

func TestConversationHandlerMapsErrors(t *testing.T) {
	tests := []struct {
		name   string
		err    error
		status int
		body   string
	}{
		{name: "not found", err: conversationdomain.ErrNotFound, status: http.StatusNotFound, body: "conversation not found"},
		{name: "invalid message", err: conversationapplication.ErrInvalidMessage, status: http.StatusBadRequest, body: "conversation message is invalid"},
		{name: "duplicate client id", err: conversationdomain.ErrDuplicateClientMessage, status: http.StatusConflict, body: "client message ID was already used with different content"},
		{name: "archived", err: conversationdomain.ErrArchived, status: http.StatusConflict, body: "conversation is archived"},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			service := &fakeConversationService{err: test.err}
			recorder := httptest.NewRecorder()
			request := httptest.NewRequest(http.MethodPost, "/conversations/11/messages", strings.NewReader(`{"content":"hello"}`))
			request.Header.Set("Content-Type", "application/json")
			newConversationTestRouter(service).ServeHTTP(recorder, request)
			if recorder.Code != test.status || !strings.Contains(recorder.Body.String(), test.body) {
				t.Fatalf("status/body = %d/%s", recorder.Code, recorder.Body.String())
			}
		})
	}
}
