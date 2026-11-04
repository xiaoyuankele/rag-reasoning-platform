package conversation

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"math"
	"strings"
	"time"

	accessdomain "rag-reasoning-platform/backend/internal/domain/access"
	conversationdomain "rag-reasoning-platform/backend/internal/domain/conversation"
)

const (
	DefaultTitle                            = "New conversation"
	MaxTitleLength                    int64 = 200
	MaxMessageBytes                   int   = 100_000
	MaxClientIDLen                    int   = 128
	DefaultPageSize                   int64 = 20
	MaxPageSize                       int64 = 100
	DefaultConversationCursorPageSize int64 = 20
	DefaultMessageCursorPageSize      int64 = 50
	maxConversationCursorLength             = 512
)

var (
	ErrInvalidID                 = errors.New("conversation ID must be a positive integer")
	ErrInvalidPage               = errors.New("page must be positive")
	ErrInvalidPageSize           = errors.New("page size must be between 1 and 100")
	ErrInvalidMessageOrder       = errors.New("message order must be asc or desc")
	ErrInvalidTitle              = errors.New("conversation title must be between 1 and 200 characters")
	ErrInvalidStatus             = errors.New("conversation status must be active or archived")
	ErrEmptyUpdate               = errors.New("conversation update must include title or status")
	ErrInvalidConversationCursor = errors.New("conversation cursor is invalid")
	ErrInvalidMessageCursor      = errors.New("conversation message cursor is invalid")
	ErrInvalidMessage            = errors.New("conversation message must be between 1 and 100000 bytes")
	ErrInvalidClientMessageID    = errors.New("client message ID must be between 1 and 128 characters")
)

type Service struct {
	repository conversationdomain.Repository
}

func NewService(repository conversationdomain.Repository) *Service {
	if repository == nil {
		panic("New conversation service requires a non-nil repository")
	}
	return &Service{repository: repository}
}

type CreateInput struct {
	Title string
}

type UpdateInput struct {
	ConversationID int64
	Title          *string
	Status         *conversationdomain.Status
}

type ListInput struct {
	Page     int64
	PageSize int64
}

type ListOutput struct {
	Conversations []conversationdomain.Conversation
	Page          int64
	PageSize      int64
	Total         int64
	TotalPages    int64
}

type CursorPage struct {
	Next    *string
	HasMore bool
}

type ConversationCursorInput struct {
	PageSize int64
	Cursor   string
}

type ConversationCursorOutput struct {
	Conversations []conversationdomain.Conversation
	Cursor        CursorPage
}

type MessageListInput struct {
	ConversationID int64
	Page           int64
	PageSize       int64
	Order          conversationdomain.MessageOrder
}

type MessageListOutput struct {
	ConversationID int64
	Messages       []conversationdomain.Message
	Page           int64
	PageSize       int64
	Total          int64
	TotalPages     int64
}

type MessageCursorInput struct {
	ConversationID int64
	PageSize       int64
	Cursor         string
}

type MessageCursorOutput struct {
	ConversationID int64
	Messages       []conversationdomain.Message
	Cursor         CursorPage
}

type cursorPayload struct {
	Version        int    `json:"v"`
	Kind           string `json:"k"`
	UpdatedAt      string `json:"t,omitempty"`
	ID             int64  `json:"id,omitempty"`
	ConversationID int64  `json:"cid,omitempty"`
	SequenceNo     int64  `json:"seq,omitempty"`
}

const (
	cursorVersion          = 1
	conversationCursorKind = "conversation"
	messageCursorKind      = "message"
)

type AppendUserMessageInput struct {
	ConversationID  int64
	Content         string
	ClientMessageID *string
}

type StartTurnInput struct {
	ConversationID  int64
	Content         string
	ClientMessageID *string
}

type UpdateAssistantMessageInput struct {
	ConversationID int64
	MessageID      int64
	Content        string
	Status         conversationdomain.MessageStatus
}

type FailAssistantIfUnqueuedInput struct {
	ConversationID int64
	MessageID      int64
	Content        string
}

func (s *Service) Create(
	ctx context.Context,
	scope accessdomain.OwnerScope,
	input CreateInput,
) (conversationdomain.Conversation, error) {
	if !scope.IsValid() {
		return conversationdomain.Conversation{}, accessdomain.ErrInvalidOwnerScope
	}
	title := strings.TrimSpace(input.Title)
	if title == "" {
		title = DefaultTitle
	}
	if int64(len([]rune(title))) > MaxTitleLength {
		return conversationdomain.Conversation{}, ErrInvalidTitle
	}

	created, err := s.repository.CreateConversation(ctx, scope, conversationdomain.CreateInput{Title: title})
	if err != nil {
		return conversationdomain.Conversation{}, fmt.Errorf("create conversation: %w", err)
	}
	return created, nil
}

func (s *Service) List(
	ctx context.Context,
	scope accessdomain.OwnerScope,
	input ListInput,
) (ListOutput, error) {
	if input.Page <= 0 {
		return ListOutput{}, ErrInvalidPage
	}
	if input.PageSize <= 0 || input.PageSize > MaxPageSize {
		return ListOutput{}, ErrInvalidPageSize
	}
	if input.Page-1 > math.MaxInt64/input.PageSize {
		return ListOutput{}, ErrInvalidPage
	}

	result, err := s.repository.ListConversations(ctx, scope, conversationdomain.ListOptions{
		Limit:  input.PageSize,
		Offset: (input.Page - 1) * input.PageSize,
	})
	if err != nil {
		return ListOutput{}, fmt.Errorf("list conversations: %w", err)
	}
	return ListOutput{
		Conversations: result.Conversations,
		Page:          input.Page,
		PageSize:      input.PageSize,
		Total:         result.Total,
		TotalPages:    totalPages(result.Total, input.PageSize),
	}, nil
}

func (s *Service) ListCursor(
	ctx context.Context,
	scope accessdomain.OwnerScope,
	input ConversationCursorInput,
) (ConversationCursorOutput, error) {
	if input.PageSize <= 0 || input.PageSize > MaxPageSize {
		return ConversationCursorOutput{}, ErrInvalidPageSize
	}
	cursor, err := decodeConversationCursor(input.Cursor)
	if err != nil {
		return ConversationCursorOutput{}, err
	}
	result, err := s.repository.ListConversationsCursor(ctx, scope, conversationdomain.ConversationCursorOptions{
		Limit: input.PageSize, Cursor: cursor,
	})
	if err != nil {
		return ConversationCursorOutput{}, fmt.Errorf("list conversations by cursor: %w", err)
	}
	if result.Conversations == nil {
		result.Conversations = make([]conversationdomain.Conversation, 0)
	}
	var next *string
	if result.HasMore && len(result.Conversations) > 0 {
		last := result.Conversations[len(result.Conversations)-1]
		encoded, err := encodeCursor(cursorPayload{
			Version: cursorVersion, Kind: conversationCursorKind,
			UpdatedAt: last.UpdatedAt.UTC().Format(time.RFC3339Nano), ID: last.ID,
		})
		if err != nil {
			return ConversationCursorOutput{}, fmt.Errorf("encode next conversation cursor: %w", err)
		}
		next = &encoded
	}
	return ConversationCursorOutput{
		Conversations: result.Conversations,
		Cursor:        CursorPage{Next: next, HasMore: result.HasMore},
	}, nil
}

func (s *Service) Get(
	ctx context.Context,
	scope accessdomain.OwnerScope,
	conversationID int64,
) (conversationdomain.Conversation, error) {
	if conversationID <= 0 {
		return conversationdomain.Conversation{}, ErrInvalidID
	}
	conversation, err := s.repository.GetConversation(ctx, scope, conversationID)
	if err != nil {
		return conversationdomain.Conversation{}, fmt.Errorf("get conversation: %w", err)
	}
	return conversation, nil
}

func (s *Service) Update(
	ctx context.Context,
	scope accessdomain.OwnerScope,
	input UpdateInput,
) (conversationdomain.Conversation, error) {
	if !scope.IsValid() {
		return conversationdomain.Conversation{}, accessdomain.ErrInvalidOwnerScope
	}
	if input.ConversationID <= 0 {
		return conversationdomain.Conversation{}, ErrInvalidID
	}
	if input.Title == nil && input.Status == nil {
		return conversationdomain.Conversation{}, ErrEmptyUpdate
	}

	var title *string
	if input.Title != nil {
		normalized := strings.TrimSpace(*input.Title)
		if normalized == "" || int64(len([]rune(normalized))) > MaxTitleLength {
			return conversationdomain.Conversation{}, ErrInvalidTitle
		}
		title = &normalized
	}
	if input.Status != nil && !input.Status.IsValid() {
		return conversationdomain.Conversation{}, ErrInvalidStatus
	}

	updated, err := s.repository.UpdateConversation(ctx, scope, conversationdomain.UpdateInput{
		ConversationID: input.ConversationID,
		Title:          title,
		Status:         input.Status,
	})
	if err != nil {
		return conversationdomain.Conversation{}, fmt.Errorf("update conversation: %w", err)
	}
	return updated, nil
}

func (s *Service) Delete(
	ctx context.Context,
	scope accessdomain.OwnerScope,
	conversationID int64,
) error {
	if !scope.IsValid() {
		return accessdomain.ErrInvalidOwnerScope
	}
	if conversationID <= 0 {
		return ErrInvalidID
	}
	if err := s.repository.DeleteConversation(ctx, scope, conversationID); err != nil {
		return fmt.Errorf("delete conversation: %w", err)
	}
	return nil
}

func (s *Service) ListMessages(
	ctx context.Context,
	scope accessdomain.OwnerScope,
	input MessageListInput,
) (MessageListOutput, error) {
	if input.ConversationID <= 0 {
		return MessageListOutput{}, ErrInvalidID
	}
	if input.Page <= 0 {
		return MessageListOutput{}, ErrInvalidPage
	}
	if input.PageSize <= 0 || input.PageSize > MaxPageSize {
		return MessageListOutput{}, ErrInvalidPageSize
	}
	if input.Page-1 > math.MaxInt64/input.PageSize {
		return MessageListOutput{}, ErrInvalidPage
	}
	order := input.Order
	if order == "" {
		order = conversationdomain.MessageOrderAscending
	}
	if !order.IsValid() {
		return MessageListOutput{}, ErrInvalidMessageOrder
	}

	result, err := s.repository.ListMessages(ctx, scope, input.ConversationID, conversationdomain.ListOptions{
		Limit:  input.PageSize,
		Offset: (input.Page - 1) * input.PageSize,
		Order:  order,
	})
	if err != nil {
		return MessageListOutput{}, fmt.Errorf("list conversation messages: %w", err)
	}
	return MessageListOutput{
		ConversationID: input.ConversationID,
		Messages:       result.Messages,
		Page:           input.Page,
		PageSize:       input.PageSize,
		Total:          result.Total,
		TotalPages:     totalPages(result.Total, input.PageSize),
	}, nil
}

func (s *Service) ListMessagesCursor(
	ctx context.Context,
	scope accessdomain.OwnerScope,
	input MessageCursorInput,
) (MessageCursorOutput, error) {
	if input.ConversationID <= 0 {
		return MessageCursorOutput{}, ErrInvalidID
	}
	if input.PageSize <= 0 || input.PageSize > MaxPageSize {
		return MessageCursorOutput{}, ErrInvalidPageSize
	}
	cursor, err := decodeMessageCursor(input.Cursor, input.ConversationID)
	if err != nil {
		return MessageCursorOutput{}, err
	}
	result, err := s.repository.ListMessagesCursor(ctx, scope, input.ConversationID, conversationdomain.MessageCursorOptions{
		Limit: input.PageSize, Cursor: cursor,
	})
	if err != nil {
		return MessageCursorOutput{}, fmt.Errorf("list conversation messages by cursor: %w", err)
	}
	if result.Messages == nil {
		result.Messages = make([]conversationdomain.Message, 0)
	}
	var next *string
	if result.HasMore && len(result.Messages) > 0 {
		encoded, err := encodeCursor(cursorPayload{
			Version: cursorVersion, Kind: messageCursorKind, ConversationID: input.ConversationID,
			SequenceNo: result.Messages[0].SequenceNo,
		})
		if err != nil {
			return MessageCursorOutput{}, fmt.Errorf("encode next message cursor: %w", err)
		}
		next = &encoded
	}
	return MessageCursorOutput{
		ConversationID: input.ConversationID,
		Messages:       result.Messages,
		Cursor:         CursorPage{Next: next, HasMore: result.HasMore},
	}, nil
}

func encodeCursor(payload cursorPayload) (string, error) {
	encoded, err := json.Marshal(payload)
	if err != nil {
		return "", err
	}
	return base64.RawURLEncoding.EncodeToString(encoded), nil
}

func decodeCursor(raw string) (cursorPayload, error) {
	if raw == "" {
		return cursorPayload{}, nil
	}
	if len(raw) > maxConversationCursorLength {
		return cursorPayload{}, errors.New("cursor exceeds maximum length")
	}
	decoded, err := base64.RawURLEncoding.DecodeString(raw)
	if err != nil || base64.RawURLEncoding.EncodeToString(decoded) != raw {
		return cursorPayload{}, errors.New("cursor is not canonical base64url")
	}
	decoder := json.NewDecoder(bytes.NewReader(decoded))
	decoder.DisallowUnknownFields()
	var payload cursorPayload
	if err := decoder.Decode(&payload); err != nil {
		return cursorPayload{}, err
	}
	var trailing any
	if err := decoder.Decode(&trailing); !errors.Is(err, io.EOF) {
		if err == nil {
			return cursorPayload{}, errors.New("cursor contains multiple JSON values")
		}
		return cursorPayload{}, err
	}
	if payload.Version != cursorVersion {
		return cursorPayload{}, errors.New("cursor version is unsupported")
	}
	return payload, nil
}

func decodeConversationCursor(raw string) (*conversationdomain.ConversationCursor, error) {
	if raw == "" {
		return nil, nil
	}
	payload, err := decodeCursor(raw)
	if err != nil || payload.Kind != conversationCursorKind || payload.ID <= 0 ||
		payload.UpdatedAt == "" || payload.ConversationID != 0 || payload.SequenceNo != 0 {
		return nil, ErrInvalidConversationCursor
	}
	updatedAt, err := time.Parse(time.RFC3339Nano, payload.UpdatedAt)
	if err != nil || !updatedAt.After(time.Unix(0, 0)) {
		return nil, ErrInvalidConversationCursor
	}
	return &conversationdomain.ConversationCursor{UpdatedAt: updatedAt, ID: payload.ID}, nil
}

func decodeMessageCursor(raw string, conversationID int64) (*conversationdomain.MessageCursor, error) {
	if raw == "" {
		return nil, nil
	}
	payload, err := decodeCursor(raw)
	if err != nil || payload.Kind != messageCursorKind || payload.SequenceNo <= 0 ||
		payload.ConversationID <= 0 || payload.ConversationID != conversationID ||
		payload.UpdatedAt != "" || payload.ID != 0 {
		return nil, ErrInvalidMessageCursor
	}
	return &conversationdomain.MessageCursor{BeforeSequenceNo: payload.SequenceNo}, nil
}

func (s *Service) AppendUserMessage(
	ctx context.Context,
	scope accessdomain.OwnerScope,
	input AppendUserMessageInput,
) (conversationdomain.AppendUserMessageResult, error) {
	if input.ConversationID <= 0 {
		return conversationdomain.AppendUserMessageResult{}, ErrInvalidID
	}
	if len(strings.TrimSpace(input.Content)) == 0 || len(input.Content) > MaxMessageBytes {
		return conversationdomain.AppendUserMessageResult{}, ErrInvalidMessage
	}

	clientMessageID := normalizeClientMessageID(input.ClientMessageID)
	if clientMessageID != nil && len([]rune(*clientMessageID)) > MaxClientIDLen {
		return conversationdomain.AppendUserMessageResult{}, ErrInvalidClientMessageID
	}
	if input.ClientMessageID != nil && clientMessageID == nil {
		return conversationdomain.AppendUserMessageResult{}, ErrInvalidClientMessageID
	}

	result, err := s.repository.AppendUserMessage(ctx, scope, conversationdomain.AppendUserMessageInput{
		ConversationID:  input.ConversationID,
		Content:         input.Content,
		ClientMessageID: clientMessageID,
	})
	if err != nil {
		return conversationdomain.AppendUserMessageResult{}, fmt.Errorf("append user conversation message: %w", err)
	}
	return result, nil
}

// StartTurn 原子写入用户消息和助手 generating 占位消息。
func (s *Service) StartTurn(
	ctx context.Context,
	scope accessdomain.OwnerScope,
	input StartTurnInput,
) (conversationdomain.StartTurnResult, error) {
	if input.ConversationID <= 0 {
		return conversationdomain.StartTurnResult{}, ErrInvalidID
	}
	if len(strings.TrimSpace(input.Content)) == 0 || len(input.Content) > MaxMessageBytes {
		return conversationdomain.StartTurnResult{}, ErrInvalidMessage
	}

	clientMessageID := normalizeClientMessageID(input.ClientMessageID)
	if clientMessageID != nil && len([]rune(*clientMessageID)) > MaxClientIDLen {
		return conversationdomain.StartTurnResult{}, ErrInvalidClientMessageID
	}
	if input.ClientMessageID != nil && clientMessageID == nil {
		return conversationdomain.StartTurnResult{}, ErrInvalidClientMessageID
	}

	result, err := s.repository.StartTurn(ctx, scope, conversationdomain.StartTurnInput{
		ConversationID:  input.ConversationID,
		Content:         input.Content,
		ClientMessageID: clientMessageID,
	})
	if err != nil {
		return conversationdomain.StartTurnResult{}, fmt.Errorf("start conversation turn: %w", err)
	}
	return result, nil
}

func (s *Service) UpdateAssistantMessage(
	ctx context.Context,
	scope accessdomain.OwnerScope,
	input UpdateAssistantMessageInput,
) (conversationdomain.Message, error) {
	if input.ConversationID <= 0 || input.MessageID <= 0 {
		return conversationdomain.Message{}, ErrInvalidID
	}
	if strings.TrimSpace(input.Content) == "" || !input.Status.IsValid() || input.Status == conversationdomain.MessageStatusGenerating {
		return conversationdomain.Message{}, ErrInvalidMessage
	}
	message, err := s.repository.UpdateAssistantMessage(ctx, scope, conversationdomain.UpdateAssistantMessageInput{
		ConversationID: input.ConversationID,
		MessageID:      input.MessageID,
		Content:        input.Content,
		Status:         input.Status,
	})
	if err != nil {
		return conversationdomain.Message{}, fmt.Errorf("update assistant message: %w", err)
	}
	return message, nil
}

// FailAssistantIfUnqueued 只收尾仍未关联 Answer Job 的 generating 占位消息。
func (s *Service) FailAssistantIfUnqueued(
	ctx context.Context,
	scope accessdomain.OwnerScope,
	input FailAssistantIfUnqueuedInput,
) (conversationdomain.Message, error) {
	if input.ConversationID <= 0 || input.MessageID <= 0 {
		return conversationdomain.Message{}, ErrInvalidID
	}
	if strings.TrimSpace(input.Content) == "" {
		return conversationdomain.Message{}, ErrInvalidMessage
	}
	message, err := s.repository.FailAssistantIfUnqueued(ctx, scope, conversationdomain.FailAssistantIfUnqueuedInput{
		ConversationID: input.ConversationID,
		MessageID:      input.MessageID,
		Content:        input.Content,
	})
	if err != nil {
		return conversationdomain.Message{}, fmt.Errorf("fail unqueued assistant message: %w", err)
	}
	return message, nil
}

func normalizeClientMessageID(value *string) *string {
	if value == nil {
		return nil
	}
	normalized := strings.TrimSpace(*value)
	if normalized == "" {
		return nil
	}
	return &normalized
}

func totalPages(total, pageSize int64) int64 {
	if total == 0 {
		return 0
	}
	pages := total / pageSize
	if total%pageSize != 0 {
		pages++
	}
	return pages
}
