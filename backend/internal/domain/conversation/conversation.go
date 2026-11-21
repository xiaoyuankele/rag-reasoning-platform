// Package conversation 定义个人用户多轮对话的持久化事实与边界。
package conversation

import (
	"context"
	"errors"
	"time"

	accessdomain "rag-reasoning-platform/backend/internal/domain/access"
)

var (
	// ErrNotFound 表示会话或消息所属的会话不存在，或者不属于当前用户。
	ErrNotFound = errors.New("conversation not found")
	// ErrDuplicateClientMessage 表示同一个客户端消息标识已经写入。
	ErrDuplicateClientMessage = errors.New("conversation client message already exists")
	// ErrArchived 表示会话已归档，不能再追加用户消息或启动新回答。
	ErrArchived = errors.New("conversation is archived")
	// ErrAnswerActive 表示会话仍有 queued/processing 回答任务，不能删除。
	ErrAnswerActive = errors.New("conversation has an active answer job")
)

type Status string

const (
	StatusActive   Status = "active"
	StatusArchived Status = "archived"
)

func (s Status) IsValid() bool {
	return s == StatusActive || s == StatusArchived
}

type Role string

const (
	RoleUser      Role = "user"
	RoleAssistant Role = "assistant"
)

func (r Role) IsValid() bool {
	return r == RoleUser || r == RoleAssistant
}

type MessageStatus string

const (
	MessageStatusCompleted   MessageStatus = "completed"
	MessageStatusGenerating  MessageStatus = "generating"
	MessageStatusFailed      MessageStatus = "failed"
	MessageStatusInterrupted MessageStatus = "interrupted"
)

const (
	AssistantGeneratingPlaceholder      = "正在生成回答…"
	AssistantInterruptedBeforeQueueText = "回答任务在入队前中断。"
)

func (s MessageStatus) IsValid() bool {
	return s == MessageStatusCompleted ||
		s == MessageStatusGenerating ||
		s == MessageStatusFailed ||
		s == MessageStatusInterrupted
}

type MessageOrder string

const (
	MessageOrderAscending  MessageOrder = "asc"
	MessageOrderDescending MessageOrder = "desc"
)

func (o MessageOrder) IsValid() bool {
	return o == MessageOrderAscending || o == MessageOrderDescending
}

type Conversation struct {
	ID            int64
	OwnerUserID   int64
	Title         string
	Status        Status
	LastMessageAt *time.Time
	CreatedAt     time.Time
	UpdatedAt     time.Time
}

type Message struct {
	ID              int64
	ConversationID  int64
	SequenceNo      int64
	Role            Role
	Content         string
	Status          MessageStatus
	ClientMessageID *string
	CreatedAt       time.Time
	UpdatedAt       time.Time
}

type ListOptions struct {
	Limit  int64
	Offset int64
	Order  MessageOrder
}

type ConversationListResult struct {
	Conversations []Conversation
	Total         int64
}

type ConversationCursor struct {
	UpdatedAt time.Time
	ID        int64
}

type ConversationCursorOptions struct {
	Limit  int64
	Cursor *ConversationCursor
}

type ConversationCursorResult struct {
	Conversations []Conversation
	HasMore       bool
}

type MessageListResult struct {
	Messages []Message
	Total    int64
}

type MessageCursor struct {
	BeforeSequenceNo int64
}

type MessageCursorOptions struct {
	Limit  int64
	Cursor *MessageCursor
}

type MessageCursorResult struct {
	Messages []Message
	HasMore  bool
}

type CreateInput struct {
	Title string
}

type UpdateInput struct {
	ConversationID int64
	Title          *string
	Status         *Status
}

type AppendUserMessageInput struct {
	ConversationID  int64
	Content         string
	ClientMessageID *string
}

type AppendUserMessageResult struct {
	Message Message
	Created bool
}

// StartTurnInput 描述一轮需要进入 RAG 生成的用户消息。
// Repository 会在同一事务中创建 user 消息和 generating 状态的 assistant 占位消息。
type StartTurnInput struct {
	ConversationID  int64
	Content         string
	ClientMessageID *string
}

type StartTurnResult struct {
	UserMessage      Message
	AssistantMessage Message
	Created          bool
}

type UpdateAssistantMessageInput struct {
	ConversationID int64
	MessageID      int64
	Content        string
	Status         MessageStatus
}

type FailAssistantIfUnqueuedInput struct {
	ConversationID int64
	MessageID      int64
	Content        string
}

type Repository interface {
	CreateConversation(
		ctx context.Context,
		scope accessdomain.OwnerScope,
		input CreateInput,
	) (Conversation, error)
	ListConversations(
		ctx context.Context,
		scope accessdomain.OwnerScope,
		options ListOptions,
	) (ConversationListResult, error)
	ListConversationsCursor(
		ctx context.Context,
		scope accessdomain.OwnerScope,
		options ConversationCursorOptions,
	) (ConversationCursorResult, error)
	GetConversation(
		ctx context.Context,
		scope accessdomain.OwnerScope,
		conversationID int64,
	) (Conversation, error)
	UpdateConversation(
		ctx context.Context,
		scope accessdomain.OwnerScope,
		input UpdateInput,
	) (Conversation, error)
	DeleteConversation(
		ctx context.Context,
		scope accessdomain.OwnerScope,
		conversationID int64,
	) error
	ListMessages(
		ctx context.Context,
		scope accessdomain.OwnerScope,
		conversationID int64,
		options ListOptions,
	) (MessageListResult, error)
	ListMessagesCursor(
		ctx context.Context,
		scope accessdomain.OwnerScope,
		conversationID int64,
		options MessageCursorOptions,
	) (MessageCursorResult, error)
	AppendUserMessage(
		ctx context.Context,
		scope accessdomain.OwnerScope,
		input AppendUserMessageInput,
	) (AppendUserMessageResult, error)
	StartTurn(
		ctx context.Context,
		scope accessdomain.OwnerScope,
		input StartTurnInput,
	) (StartTurnResult, error)
	UpdateAssistantMessage(
		ctx context.Context,
		scope accessdomain.OwnerScope,
		input UpdateAssistantMessageInput,
	) (Message, error)
	FailAssistantIfUnqueued(
		ctx context.Context,
		scope accessdomain.OwnerScope,
		input FailAssistantIfUnqueuedInput,
	) (Message, error)
}
