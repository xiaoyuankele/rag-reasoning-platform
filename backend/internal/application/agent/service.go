package agent

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"

	accessdomain "rag-reasoning-platform/backend/internal/domain/access"
	agentdomain "rag-reasoning-platform/backend/internal/domain/agent"
)

const (
	MaxContentBytes      = 100_000
	MaxClientRunIDLength = 128
)

var (
	ErrServiceDependencies     = errors.New("agent service dependencies must be provided")
	ErrInvalidRunPolicy        = errors.New("agent run policy is invalid")
	ErrInvalidRunID            = errors.New("agent run ID must be positive")
	ErrInvalidConversationID   = errors.New("conversation ID must be positive")
	ErrInvalidContent          = errors.New("agent content must be between 1 and 100000 bytes")
	ErrInvalidClientRunID      = errors.New("client run ID must be between 1 and 128 characters")
	ErrInvalidDocumentID       = errors.New("agent document ID must be positive")
	ErrInvalidResponseLanguage = errors.New("agent response language must be auto, zh, or en")
)

type RunPolicy struct {
	MaxSteps       int
	MaxTotalTokens int
	RunTimeout     time.Duration
}

func (p RunPolicy) IsValid() bool {
	return p.MaxSteps > 0 &&
		p.MaxSteps <= agentdomain.MaxPersistedSteps &&
		p.MaxTotalTokens > 0 && p.MaxTotalTokens <= agentdomain.MaxRunTotalTokens &&
		p.RunTimeout > 0
}

type QueueInput struct {
	ConversationID      int64
	Content             string
	ClientRunID         string
	RequestedDocumentID *int64
	ResponseLanguage    agentdomain.ResponseLanguage
}

type Service struct {
	runs   ScopedRepository
	steps  StepRepository
	policy RunPolicy
	now    func() time.Time
}

func NewService(
	runs ScopedRepository,
	steps StepRepository,
	policy RunPolicy,
) (*Service, error) {
	if runs == nil || steps == nil {
		return nil, ErrServiceDependencies
	}
	if !policy.IsValid() {
		return nil, ErrInvalidRunPolicy
	}
	return &Service{runs: runs, steps: steps, policy: policy, now: time.Now}, nil
}

func (s *Service) Queue(
	ctx context.Context,
	scope accessdomain.OwnerScope,
	input QueueInput,
) (QueueResult, error) {
	if !scope.IsValid() {
		return QueueResult{}, accessdomain.ErrInvalidOwnerScope
	}
	if input.ConversationID <= 0 {
		return QueueResult{}, ErrInvalidConversationID
	}
	if strings.TrimSpace(input.Content) == "" || len(input.Content) > MaxContentBytes {
		return QueueResult{}, ErrInvalidContent
	}
	clientRunID := strings.TrimSpace(input.ClientRunID)
	if clientRunID == "" || len([]rune(clientRunID)) > MaxClientRunIDLength {
		return QueueResult{}, ErrInvalidClientRunID
	}
	if input.RequestedDocumentID != nil && *input.RequestedDocumentID <= 0 {
		return QueueResult{}, ErrInvalidDocumentID
	}
	language := agentdomain.ResponseLanguage(strings.ToLower(strings.TrimSpace(string(input.ResponseLanguage))))
	if language == "" {
		language = agentdomain.ResponseLanguageAuto
	}
	if !language.IsValid() {
		return QueueResult{}, ErrInvalidResponseLanguage
	}

	result, err := s.runs.CreateRun(ctx, scope, CreateRunInput{
		ConversationID:      input.ConversationID,
		Content:             input.Content,
		ClientRunID:         clientRunID,
		RequestedDocumentID: input.RequestedDocumentID,
		ResponseLanguage:    language,
		Budget: agentdomain.Budget{
			MaxSteps:       s.policy.MaxSteps,
			MaxTotalTokens: s.policy.MaxTotalTokens,
			DeadlineAt:     s.now().Add(s.policy.RunTimeout),
		},
	})
	if err != nil {
		return QueueResult{}, fmt.Errorf("create agent run: %w", err)
	}
	return result, nil
}

func (s *Service) GetByID(
	ctx context.Context,
	scope accessdomain.OwnerScope,
	runID int64,
) (RunSnapshot, error) {
	if !scope.IsValid() {
		return RunSnapshot{}, accessdomain.ErrInvalidOwnerScope
	}
	if runID <= 0 {
		return RunSnapshot{}, ErrInvalidRunID
	}
	snapshot, err := s.runs.GetRunByID(ctx, scope, runID)
	if err != nil {
		return RunSnapshot{}, fmt.Errorf("get agent run: %w", err)
	}
	return snapshot, nil
}

func (s *Service) GetActiveConversationRun(
	ctx context.Context,
	scope accessdomain.OwnerScope,
	conversationID int64,
) (RunSnapshot, error) {
	if !scope.IsValid() {
		return RunSnapshot{}, accessdomain.ErrInvalidOwnerScope
	}
	if conversationID <= 0 {
		return RunSnapshot{}, ErrInvalidConversationID
	}
	snapshot, err := s.runs.GetActiveRunByConversationID(ctx, scope, conversationID)
	if err != nil {
		return RunSnapshot{}, fmt.Errorf("get active conversation agent run: %w", err)
	}
	return snapshot, nil
}

func (s *Service) GetLatestConversationRun(
	ctx context.Context,
	scope accessdomain.OwnerScope,
	conversationID int64,
) (RunSnapshot, error) {
	if !scope.IsValid() {
		return RunSnapshot{}, accessdomain.ErrInvalidOwnerScope
	}
	if conversationID <= 0 {
		return RunSnapshot{}, ErrInvalidConversationID
	}
	snapshot, err := s.runs.GetLatestRunByConversationID(ctx, scope, conversationID)
	if err != nil {
		return RunSnapshot{}, fmt.Errorf("get latest conversation agent run: %w", err)
	}
	return snapshot, nil
}

func (s *Service) ListSteps(
	ctx context.Context,
	scope accessdomain.OwnerScope,
	runID int64,
) ([]agentdomain.Step, error) {
	if !scope.IsValid() {
		return nil, accessdomain.ErrInvalidOwnerScope
	}
	if runID <= 0 {
		return nil, ErrInvalidRunID
	}
	steps, err := s.steps.ListSteps(ctx, scope, runID)
	if err != nil {
		return nil, fmt.Errorf("list agent steps: %w", err)
	}
	if steps == nil {
		steps = make([]agentdomain.Step, 0)
	}
	return steps, nil
}

func (s *Service) Cancel(
	ctx context.Context,
	scope accessdomain.OwnerScope,
	runID int64,
) (RunSnapshot, error) {
	if !scope.IsValid() {
		return RunSnapshot{}, accessdomain.ErrInvalidOwnerScope
	}
	if runID <= 0 {
		return RunSnapshot{}, ErrInvalidRunID
	}
	snapshot, err := s.runs.CancelRun(ctx, scope, runID)
	if err != nil {
		return RunSnapshot{}, fmt.Errorf("cancel agent run: %w", err)
	}
	return snapshot, nil
}
