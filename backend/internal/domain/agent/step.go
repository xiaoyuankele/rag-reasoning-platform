package agent

import (
	"encoding/json"
	"time"
)

type StepKind string

const (
	StepKindToolCall StepKind = "tool_call"
	StepKindFinal    StepKind = "final"
)

func (k StepKind) IsValid() bool {
	return k == StepKindToolCall || k == StepKindFinal
}

type StepStatus string

const (
	StepStatusPending   StepStatus = "pending"
	StepStatusRunning   StepStatus = "running"
	StepStatusSucceeded StepStatus = "succeeded"
	StepStatusFailed    StepStatus = "failed"
)

func (s StepStatus) IsValid() bool {
	return s == StepStatusPending ||
		s == StepStatusRunning ||
		s == StepStatusSucceeded ||
		s == StepStatusFailed
}

func (s StepStatus) IsTerminal() bool {
	return s == StepStatusSucceeded || s == StepStatusFailed
}

// Step stores one bounded, auditable action. Arguments and Result must already
// be size-bounded and redacted by Application code before persistence.
type Step struct {
	ID               int64
	RunID            int64
	SequenceNo       int
	Kind             StepKind
	Status           StepStatus
	ToolName         *string
	Arguments        json.RawMessage
	Result           json.RawMessage
	ErrorCode        ErrorCode
	ErrorMessage     *string
	PromptTokens     int
	CompletionTokens int
	TotalTokens      int
	CreatedAt        time.Time
	StartedAt        *time.Time
	CompletedAt      *time.Time
}
