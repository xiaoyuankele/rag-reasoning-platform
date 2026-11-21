package embedding

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"strings"
	"time"

	embeddingdomain "rag-reasoning-platform/backend/internal/domain/embedding"
)

var (
	// ErrEmbeddingProviderCoordinationUnavailable 表示 Redis 容量协调暂时不可用。
	// 它会同时包装 ErrEmbeddingProviderCapacityExhausted，使现有 HTTP 和异步
	// 重试语义保持不变，同时允许日志和测试识别真正的协调故障。
	ErrEmbeddingProviderCoordinationUnavailable = errors.New(
		"embedding provider capacity coordination is unavailable",
	)
	ErrDistributedEmbeddingProviderGateDependencies = errors.New(
		"distributed embedding provider gate dependencies must be provided",
	)
	ErrDistributedEmbeddingProviderGateConfiguration = errors.New(
		"distributed embedding provider gate configuration is invalid",
	)
)

// DistributedCapacityStore 是 Application 使用跨进程执行租约所需的最小端口。
// Infrastructure 的 Redis 实现会自动满足该接口。
type DistributedCapacityStore interface {
	AcquireCapacity(
		context.Context,
		[]string,
		[]int,
		time.Duration,
	) (string, []int, bool, error)
	ReleaseCapacity(context.Context, []string, string) error
}

// DistributedEmbeddingProviderGateConfig 定义跨进程 Embedding 容量键与等待策略。
type DistributedEmbeddingProviderGateConfig struct {
	Namespace              string
	Provider               string
	Model                  string
	Dimensions             int
	Origin                 EmbeddingProviderCallOrigin
	ProviderMaxConcurrency int
	OriginMaxConcurrency   int
	LeaseTTL               time.Duration
	RetryInterval          time.Duration
	WaitTimeout            time.Duration
}

// DistributedGatedEmbedder 在真正调用远程 Provider 前申请 Redis 共享槽位。
// 它与进程内 GatedEmbedder 叠加使用：本地层负责公平和快速保护，Redis 层负责
// 所有 API/Worker 进程合计不超过配置上限。
type DistributedGatedEmbedder struct {
	next   embeddingdomain.Embedder
	store  DistributedCapacityStore
	events EmbeddingProviderAdmissionObserver
	config DistributedEmbeddingProviderGateConfig
	keys   []string
	limits []int
}

var _ embeddingdomain.Embedder = (*DistributedGatedEmbedder)(nil)

// NewDistributedGatedEmbedder 创建跨进程 Embedding Provider 闸门。
func NewDistributedGatedEmbedder(
	next embeddingdomain.Embedder,
	store DistributedCapacityStore,
	events EmbeddingProviderAdmissionObserver,
	config DistributedEmbeddingProviderGateConfig,
) (*DistributedGatedEmbedder, error) {
	if next == nil || store == nil || events == nil {
		return nil, ErrDistributedEmbeddingProviderGateDependencies
	}
	if strings.TrimSpace(config.Namespace) == "" ||
		strings.TrimSpace(config.Provider) == "" ||
		strings.TrimSpace(config.Model) == "" ||
		config.Dimensions <= 0 ||
		config.ProviderMaxConcurrency <= 0 ||
		config.OriginMaxConcurrency <= 0 ||
		config.OriginMaxConcurrency > config.ProviderMaxConcurrency ||
		config.LeaseTTL <= 0 ||
		config.RetryInterval <= 0 ||
		config.WaitTimeout < 0 ||
		(config.Origin != EmbeddingProviderCallOriginWorker &&
			config.Origin != EmbeddingProviderCallOriginOnline) {
		return nil, ErrDistributedEmbeddingProviderGateConfiguration
	}

	identity := sha256.Sum256([]byte(fmt.Sprintf(
		"%s\x00%s\x00%d",
		config.Provider,
		config.Model,
		config.Dimensions,
	)))
	hashTag := fmt.Sprintf(
		"{%s-embedding-%s}",
		config.Namespace,
		hex.EncodeToString(identity[:12]),
	)
	return &DistributedGatedEmbedder{
		next:   next,
		store:  store,
		events: events,
		config: config,
		keys: []string{
			hashTag + ":global",
			hashTag + ":origin:" + string(config.Origin),
		},
		limits: []int{
			config.ProviderMaxConcurrency,
			config.OriginMaxConcurrency,
		},
	}, nil
}

// Embed 等待跨进程槽位、调用下游，并在所有正常返回路径归还租约。
func (e *DistributedGatedEmbedder) Embed(
	ctx context.Context,
	request embeddingdomain.EmbedRequest,
) (result embeddingdomain.EmbedResult, err error) {
	waitStartedAt := time.Now()
	token, counts, acquireErr := e.acquire(ctx)
	waitDuration := time.Since(waitStartedAt)
	if acquireErr != nil {
		outcome := EmbeddingProviderAdmissionOutcomeCapacityTimeout
		if errors.Is(acquireErr, ErrEmbeddingProviderCoordinationUnavailable) {
			outcome = EmbeddingProviderAdmissionOutcomeCoordinationError
		} else if errors.Is(acquireErr, context.Canceled) ||
			errors.Is(acquireErr, context.DeadlineExceeded) {
			outcome = EmbeddingProviderAdmissionOutcomeCanceled
		}
		e.observe(
			ctx,
			EmbeddingProviderDistributedAdmissionEventRejected,
			outcome,
			waitDuration,
			0,
			counts,
			acquireErr,
		)
		return embeddingdomain.EmbedResult{}, acquireErr
	}

	e.observe(
		ctx,
		EmbeddingProviderDistributedAdmissionEventAdmitted,
		"",
		waitDuration,
		0,
		counts,
		nil,
	)
	executionStartedAt := time.Now()
	defer func() {
		releaseErr := e.store.ReleaseCapacity(
			context.Background(),
			e.keys,
			token,
		)
		outcome := EmbeddingProviderAdmissionOutcomeSucceeded
		if err != nil {
			outcome = EmbeddingProviderAdmissionOutcomeDownstreamError
		}
		if releaseErr != nil {
			outcome = EmbeddingProviderAdmissionOutcomeCoordinationError
		}
		releasedCounts := append([]int(nil), counts...)
		for index := range releasedCounts {
			releasedCounts[index] = max(releasedCounts[index]-1, 0)
		}
		e.observe(
			ctx,
			EmbeddingProviderDistributedAdmissionEventReleased,
			outcome,
			waitDuration,
			time.Since(executionStartedAt),
			releasedCounts,
			releaseErr,
		)
	}()

	return e.next.Embed(ctx, request)
}

func (e *DistributedGatedEmbedder) acquire(
	ctx context.Context,
) (string, []int, error) {
	var timeout <-chan time.Time
	var timeoutTimer *time.Timer
	if e.config.WaitTimeout > 0 {
		timeoutTimer = time.NewTimer(e.config.WaitTimeout)
		defer timeoutTimer.Stop()
		timeout = timeoutTimer.C
	}

	var lastCounts []int
	for {
		if err := ctx.Err(); err != nil {
			return "", lastCounts, err
		}
		token, counts, acquired, err := e.store.AcquireCapacity(
			ctx,
			e.keys,
			e.limits,
			e.config.LeaseTTL,
		)
		lastCounts = counts
		if err != nil {
			return "", lastCounts, errors.Join(
				ErrEmbeddingProviderCapacityExhausted,
				fmt.Errorf(
					"%w: %v",
					ErrEmbeddingProviderCoordinationUnavailable,
					err,
				),
			)
		}
		if acquired {
			return token, counts, nil
		}

		retryTimer := time.NewTimer(e.config.RetryInterval)
		select {
		case <-ctx.Done():
			retryTimer.Stop()
			return "", lastCounts, ctx.Err()
		case <-timeout:
			retryTimer.Stop()
			return "", lastCounts, ErrEmbeddingProviderCapacityExhausted
		case <-retryTimer.C:
		}
	}
}

func (e *DistributedGatedEmbedder) observe(
	ctx context.Context,
	eventType EmbeddingProviderAdmissionEventType,
	outcome EmbeddingProviderAdmissionOutcome,
	waitDuration time.Duration,
	executionDuration time.Duration,
	counts []int,
	err error,
) {
	providerCount := 0
	originCount := 0
	if len(counts) > 0 {
		providerCount = counts[0]
	}
	if len(counts) > 1 {
		originCount = counts[1]
	}
	e.events.ObserveEmbeddingProviderAdmissionEvent(
		ctx,
		EmbeddingProviderAdmissionEvent{
			Type:                 eventType,
			Origin:               e.config.Origin,
			Outcome:              outcome,
			WaitDuration:         waitDuration,
			ExecutionDuration:    executionDuration,
			OriginInFlight:       originCount,
			OriginMaxConcurrency: e.config.OriginMaxConcurrency,
			InFlight:             providerCount,
			MaxConcurrency:       e.config.ProviderMaxConcurrency,
			Err:                  err,
		},
	)
}
