// Package agent defines durable Agent Run and Step facts.
package agent

import (
	"encoding/json"
	"math"
	"strings"
	"time"
)

const (
	// MaxPersistedSteps is the frozen Agent v0 bound: four tool calls and one final.
	MaxPersistedSteps = 5
	// MaxRunTotalTokens is the frozen Agent v0 aggregate token budget.
	MaxRunTotalTokens = 48_000
	// MaxAutomaticAttempts bounds lease recovery and transient requeues.
	MaxAutomaticAttempts = 2
)

// RunStatus is the stable lifecycle of a persisted Agent Run.
type RunStatus string

const (
	RunStatusQueued     RunStatus = "queued"
	RunStatusProcessing RunStatus = "processing"
	RunStatusSucceeded  RunStatus = "succeeded"
	RunStatusFailed     RunStatus = "failed"
	RunStatusCanceled   RunStatus = "canceled"
)

func (s RunStatus) IsValid() bool {
	switch s {
	case RunStatusQueued,
		RunStatusProcessing,
		RunStatusSucceeded,
		RunStatusFailed,
		RunStatusCanceled:
		return true
	default:
		return false
	}
}

func (s RunStatus) IsActive() bool {
	return s == RunStatusQueued || s == RunStatusProcessing
}

func (s RunStatus) IsTerminal() bool {
	return s == RunStatusSucceeded || s == RunStatusFailed || s == RunStatusCanceled
}

func (s RunStatus) CanCancel() bool { return s == RunStatusQueued }

// ResponseLanguage is the persisted user preference for the final answer.
type ResponseLanguage string

const (
	ResponseLanguageAuto    ResponseLanguage = "auto"
	ResponseLanguageChinese ResponseLanguage = "zh"
	ResponseLanguageEnglish ResponseLanguage = "en"
)

func (l ResponseLanguage) IsValid() bool {
	return l == ResponseLanguageAuto ||
		l == ResponseLanguageChinese ||
		l == ResponseLanguageEnglish
}

// IsResolved reports whether the language is safe to persist in a successful
// result. Auto is a request preference and must be resolved before execution.
func (l ResponseLanguage) IsResolved() bool {
	return l == ResponseLanguageChinese || l == ResponseLanguageEnglish
}

// ErrorCode is safe to persist and expose. Provider payloads, SQL and secrets
// must never be stored in this field.
type ErrorCode string

const (
	ErrorCodeNone                   ErrorCode = ""
	ErrorCodeExecutionFailed        ErrorCode = "agent_execution_failed"
	ErrorCodeTemporarilyUnavailable ErrorCode = "agent_temporarily_unavailable"
	ErrorCodeStepLimitExceeded      ErrorCode = "agent_step_limit_exceeded"
	ErrorCodeTokenBudgetExceeded    ErrorCode = "agent_token_budget_exceeded"
	ErrorCodeDeadlineExceeded       ErrorCode = "agent_deadline_exceeded"
	ErrorCodeInvalidToolCall        ErrorCode = "agent_invalid_tool_call"
	ErrorCodeToolFailed             ErrorCode = "agent_tool_failed"
	ErrorCodeWorkerInterrupted      ErrorCode = "agent_worker_interrupted"
)

func (c ErrorCode) IsValid() bool {
	switch c {
	case ErrorCodeNone,
		ErrorCodeExecutionFailed,
		ErrorCodeTemporarilyUnavailable,
		ErrorCodeStepLimitExceeded,
		ErrorCodeTokenBudgetExceeded,
		ErrorCodeDeadlineExceeded,
		ErrorCodeInvalidToolCall,
		ErrorCodeToolFailed,
		ErrorCodeWorkerInterrupted:
		return true
	default:
		return false
	}
}

// Budget is a server-owned hard execution boundary. Clients cannot enlarge it.
type Budget struct {
	MaxSteps       int
	MaxTotalTokens int
	DeadlineAt     time.Time
}

func (b Budget) IsValid() bool {
	return b.MaxSteps > 0 &&
		b.MaxSteps <= MaxPersistedSteps &&
		b.MaxTotalTokens > 0 && b.MaxTotalTokens <= MaxRunTotalTokens &&
		!b.DeadlineAt.IsZero()
}

// AllowsNextCall checks the budget before a Model or Tool call starts.
func (b Budget) AllowsNextCall(stepCount, totalTokens int, now time.Time) bool {
	return b.IsValid() &&
		stepCount >= 0 && stepCount < b.MaxSteps &&
		totalTokens >= 0 && totalTokens < b.MaxTotalTokens &&
		now.Before(b.DeadlineAt)
}

// Result stores the bounded final snapshot. JSON is reserved for the frozen
// answer/source shape introduced by the execution slice.
type Result struct {
	Answer           string
	ResponseLanguage ResponseLanguage
	Payload          json.RawMessage
	PromptTokens     int
	CompletionTokens int
	TotalTokens      int
}

func (r Result) IsValid() bool {
	return strings.TrimSpace(r.Answer) != "" &&
		r.ResponseLanguage.IsResolved() &&
		TokenUsageIsValid(r.PromptTokens, r.CompletionTokens, r.TotalTokens)
}

// TokenUsageIsValid rejects negative or overflowing token arithmetic.
func TokenUsageIsValid(promptTokens, completionTokens, totalTokens int) bool {
	return promptTokens >= 0 &&
		completionTokens >= 0 &&
		totalTokens >= 0 &&
		promptTokens <= math.MaxInt-completionTokens &&
		totalTokens == promptTokens+completionTokens
}

// Run is one persisted, owner-scoped Agent execution.
type Run struct {
	ID                  int64
	OwnerUserID         int64
	ConversationID      int64
	UserMessageID       int64
	AssistantMessageID  int64
	ClientRunID         string
	RequestedDocumentID *int64
	ResponseLanguage    ResponseLanguage
	Status              RunStatus
	StepCount           int
	Budget              Budget
	PromptTokens        int
	CompletionTokens    int
	TotalTokens         int
	AttemptCount        int
	NextAttemptAt       time.Time
	WorkerID            string
	LeaseToken          string
	LeaseExpiresAt      *time.Time
	HeartbeatAt         *time.Time
	ErrorCode           ErrorCode
	ErrorMessage        *string
	Result              *Result
	CreatedAt           time.Time
	UpdatedAt           time.Time
	StartedAt           *time.Time
	CompletedAt         *time.Time
}

func (r Run) IsActive() bool { return r.Status.IsActive() }

func (r Run) IsTerminal() bool { return r.Status.IsTerminal() }

func (r Run) CanCancel() bool { return r.Status == RunStatusQueued }

// AdmissionLimits bounds persisted queued work. Processing runs already own an
// execution slot and do not count toward the waiting budget.
type AdmissionLimits struct {
	MaxQueuedRunsPerOwner int
	MaxQueuedRunsGlobal   int
}

func (l AdmissionLimits) IsValid() bool {
	return l.MaxQueuedRunsPerOwner > 0 &&
		l.MaxQueuedRunsGlobal >= l.MaxQueuedRunsPerOwner
}

// SchedulingPolicy defines owner-fair claims.
type SchedulingPolicy struct {
	MaxInFlightPerOwner         int
	MaxBorrowedInFlightPerOwner int
	StarvationThreshold         time.Duration
}

func (p SchedulingPolicy) IsValid() bool {
	return p.MaxInFlightPerOwner > 0 &&
		p.MaxBorrowedInFlightPerOwner >= p.MaxInFlightPerOwner &&
		p.StarvationThreshold > 0
}

// LeasePolicy identifies one Agent Worker and its lease duration.
type LeasePolicy struct {
	WorkerID      string
	LeaseDuration time.Duration
	MaxAttempts   int
}

func (p LeasePolicy) IsValid() bool {
	return strings.TrimSpace(p.WorkerID) != "" &&
		p.LeaseDuration > 0 &&
		p.MaxAttempts == MaxAutomaticAttempts
}

// QueueStats is safe observability data for the durable queue.
type QueueStats struct {
	QueuedCount             int64
	ReadyQueuedCount        int64
	ProcessingCount         int64
	MaxOwnerProcessingCount int64
	OldestReadyWait         time.Duration
}

func (s QueueStats) IsValid() bool {
	return s.QueuedCount >= 0 &&
		s.ReadyQueuedCount >= 0 &&
		s.ReadyQueuedCount <= s.QueuedCount &&
		s.ProcessingCount >= 0 &&
		s.MaxOwnerProcessingCount >= 0 &&
		s.MaxOwnerProcessingCount <= s.ProcessingCount &&
		s.OldestReadyWait >= 0
}
