package agent

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"time"
	"unicode/utf8"

	accessdomain "rag-reasoning-platform/backend/internal/domain/access"
	agentdomain "rag-reasoning-platform/backend/internal/domain/agent"
)

var (
	ErrInvalidModelDecision        = errors.New("agent model decision is invalid")
	ErrModelTemporarilyUnavailable = errors.New("agent model is temporarily unavailable")
	ErrModelRequestRejected        = errors.New("agent model request was rejected")
	ErrAgentContextUnavailable     = errors.New("agent context is unavailable")
)

const (
	maxContextHistoryMessages = 32
	maxSystemInstructionRunes = 10_000
	maxContextMessageRunes    = 20_000
	maxCurrentQuestionRunes   = 100_000
	maxAgentContextRunes      = 200_000
)

// ModelMessage is bounded conversation context. It never contains hidden
// reasoning, a System Prompt, or a Provider response.
type ModelMessage struct {
	Role    string
	Content string
}

// AgentContext is the M3.1-bounded context supplied by a trusted Application port.
type AgentContext struct {
	SystemInstruction string
	History           []ModelMessage
	CurrentQuestion   string
}

func (c AgentContext) IsValid() bool {
	if strings.TrimSpace(c.SystemInstruction) == "" || strings.TrimSpace(c.CurrentQuestion) == "" ||
		!utf8.ValidString(c.SystemInstruction) || !utf8.ValidString(c.CurrentQuestion) ||
		utf8.RuneCountInString(c.SystemInstruction) > maxSystemInstructionRunes ||
		utf8.RuneCountInString(c.CurrentQuestion) > maxCurrentQuestionRunes ||
		len(c.History) > maxContextHistoryMessages {
		return false
	}
	totalRunes := utf8.RuneCountInString(c.SystemInstruction) + utf8.RuneCountInString(c.CurrentQuestion)
	for _, message := range c.History {
		messageRunes := utf8.RuneCountInString(message.Content)
		if (message.Role != "user" && message.Role != "assistant") ||
			strings.TrimSpace(message.Content) == "" || !utf8.ValidString(message.Content) ||
			messageRunes > maxContextMessageRunes {
			return false
		}
		totalRunes += messageRunes
	}
	return totalRunes <= maxAgentContextRunes
}

// ContextProvider loads already-bounded history and the current question.
// Implementations must apply the supplied OwnerScope.
type ContextProvider interface {
	LoadAgentContext(
		ctx context.Context,
		scope accessdomain.OwnerScope,
		run agentdomain.Run,
	) (AgentContext, error)
}

type TokenUsage struct {
	PromptTokens     int
	CompletionTokens int
	TotalTokens      int
}

func (u TokenUsage) IsValid() bool {
	return agentdomain.TokenUsageIsValid(u.PromptTokens, u.CompletionTokens, u.TotalTokens)
}

type ModelToolCall struct {
	Name      string
	Arguments json.RawMessage
}

type ModelFinalAnswer struct {
	Text         string
	EvidenceRefs []string
}

// ModelDecision is a provider-neutral tagged union. Exactly one branch is set.
type ModelDecision struct {
	ToolCall *ModelToolCall
	Final    *ModelFinalAnswer
	Usage    TokenUsage
}

func (d ModelDecision) IsValid() bool {
	if !d.Usage.IsValid() || (d.ToolCall == nil) == (d.Final == nil) {
		return false
	}
	if d.ToolCall != nil {
		return strings.TrimSpace(d.ToolCall.Name) != "" && len(d.ToolCall.Arguments) > 0
	}
	return strings.TrimSpace(d.Final.Text) != ""
}

type RemainingBudget struct {
	Steps      int
	Tokens     int
	DeadlineAt time.Time
}

type ModelObservation struct {
	SequenceNo int
	ToolName   string
	Arguments  json.RawMessage
	Result     json.RawMessage
}

type ModelRequest struct {
	RunID               int64
	RequestedDocumentID *int64
	ResponseLanguage    agentdomain.ResponseLanguage
	Context             AgentContext
	Tools               []ToolDefinition
	Observations        []ModelObservation
	Remaining           RemainingBudget
}

// AgentModel returns one Tool Call or one Final Answer per invocation.
type AgentModel interface {
	Decide(ctx context.Context, request ModelRequest) (ModelDecision, error)
}
