package conversation

import (
	"context"
	"encoding/base64"
	"errors"
	"math"
	"strings"
	"testing"
	"time"

	accessdomain "rag-reasoning-platform/backend/internal/domain/access"
	conversationdomain "rag-reasoning-platform/backend/internal/domain/conversation"
)

type fakeConversationRepository struct {
	createdInput              conversationdomain.CreateInput
	appendInput               conversationdomain.AppendUserMessageInput
	startInput                conversationdomain.StartTurnInput
	failInput                 conversationdomain.FailAssistantIfUnqueuedInput
	messageOptions            conversationdomain.ListOptions
	updateInput               conversationdomain.UpdateInput
	deleteID                  int64
	conversationCursorOptions conversationdomain.ConversationCursorOptions
	conversationCursorResult  conversationdomain.ConversationCursorResult
	messageCursorOptions      conversationdomain.MessageCursorOptions
	messageCursorResult       conversationdomain.MessageCursorResult
	conversation              conversationdomain.Conversation
	messages                  []conversationdomain.Message
	appendResult              conversationdomain.AppendUserMessageResult
	err                       error
}

func (f *fakeConversationRepository) CreateConversation(_ context.Context, _ accessdomain.OwnerScope, input conversationdomain.CreateInput) (conversationdomain.Conversation, error) {
	f.createdInput = input
	return f.conversation, f.err
}

func (f *fakeConversationRepository) ListConversations(_ context.Context, _ accessdomain.OwnerScope, _ conversationdomain.ListOptions) (conversationdomain.ConversationListResult, error) {
	return conversationdomain.ConversationListResult{Conversations: []conversationdomain.Conversation{f.conversation}, Total: 1}, f.err
}

func (f *fakeConversationRepository) ListConversationsCursor(_ context.Context, _ accessdomain.OwnerScope, options conversationdomain.ConversationCursorOptions) (conversationdomain.ConversationCursorResult, error) {
	f.conversationCursorOptions = options
	return f.conversationCursorResult, f.err
}

func (f *fakeConversationRepository) GetConversation(_ context.Context, _ accessdomain.OwnerScope, _ int64) (conversationdomain.Conversation, error) {
	return f.conversation, f.err
}

func (f *fakeConversationRepository) UpdateConversation(_ context.Context, _ accessdomain.OwnerScope, input conversationdomain.UpdateInput) (conversationdomain.Conversation, error) {
	f.updateInput = input
	return f.conversation, f.err
}

func (f *fakeConversationRepository) DeleteConversation(_ context.Context, _ accessdomain.OwnerScope, conversationID int64) error {
	f.deleteID = conversationID
	return f.err
}

func (f *fakeConversationRepository) ListMessages(_ context.Context, _ accessdomain.OwnerScope, _ int64, options conversationdomain.ListOptions) (conversationdomain.MessageListResult, error) {
	f.messageOptions = options
	return conversationdomain.MessageListResult{Messages: f.messages, Total: int64(len(f.messages))}, f.err
}

func (f *fakeConversationRepository) ListMessagesCursor(_ context.Context, _ accessdomain.OwnerScope, _ int64, options conversationdomain.MessageCursorOptions) (conversationdomain.MessageCursorResult, error) {
	f.messageCursorOptions = options
	return f.messageCursorResult, f.err
}

func (f *fakeConversationRepository) AppendUserMessage(_ context.Context, _ accessdomain.OwnerScope, input conversationdomain.AppendUserMessageInput) (conversationdomain.AppendUserMessageResult, error) {
	f.appendInput = input
	return f.appendResult, f.err
}

func (f *fakeConversationRepository) StartTurn(_ context.Context, _ accessdomain.OwnerScope, input conversationdomain.StartTurnInput) (conversationdomain.StartTurnResult, error) {
	f.startInput = input
	return conversationdomain.StartTurnResult{}, f.err
}

func (f *fakeConversationRepository) UpdateAssistantMessage(_ context.Context, _ accessdomain.OwnerScope, _ conversationdomain.UpdateAssistantMessageInput) (conversationdomain.Message, error) {
	return conversationdomain.Message{}, f.err
}

func (f *fakeConversationRepository) FailAssistantIfUnqueued(_ context.Context, _ accessdomain.OwnerScope, input conversationdomain.FailAssistantIfUnqueuedInput) (conversationdomain.Message, error) {
	f.failInput = input
	return conversationdomain.Message{}, f.err
}

func testOwnerScope(t *testing.T) accessdomain.OwnerScope {
	t.Helper()
	scope, err := accessdomain.NewOwnerScope(7)
	if err != nil {
		t.Fatalf("create owner scope: %v", err)
	}
	return scope
}

func TestServiceCreateUsesDefaultTitle(t *testing.T) {
	repository := &fakeConversationRepository{}
	service := NewService(repository)

	created, err := service.Create(context.Background(), testOwnerScope(t), CreateInput{})
	if err != nil {
		t.Fatalf("Create() error = %v", err)
	}
	if repository.createdInput.Title != DefaultTitle {
		t.Fatalf("stored title = %q, want %q", repository.createdInput.Title, DefaultTitle)
	}
	if created.ID != 0 {
		t.Fatalf("created result = %+v, fake should be returned unchanged", created)
	}
}

func TestServiceCursorRoundTripsAndBindsMessageConversation(t *testing.T) {
	now := time.Now().UTC().Truncate(time.Microsecond)
	repository := &fakeConversationRepository{conversationCursorResult: conversationdomain.ConversationCursorResult{
		Conversations: []conversationdomain.Conversation{
			{ID: 2, UpdatedAt: now},
			{ID: 1, UpdatedAt: now.Add(-time.Second)},
		},
		HasMore: true,
	}}
	service := NewService(repository)
	scope := testOwnerScope(t)
	first, err := service.ListCursor(context.Background(), scope, ConversationCursorInput{PageSize: 2})
	if err != nil || first.Cursor.Next == nil || !first.Cursor.HasMore {
		t.Fatalf("first conversation cursor page = %+v, %v", first, err)
	}
	repository.conversationCursorResult = conversationdomain.ConversationCursorResult{Conversations: []conversationdomain.Conversation{}}
	if _, err := service.ListCursor(context.Background(), scope, ConversationCursorInput{
		PageSize: 2, Cursor: *first.Cursor.Next,
	}); err != nil {
		t.Fatalf("continue conversation cursor: %v", err)
	}
	if repository.conversationCursorOptions.Cursor == nil || repository.conversationCursorOptions.Cursor.ID != 1 ||
		!repository.conversationCursorOptions.Cursor.UpdatedAt.Equal(now.Add(-time.Second)) {
		t.Fatalf("decoded conversation cursor = %+v", repository.conversationCursorOptions.Cursor)
	}

	repository.messageCursorResult = conversationdomain.MessageCursorResult{
		Messages: []conversationdomain.Message{{SequenceNo: 5}, {SequenceNo: 6}}, HasMore: true,
	}
	messageFirst, err := service.ListMessagesCursor(context.Background(), scope, MessageCursorInput{
		ConversationID: 9, PageSize: 2,
	})
	if err != nil || messageFirst.Cursor.Next == nil || !messageFirst.Cursor.HasMore {
		t.Fatalf("first message cursor page = %+v, %v", messageFirst, err)
	}
	repository.messageCursorResult = conversationdomain.MessageCursorResult{}
	if _, err := service.ListMessagesCursor(context.Background(), scope, MessageCursorInput{
		ConversationID: 9, PageSize: 2, Cursor: *messageFirst.Cursor.Next,
	}); err != nil {
		t.Fatalf("continue message cursor: %v", err)
	}
	if repository.messageCursorOptions.Cursor == nil || repository.messageCursorOptions.Cursor.BeforeSequenceNo != 5 {
		t.Fatalf("decoded message cursor = %+v", repository.messageCursorOptions.Cursor)
	}
	if _, err := service.ListMessagesCursor(context.Background(), scope, MessageCursorInput{
		ConversationID: 10, PageSize: 2, Cursor: *messageFirst.Cursor.Next,
	}); !errors.Is(err, ErrInvalidMessageCursor) {
		t.Fatalf("cross-conversation message cursor error = %v", err)
	}
	if _, err := service.ListCursor(context.Background(), scope, ConversationCursorInput{
		PageSize: 2, Cursor: *messageFirst.Cursor.Next,
	}); !errors.Is(err, ErrInvalidConversationCursor) {
		t.Fatalf("cross-type conversation cursor error = %v", err)
	}
}

func TestServiceRejectsMalformedCursors(t *testing.T) {
	service := NewService(&fakeConversationRepository{})
	scope := testOwnerScope(t)
	unknownField := base64.RawURLEncoding.EncodeToString([]byte(`{"v":1,"k":"conversation","t":"2026-01-01T00:00:00Z","id":1,"x":1}`))
	trailing := base64.RawURLEncoding.EncodeToString([]byte(`{"v":1,"k":"conversation","t":"2026-01-01T00:00:00Z","id":1}{}`))
	invalidPayload, _ := encodeCursor(cursorPayload{Version: 2, Kind: conversationCursorKind, UpdatedAt: "bad", ID: -1})
	invalidTime, _ := encodeCursor(cursorPayload{Version: 1, Kind: conversationCursorKind, UpdatedAt: "bad", ID: 1})
	invalidID, _ := encodeCursor(cursorPayload{Version: 1, Kind: conversationCursorKind, UpdatedAt: "2026-01-01T00:00:00Z", ID: -1})
	for _, cursor := range []string{
		"%%%", strings.Repeat("a", maxConversationCursorLength+1), unknownField, trailing,
		invalidPayload, invalidTime, invalidID,
	} {
		if _, err := service.ListCursor(context.Background(), scope, ConversationCursorInput{
			PageSize: 20, Cursor: cursor,
		}); !errors.Is(err, ErrInvalidConversationCursor) {
			t.Fatalf("malformed cursor %q error = %v", cursor, err)
		}
	}
}

func TestServiceUpdatesAndDeletesConversation(t *testing.T) {
	repository := &fakeConversationRepository{conversation: conversationdomain.Conversation{ID: 4}}
	service := NewService(repository)
	title := "  Renamed conversation  "
	status := conversationdomain.StatusArchived

	updated, err := service.Update(context.Background(), testOwnerScope(t), UpdateInput{
		ConversationID: 4, Title: &title, Status: &status,
	})
	if err != nil || updated.ID != 4 {
		t.Fatalf("Update() = %+v, %v", updated, err)
	}
	if repository.updateInput.Title == nil || *repository.updateInput.Title != "Renamed conversation" ||
		repository.updateInput.Status == nil || *repository.updateInput.Status != conversationdomain.StatusArchived {
		t.Fatalf("normalized update input = %+v", repository.updateInput)
	}
	if err := service.Delete(context.Background(), testOwnerScope(t), 4); err != nil || repository.deleteID != 4 {
		t.Fatalf("Delete() error/id = %v / %d", err, repository.deleteID)
	}
}

func TestServiceRejectsInvalidConversationUpdates(t *testing.T) {
	service := NewService(&fakeConversationRepository{})
	scope := testOwnerScope(t)
	blank := "   "
	oversized := strings.Repeat("x", 201)
	invalidStatus := conversationdomain.Status("deleted")
	tests := []struct {
		name  string
		input UpdateInput
		want  error
	}{
		{name: "empty", input: UpdateInput{ConversationID: 1}, want: ErrEmptyUpdate},
		{name: "blank title", input: UpdateInput{ConversationID: 1, Title: &blank}, want: ErrInvalidTitle},
		{name: "oversized title", input: UpdateInput{ConversationID: 1, Title: &oversized}, want: ErrInvalidTitle},
		{name: "invalid status", input: UpdateInput{ConversationID: 1, Status: &invalidStatus}, want: ErrInvalidStatus},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			if _, err := service.Update(context.Background(), scope, test.input); !errors.Is(err, test.want) {
				t.Fatalf("Update() error = %v, want %v", err, test.want)
			}
		})
	}
}

func TestServiceAppendNormalizesClientMessageIDAndPreservesContent(t *testing.T) {
	repository := &fakeConversationRepository{
		appendResult: conversationdomain.AppendUserMessageResult{
			Created: true,
			Message: conversationdomain.Message{
				ID: 1, ConversationID: 2, SequenceNo: 1,
				Role: conversationdomain.RoleUser, Status: conversationdomain.MessageStatusCompleted,
				Content: " hello ", CreatedAt: time.Now(), UpdatedAt: time.Now(),
			},
		},
	}
	service := NewService(repository)
	clientMessageID := "  client-1  "

	result, err := service.AppendUserMessage(context.Background(), testOwnerScope(t), AppendUserMessageInput{
		ConversationID: 2, Content: " hello ", ClientMessageID: &clientMessageID,
	})
	if err != nil {
		t.Fatalf("AppendUserMessage() error = %v", err)
	}
	if !result.Created || repository.appendInput.ClientMessageID == nil || *repository.appendInput.ClientMessageID != "client-1" {
		t.Fatalf("append result/input = %+v / %+v", result, repository.appendInput)
	}
	if repository.appendInput.Content != " hello " {
		t.Fatalf("content = %q, want original whitespace preserved", repository.appendInput.Content)
	}
}

func TestServiceStartTurnNormalizesClientMessageID(t *testing.T) {
	repository := &fakeConversationRepository{}
	service := NewService(repository)
	clientMessageID := "  client-turn  "

	_, err := service.StartTurn(context.Background(), testOwnerScope(t), StartTurnInput{
		ConversationID: 2, Content: "question", ClientMessageID: &clientMessageID,
	})
	if err != nil {
		t.Fatalf("StartTurn() error = %v", err)
	}
	if repository.startInput.ClientMessageID == nil || *repository.startInput.ClientMessageID != "client-turn" {
		t.Fatalf("normalized start input = %+v", repository.startInput)
	}
}

func TestServiceRejectsInvalidMessageAndPagination(t *testing.T) {
	service := NewService(&fakeConversationRepository{})
	scope := testOwnerScope(t)

	if _, err := service.AppendUserMessage(context.Background(), scope, AppendUserMessageInput{ConversationID: 1, Content: "   "}); !errors.Is(err, ErrInvalidMessage) {
		t.Fatalf("blank message error = %v, want ErrInvalidMessage", err)
	}
	if _, err := service.List(context.Background(), scope, ListInput{Page: 0, PageSize: 20}); !errors.Is(err, ErrInvalidPage) {
		t.Fatalf("invalid page error = %v, want ErrInvalidPage", err)
	}
	if _, err := service.List(context.Background(), scope, ListInput{Page: math.MaxInt64, PageSize: 20}); !errors.Is(err, ErrInvalidPage) {
		t.Fatalf("overflowing conversation page error = %v, want ErrInvalidPage", err)
	}
	if _, err := service.ListMessages(context.Background(), scope, MessageListInput{ConversationID: 1, Page: 1, PageSize: 101}); !errors.Is(err, ErrInvalidPageSize) {
		t.Fatalf("invalid page size error = %v, want ErrInvalidPageSize", err)
	}
	if _, err := service.ListMessages(context.Background(), scope, MessageListInput{
		ConversationID: 1, Page: math.MaxInt64, PageSize: 20,
	}); !errors.Is(err, ErrInvalidPage) {
		t.Fatalf("overflowing message page error = %v, want ErrInvalidPage", err)
	}
	if _, err := service.ListMessages(context.Background(), scope, MessageListInput{
		ConversationID: 1, Page: 1, PageSize: 20, Order: "newest",
	}); !errors.Is(err, ErrInvalidMessageOrder) {
		t.Fatalf("invalid message order error = %v, want ErrInvalidMessageOrder", err)
	}
}

func TestServiceListMessagesDefaultsAndPassesOrder(t *testing.T) {
	repository := &fakeConversationRepository{}
	service := NewService(repository)
	scope := testOwnerScope(t)

	if _, err := service.ListMessages(context.Background(), scope, MessageListInput{
		ConversationID: 1, Page: 1, PageSize: 20,
	}); err != nil {
		t.Fatalf("default ListMessages() error = %v", err)
	}
	if repository.messageOptions.Order != conversationdomain.MessageOrderAscending ||
		repository.messageOptions.Limit != 20 || repository.messageOptions.Offset != 0 {
		t.Fatalf("default message options = %+v", repository.messageOptions)
	}

	if _, err := service.ListMessages(context.Background(), scope, MessageListInput{
		ConversationID: 1, Page: 2, PageSize: 5, Order: conversationdomain.MessageOrderDescending,
	}); err != nil {
		t.Fatalf("descending ListMessages() error = %v", err)
	}
	if repository.messageOptions.Order != conversationdomain.MessageOrderDescending ||
		repository.messageOptions.Limit != 5 || repository.messageOptions.Offset != 5 {
		t.Fatalf("descending message options = %+v", repository.messageOptions)
	}
}

func TestServiceFailsOnlyRequestedUnqueuedAssistant(t *testing.T) {
	repository := &fakeConversationRepository{}
	service := NewService(repository)

	_, err := service.FailAssistantIfUnqueued(context.Background(), testOwnerScope(t), FailAssistantIfUnqueuedInput{
		ConversationID: 4, MessageID: 9, Content: "queue failed",
	})
	if err != nil {
		t.Fatalf("FailAssistantIfUnqueued() error = %v", err)
	}
	if repository.failInput.ConversationID != 4 || repository.failInput.MessageID != 9 || repository.failInput.Content != "queue failed" {
		t.Fatalf("fail input = %+v", repository.failInput)
	}
	if _, err := service.FailAssistantIfUnqueued(context.Background(), testOwnerScope(t), FailAssistantIfUnqueuedInput{
		ConversationID: 4, MessageID: 9, Content: "   ",
	}); !errors.Is(err, ErrInvalidMessage) {
		t.Fatalf("blank cleanup error = %v, want ErrInvalidMessage", err)
	}
}
