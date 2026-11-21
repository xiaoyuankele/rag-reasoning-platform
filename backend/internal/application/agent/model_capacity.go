package agent

import (
	"context"
	"errors"
	"strings"
	"time"
)

var (
	ErrModelCapacityDependencies  = errors.New("agent model capacity dependencies must be provided")
	ErrModelCapacityConfiguration = errors.New("agent model capacity configuration is invalid")
)

type ModelCapacityStore interface {
	AcquireCapacity(context.Context, []string, []int, time.Duration) (string, []int, bool, error)
	ReleaseCapacity(context.Context, []string, string) error
}

type ModelCapacityConfig struct {
	MaxConcurrencyGlobal   int
	MaxConcurrencyPerOwner int
	LeaseTTL               time.Duration
	RetryInterval          time.Duration
	WaitTimeout            time.Duration
}

func (c ModelCapacityConfig) IsValid() bool {
	return c.MaxConcurrencyGlobal > 0 && c.MaxConcurrencyPerOwner > 0 &&
		c.MaxConcurrencyPerOwner <= c.MaxConcurrencyGlobal &&
		c.LeaseTTL > 0 && c.RetryInterval > 0 && c.WaitTimeout > 0
}

type ModelCapacityEventType string

const (
	ModelCapacityEventAdmitted ModelCapacityEventType = "agent_model_capacity_admitted"
	ModelCapacityEventRejected ModelCapacityEventType = "agent_model_capacity_rejected"
	ModelCapacityEventReleased ModelCapacityEventType = "agent_model_capacity_released"
)

type ModelCapacityOutcome string

const (
	ModelCapacityOutcomeSucceeded       ModelCapacityOutcome = "succeeded"
	ModelCapacityOutcomeCapacity        ModelCapacityOutcome = "capacity_exhausted"
	ModelCapacityOutcomeCoordination    ModelCapacityOutcome = "coordination_error"
	ModelCapacityOutcomeCanceled        ModelCapacityOutcome = "canceled"
	ModelCapacityOutcomeInvalidRequest  ModelCapacityOutcome = "invalid_request"
	ModelCapacityOutcomeDownstreamError ModelCapacityOutcome = "downstream_error"
	ModelCapacityOutcomeReleaseError    ModelCapacityOutcome = "release_error"
)

type ModelSafeErrorCategory string

const (
	ModelSafeErrorNone         ModelSafeErrorCategory = ""
	ModelSafeErrorTemporary    ModelSafeErrorCategory = "temporary"
	ModelSafeErrorRejected     ModelSafeErrorCategory = "rejected"
	ModelSafeErrorInvalid      ModelSafeErrorCategory = "invalid"
	ModelSafeErrorCanceled     ModelSafeErrorCategory = "canceled"
	ModelSafeErrorCoordination ModelSafeErrorCategory = "coordination"
	ModelSafeErrorInternal     ModelSafeErrorCategory = "internal"
)

type ModelCapacityEvent struct {
	Type                ModelCapacityEventType
	Outcome             ModelCapacityOutcome
	WaitDuration        time.Duration
	ExecutionDuration   time.Duration
	InFlight            int
	MaxConcurrency      int
	OwnerInFlight       int
	OwnerMaxConcurrency int
	ErrorCategory       ModelSafeErrorCategory
}

type ModelCapacityEventObserver interface {
	ObserveModelCapacityEvent(context.Context, ModelCapacityEvent)
}

// CapacityModel coordinates global and per-owner Provider slots before
// delegating to the real AgentModel. Owner identity is used only to derive
// Redis keys and is never included in events.
type CapacityModel struct {
	next    AgentModel
	store   ModelCapacityStore
	observe ModelCapacityEventObserver
	keys    func(int64) ([]string, error)
	config  ModelCapacityConfig
}

var _ AgentModel = (*CapacityModel)(nil)

func NewCapacityModel(
	next AgentModel,
	store ModelCapacityStore,
	observer ModelCapacityEventObserver,
	keyBuilder func(int64) ([]string, error),
	config ModelCapacityConfig,
) (*CapacityModel, error) {
	if next == nil || store == nil || observer == nil || keyBuilder == nil {
		return nil, ErrModelCapacityDependencies
	}
	if !config.IsValid() {
		return nil, ErrModelCapacityConfiguration
	}
	return &CapacityModel{next: next, store: store, observe: observer, keys: keyBuilder, config: config}, nil
}

func (m *CapacityModel) Decide(
	ctx context.Context,
	request ModelRequest,
) (decision ModelDecision, err error) {
	waitStartedAt := time.Now()
	if request.OwnerUserID <= 0 {
		m.emit(ctx, ModelCapacityEventRejected, ModelCapacityOutcomeInvalidRequest, time.Since(waitStartedAt), 0, nil, ModelSafeErrorInvalid)
		return ModelDecision{}, ErrModelRequestRejected
	}
	keys, keyErr := m.keys(request.OwnerUserID)
	if keyErr != nil || !validModelCapacityKeys(keys) {
		m.emit(ctx, ModelCapacityEventRejected, ModelCapacityOutcomeInvalidRequest, time.Since(waitStartedAt), 0, nil, ModelSafeErrorInvalid)
		return ModelDecision{}, ErrModelRequestRejected
	}
	token, counts, acquireErr := m.acquire(ctx, keys)
	waitDuration := time.Since(waitStartedAt)
	if acquireErr != nil {
		outcome := ModelCapacityOutcomeCapacity
		category := ModelSafeErrorTemporary
		if errors.Is(acquireErr, context.Canceled) || errors.Is(acquireErr, context.DeadlineExceeded) {
			outcome = ModelCapacityOutcomeCanceled
			category = ModelSafeErrorCanceled
		} else if errors.Is(acquireErr, errModelCapacityCoordination) {
			outcome = ModelCapacityOutcomeCoordination
			category = ModelSafeErrorCoordination
		}
		m.emit(ctx, ModelCapacityEventRejected, outcome, waitDuration, 0, counts, category)
		if errors.Is(acquireErr, context.Canceled) || errors.Is(acquireErr, context.DeadlineExceeded) {
			return ModelDecision{}, acquireErr
		}
		return ModelDecision{}, ErrModelTemporarilyUnavailable
	}
	m.emit(ctx, ModelCapacityEventAdmitted, ModelCapacityOutcomeSucceeded, waitDuration, 0, counts, ModelSafeErrorNone)

	executionStartedAt := time.Now()
	defer func() {
		releaseErr := m.store.ReleaseCapacity(context.Background(), keys, token)
		outcome := ModelCapacityOutcomeSucceeded
		category := modelSafeErrorCategory(err)
		if err != nil {
			outcome = ModelCapacityOutcomeDownstreamError
		}
		if releaseErr != nil {
			outcome = ModelCapacityOutcomeReleaseError
			category = ModelSafeErrorCoordination
		}
		releasedCounts := append([]int(nil), counts...)
		if releaseErr == nil {
			for index := range releasedCounts {
				releasedCounts[index] = max(releasedCounts[index]-1, 0)
			}
		}
		m.emit(ctx, ModelCapacityEventReleased, outcome, waitDuration, time.Since(executionStartedAt), releasedCounts, category)
	}()

	return m.next.Decide(ctx, request)
}

var errModelCapacityCoordination = errors.New("agent model capacity coordination failed")

func (m *CapacityModel) acquire(
	ctx context.Context,
	keys []string,
) (string, []int, error) {
	waitContext, cancel := context.WithTimeout(ctx, m.config.WaitTimeout)
	defer cancel()
	limits := []int{m.config.MaxConcurrencyGlobal, m.config.MaxConcurrencyPerOwner}
	var lastCounts []int
	for {
		if err := waitContext.Err(); err != nil {
			if ctxErr := ctx.Err(); ctxErr != nil {
				return "", lastCounts, ctxErr
			}
			return "", lastCounts, ErrModelTemporarilyUnavailable
		}
		token, counts, acquired, err := m.store.AcquireCapacity(waitContext, keys, limits, m.config.LeaseTTL)
		lastCounts = append([]int(nil), counts...)
		if err != nil {
			if ctxErr := ctx.Err(); ctxErr != nil {
				return "", lastCounts, ctxErr
			}
			if waitContext.Err() != nil {
				return "", lastCounts, ErrModelTemporarilyUnavailable
			}
			return "", lastCounts, errModelCapacityCoordination
		}
		if acquired {
			if strings.TrimSpace(token) == "" {
				return "", lastCounts, errModelCapacityCoordination
			}
			return token, lastCounts, nil
		}
		retryTimer := time.NewTimer(m.config.RetryInterval)
		select {
		case <-waitContext.Done():
			retryTimer.Stop()
			if ctxErr := ctx.Err(); ctxErr != nil {
				return "", lastCounts, ctxErr
			}
			return "", lastCounts, ErrModelTemporarilyUnavailable
		case <-retryTimer.C:
		}
	}
}

func (m *CapacityModel) emit(
	ctx context.Context,
	eventType ModelCapacityEventType,
	outcome ModelCapacityOutcome,
	waitDuration time.Duration,
	executionDuration time.Duration,
	counts []int,
	category ModelSafeErrorCategory,
) {
	globalCount := 0
	ownerCount := 0
	if len(counts) > 0 {
		globalCount = counts[0]
	}
	if len(counts) > 1 {
		ownerCount = counts[1]
	}
	m.observe.ObserveModelCapacityEvent(ctx, ModelCapacityEvent{
		Type: eventType, Outcome: outcome,
		WaitDuration: waitDuration, ExecutionDuration: executionDuration,
		InFlight: globalCount, MaxConcurrency: m.config.MaxConcurrencyGlobal,
		OwnerInFlight: ownerCount, OwnerMaxConcurrency: m.config.MaxConcurrencyPerOwner,
		ErrorCategory: category,
	})
}

func validModelCapacityKeys(keys []string) bool {
	return len(keys) == 2 && strings.TrimSpace(keys[0]) != "" && strings.TrimSpace(keys[1]) != "" &&
		keys[0] != keys[1]
}

func modelSafeErrorCategory(err error) ModelSafeErrorCategory {
	switch {
	case err == nil:
		return ModelSafeErrorNone
	case errors.Is(err, context.Canceled), errors.Is(err, context.DeadlineExceeded):
		return ModelSafeErrorCanceled
	case errors.Is(err, ErrModelTemporarilyUnavailable):
		return ModelSafeErrorTemporary
	case errors.Is(err, ErrModelRequestRejected):
		return ModelSafeErrorRejected
	case errors.Is(err, ErrInvalidModelDecision):
		return ModelSafeErrorInvalid
	default:
		return ModelSafeErrorInternal
	}
}
