package answer

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"strings"
	"time"

	accessdomain "rag-reasoning-platform/backend/internal/domain/access"
)

var (
	// ErrAnswerCapacityCoordinationUnavailable 表示 Redis 执行槽位协调不可用。
	ErrAnswerCapacityCoordinationUnavailable = errors.New(
		"answer capacity coordination is unavailable",
	)
	ErrDistributedAnswerDependencies = errors.New(
		"distributed answer capacity dependencies must be provided",
	)
	ErrDistributedAnswerConfiguration = errors.New(
		"distributed answer capacity configuration is invalid",
	)
	ErrProviderCapacityKeyConfiguration = errors.New(
		"provider capacity key configuration is invalid",
	)
)

// DistributedCapacityStore 是问答应用层依赖的 Redis 容量协调最小端口。
type DistributedCapacityStore interface {
	AcquireCapacity(
		context.Context,
		[]string,
		[]int,
		time.Duration,
	) (string, []int, bool, error)
	ReleaseCapacity(context.Context, []string, string) error
}

// DistributedAnswerConfig 定义跨进程问答容量键、租约和等待策略。
type DistributedAnswerConfig struct {
	Namespace              string
	Provider               string
	Model                  string
	MaxConcurrencyGlobal   int
	MaxConcurrencyPerOwner int
	LeaseTTL               time.Duration
	RetryInterval          time.Duration
	WaitTimeout            time.Duration
}

// ProviderCapacityKeyBuilder derives the byte-compatible global and owner Redis
// keys for one provider/model identity. It is shared with Agent assembly so a
// rolling upgrade cannot split capacity across two key spaces.
type ProviderCapacityKeyBuilder func(ownerUserID int64) ([]string, error)

// NewProviderCapacityKeyBuilder preserves the original Answer key algorithm:
// SHA-256(provider + NUL + model), first 12 bytes, under the legacy
// {namespace-answer-hash} Redis hash tag.
func NewProviderCapacityKeyBuilder(
	namespace string,
	provider string,
	model string,
) (ProviderCapacityKeyBuilder, error) {
	if strings.TrimSpace(namespace) == "" || strings.TrimSpace(provider) == "" ||
		strings.TrimSpace(model) == "" {
		return nil, ErrProviderCapacityKeyConfiguration
	}
	identity := sha256.Sum256([]byte(provider + "\x00" + model))
	hashTag := fmt.Sprintf(
		"{%s-answer-%s}",
		namespace,
		hex.EncodeToString(identity[:12]),
	)
	return func(ownerUserID int64) ([]string, error) {
		if ownerUserID <= 0 {
			return nil, accessdomain.ErrInvalidOwnerScope
		}
		return []string{
			hashTag + ":global",
			fmt.Sprintf("%s:owner:%d", hashTag, ownerUserID),
		}, nil
	}, nil
}

// DistributedService 在完整问答链路前申请 Redis 全局和 Owner 执行槽位。
// 本地 ConcurrentService 仍负责公平等待；本类型只解决多个进程合计容量。
type DistributedService struct {
	next   Answerer
	store  DistributedCapacityStore
	events AnswerAdmissionEventObserver
	config DistributedAnswerConfig
	keys   ProviderCapacityKeyBuilder
}

var _ Answerer = (*DistributedService)(nil)

// NewDistributedService 创建跨进程问答容量包装器。
func NewDistributedService(
	next Answerer,
	store DistributedCapacityStore,
	events AnswerAdmissionEventObserver,
	config DistributedAnswerConfig,
) (*DistributedService, error) {
	if next == nil || store == nil || events == nil {
		return nil, ErrDistributedAnswerDependencies
	}
	if strings.TrimSpace(config.Namespace) == "" ||
		strings.TrimSpace(config.Provider) == "" ||
		strings.TrimSpace(config.Model) == "" ||
		config.MaxConcurrencyGlobal <= 0 ||
		config.MaxConcurrencyPerOwner <= 0 ||
		config.MaxConcurrencyPerOwner > config.MaxConcurrencyGlobal ||
		config.LeaseTTL <= 0 ||
		config.RetryInterval <= 0 ||
		config.WaitTimeout <= 0 {
		return nil, ErrDistributedAnswerConfiguration
	}

	keys, err := NewProviderCapacityKeyBuilder(config.Namespace, config.Provider, config.Model)
	if err != nil {
		return nil, ErrDistributedAnswerConfiguration
	}
	return &DistributedService{
		next: next, store: store, events: events, config: config, keys: keys,
	}, nil
}

// Answer 申请跨进程容量后执行下游，并保证正常返回路径释放租约。
func (s *DistributedService) Answer(
	ctx context.Context,
	scope accessdomain.OwnerScope,
	input Input,
) (output Output, err error) {
	if !scope.IsValid() {
		return Output{}, accessdomain.ErrInvalidOwnerScope
	}

	keys, keyErr := s.keys(scope.OwnerUserID())
	if keyErr != nil {
		return Output{}, accessdomain.ErrInvalidOwnerScope
	}
	limits := []int{
		s.config.MaxConcurrencyGlobal,
		s.config.MaxConcurrencyPerOwner,
	}

	waitStartedAt := time.Now()
	token, counts, acquireErr := s.acquire(ctx, keys, limits)
	waitDuration := time.Since(waitStartedAt)
	if acquireErr != nil {
		outcome := AnswerAdmissionOutcomeCapacityTimeout
		if errors.Is(acquireErr, ErrAnswerCapacityCoordinationUnavailable) {
			outcome = AnswerAdmissionOutcomeCoordinationError
		} else if errors.Is(acquireErr, context.Canceled) ||
			errors.Is(acquireErr, context.DeadlineExceeded) {
			outcome = AnswerAdmissionOutcomeCanceled
		}
		s.observe(
			ctx,
			AnswerDistributedAdmissionEventRejected,
			outcome,
			waitDuration,
			0,
			counts,
			acquireErr,
		)
		return Output{}, acquireErr
	}

	s.observe(
		ctx,
		AnswerDistributedAdmissionEventAdmitted,
		"",
		waitDuration,
		0,
		counts,
		nil,
	)
	executionStartedAt := time.Now()
	defer func() {
		releaseErr := s.store.ReleaseCapacity(context.Background(), keys, token)
		outcome := AnswerAdmissionOutcomeSucceeded
		if err != nil {
			outcome = AnswerAdmissionOutcomeDownstreamError
		}
		if releaseErr != nil {
			outcome = AnswerAdmissionOutcomeCoordinationError
		}
		releasedCounts := append([]int(nil), counts...)
		for index := range releasedCounts {
			releasedCounts[index] = max(releasedCounts[index]-1, 0)
		}
		s.observe(
			ctx,
			AnswerDistributedAdmissionEventReleased,
			outcome,
			waitDuration,
			time.Since(executionStartedAt),
			releasedCounts,
			releaseErr,
		)
	}()

	return s.next.Answer(ctx, scope, input)
}

func (s *DistributedService) acquire(
	ctx context.Context,
	keys []string,
	limits []int,
) (string, []int, error) {
	deadline := time.Now().Add(s.config.WaitTimeout)
	if localDeadline, ok := answerAdmissionDeadlineFromContext(ctx); ok &&
		localDeadline.Before(deadline) {
		deadline = localDeadline
	}

	var lastCounts []int
	for {
		if err := ctx.Err(); err != nil {
			return "", lastCounts, err
		}
		if !time.Now().Before(deadline) {
			return "", lastCounts, ErrAnswerCapacityExhausted
		}
		token, counts, acquired, err := s.store.AcquireCapacity(
			ctx,
			keys,
			limits,
			s.config.LeaseTTL,
		)
		lastCounts = counts
		if err != nil {
			return "", lastCounts, errors.Join(
				ErrAnswerCapacityExhausted,
				fmt.Errorf(
					"%w: %v",
					ErrAnswerCapacityCoordinationUnavailable,
					err,
				),
			)
		}
		if acquired {
			return token, counts, nil
		}

		waitDuration := min(s.config.RetryInterval, time.Until(deadline))
		retryTimer := time.NewTimer(waitDuration)
		select {
		case <-ctx.Done():
			retryTimer.Stop()
			return "", lastCounts, ctx.Err()
		case <-retryTimer.C:
		}
	}
}

func (s *DistributedService) observe(
	ctx context.Context,
	eventType AnswerAdmissionEventType,
	outcome AnswerAdmissionOutcome,
	waitDuration time.Duration,
	executionDuration time.Duration,
	counts []int,
	err error,
) {
	globalCount := 0
	ownerCount := 0
	if len(counts) > 0 {
		globalCount = counts[0]
	}
	if len(counts) > 1 {
		ownerCount = counts[1]
	}
	s.events.ObserveAnswerAdmissionEvent(ctx, AnswerAdmissionEvent{
		Type:                eventType,
		Outcome:             outcome,
		WaitDuration:        waitDuration,
		ExecutionDuration:   executionDuration,
		InFlight:            globalCount,
		MaxConcurrency:      s.config.MaxConcurrencyGlobal,
		OwnerInFlight:       ownerCount,
		OwnerMaxConcurrency: s.config.MaxConcurrencyPerOwner,
		Err:                 err,
	})
}
