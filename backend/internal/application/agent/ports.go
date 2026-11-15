// Package agent defines Agent v0 application use cases and persistence ports.
package agent

import (
	"context"
	"encoding/json"
	"errors"
	"time"

	accessdomain "rag-reasoning-platform/backend/internal/domain/access"
	agentdomain "rag-reasoning-platform/backend/internal/domain/agent"
	conversationdomain "rag-reasoning-platform/backend/internal/domain/conversation"
)

var (
	ErrRunNotFound                 = errors.New("agent run not found")
	ErrNoQueuedRun                 = errors.New("no queued agent run")
	ErrNoPendingStep               = errors.New("no pending agent step")
	ErrRunNotProcessing            = errors.New("agent run is not processing")
	ErrRunProcessingCannotCancel   = errors.New("processing agent run cannot be canceled")
	ErrRunTerminalCannotCancel     = errors.New("terminal agent run cannot be canceled")
	ErrOwnerQueueCapacity          = errors.New("agent owner queue capacity exhausted")
	ErrGlobalQueueCapacity         = errors.New("agent global queue capacity exhausted")
	ErrConversationExecutionActive = errors.New("conversation already has an active execution")
	ErrIdempotencyConflict         = errors.New("agent run idempotency conflict")
	ErrRunLeaseLost                = errors.New("agent run lease lost")
	ErrStepLimitExceeded           = errors.New("agent step limit exceeded")
	ErrTokenBudgetExceeded         = errors.New("agent token budget exceeded")
	ErrRunDeadlineExceeded         = errors.New("agent run deadline exceeded")
	ErrFinalStepAlreadyPersisted   = errors.New("agent final step already persisted")
	ErrInvalidStep                 = errors.New("agent step is invalid")
	ErrInvalidAdmissionLimits      = errors.New("agent run admission limits are invalid")
	ErrInvalidSchedulingPolicy     = errors.New("agent run scheduling policy is invalid")
	ErrInvalidLeasePolicy          = errors.New("agent run lease policy is invalid")
)

// CreateRunInput is the normalized, server-budgeted persistence command.
type CreateRunInput struct {
	ConversationID      int64
	Content             string
	ClientRunID         string
	RequestedDocumentID *int64
	ResponseLanguage    agentdomain.ResponseLanguage
	Budget              agentdomain.Budget
}

// QueueResult returns the atomic conversation turn and Agent Run creation.
type QueueResult struct {
	Run              agentdomain.Run
	UserMessage      conversationdomain.Message
	AssistantMessage conversationdomain.Message
	Created          bool
}

// ScopedRepository is the authenticated user-facing Agent Run port.
type ScopedRepository interface {
	CreateRun(
		ctx context.Context,
		scope accessdomain.OwnerScope,
		input CreateRunInput,
	) (QueueResult, error)
	GetRunByID(
		ctx context.Context,
		scope accessdomain.OwnerScope,
		runID int64,
	) (agentdomain.Run, error)
	GetActiveRunByConversationID(
		ctx context.Context,
		scope accessdomain.OwnerScope,
		conversationID int64,
	) (agentdomain.Run, error)
	GetLatestRunByConversationID(
		ctx context.Context,
		scope accessdomain.OwnerScope,
		conversationID int64,
	) (agentdomain.Run, error)
	CancelRun(
		ctx context.Context,
		scope accessdomain.OwnerScope,
		runID int64,
	) (agentdomain.Run, error)
}

// WorkerRepository owns durable queue claims and fenced Run transitions.
type WorkerRepository interface {
	ClaimNextRun(ctx context.Context) (agentdomain.Run, error)
	GetQueueStats(ctx context.Context) (agentdomain.QueueStats, error)
	RenewRunLease(ctx context.Context, runID int64, leaseToken string) error
	RequeueRun(
		ctx context.Context,
		runID int64,
		leaseToken string,
		nextAttemptAt time.Time,
		errorCode agentdomain.ErrorCode,
		errorMessage string,
	) error
	MarkRunSucceeded(
		ctx context.Context,
		runID int64,
		leaseToken string,
		result agentdomain.Result,
	) error
	MarkRunFailed(
		ctx context.Context,
		runID int64,
		leaseToken string,
		errorCode agentdomain.ErrorCode,
		errorMessage string,
	) error
	RequeueExpiredRuns(
		ctx context.Context,
		errorCode agentdomain.ErrorCode,
		errorMessage string,
	) (int64, error)
}

type StartStepInput struct {
	Kind      agentdomain.StepKind
	ToolName  *string
	Arguments json.RawMessage
}

type CompleteStepInput struct {
	Result           json.RawMessage
	PromptTokens     int
	CompletionTokens int
	TotalTokens      int
}

// FinalStepResult is the auditable JSON shape shared by the final Step and Run snapshot.
type FinalStepResult struct {
	Answer           string                       `json:"answer"`
	ResponseLanguage agentdomain.ResponseLanguage `json:"response_language"`
	Payload          json.RawMessage              `json:"payload,omitempty"`
}

type FailStepInput struct {
	ErrorCode        agentdomain.ErrorCode
	ErrorMessage     string
	PromptTokens     int
	CompletionTokens int
	TotalTokens      int
}

// StepRepository persists bounded actions under the current Run lease.
type StepRepository interface {
	ListSteps(
		ctx context.Context,
		scope accessdomain.OwnerScope,
		runID int64,
	) ([]agentdomain.Step, error)
	ListStepsForWorker(
		ctx context.Context,
		runID int64,
		leaseToken string,
	) ([]agentdomain.Step, error)
	ClaimPendingStep(
		ctx context.Context,
		runID int64,
		leaseToken string,
	) (agentdomain.Step, error)
	StartStep(
		ctx context.Context,
		runID int64,
		leaseToken string,
		input StartStepInput,
	) (agentdomain.Step, error)
	MarkStepSucceeded(
		ctx context.Context,
		runID int64,
		stepID int64,
		leaseToken string,
		input CompleteStepInput,
	) (agentdomain.Step, error)
	MarkStepFailed(
		ctx context.Context,
		runID int64,
		stepID int64,
		leaseToken string,
		input FailStepInput,
	) (agentdomain.Step, error)
}
