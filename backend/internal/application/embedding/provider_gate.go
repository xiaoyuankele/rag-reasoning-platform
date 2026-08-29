package embedding

import (
	"context"
	"errors"
	"sync/atomic"
	"time"

	embeddingdomain "rag-reasoning-platform/backend/internal/domain/embedding"
)

var (
	// ErrEmbeddingProviderCapacityExhausted 表示在线请求在限定时间内没有取得
	// 远程 Embedding 调用槽位。它属于 Application 的容量策略错误，而不是
	// DashScope/OpenAI 返回的提供方错误。
	ErrEmbeddingProviderCapacityExhausted = errors.New(
		"embedding provider concurrency capacity exhausted",
	)

	// ErrEmbeddingProviderGateDependencies 表示包装器缺少下游 Embedder、共享
	// Gate 或观测器。这是程序组装错误，应该在启动时被发现。
	ErrEmbeddingProviderGateDependencies = errors.New(
		"embedding provider gate dependencies must be provided",
	)

	// ErrEmbeddingProviderGateConfiguration 表示共享并发数、调用来源或等待
	// 超时配置非法。
	ErrEmbeddingProviderGateConfiguration = errors.New(
		"embedding provider gate configuration is invalid",
	)
)

// EmbeddingProviderCallOrigin 标识远程 Embedding 调用来自哪类消费者。
// 它只用于容量策略和观测，不会进入提供方请求。
type EmbeddingProviderCallOrigin string

const (
	EmbeddingProviderCallOriginWorker EmbeddingProviderCallOrigin = "worker"
	EmbeddingProviderCallOriginOnline EmbeddingProviderCallOrigin = "online"
)

// EmbeddingProviderAdmissionEventType 表示一次调用经过共享闸门时的节点。
type EmbeddingProviderAdmissionEventType string

const (
	EmbeddingProviderAdmissionEventAdmitted            EmbeddingProviderAdmissionEventType = "embedding_provider_request_admitted"
	EmbeddingProviderAdmissionEventRejected            EmbeddingProviderAdmissionEventType = "embedding_provider_request_rejected"
	EmbeddingProviderAdmissionEventReleased            EmbeddingProviderAdmissionEventType = "embedding_provider_request_released"
	EmbeddingProviderDistributedAdmissionEventAdmitted EmbeddingProviderAdmissionEventType = "embedding_provider_distributed_request_admitted"
	EmbeddingProviderDistributedAdmissionEventRejected EmbeddingProviderAdmissionEventType = "embedding_provider_distributed_request_rejected"
	EmbeddingProviderDistributedAdmissionEventReleased EmbeddingProviderAdmissionEventType = "embedding_provider_distributed_request_released"
)

// EmbeddingProviderAdmissionOutcome 是拒绝或释放事件的稳定结果分类。
type EmbeddingProviderAdmissionOutcome string

const (
	EmbeddingProviderAdmissionOutcomeSucceeded         EmbeddingProviderAdmissionOutcome = "succeeded"
	EmbeddingProviderAdmissionOutcomeDownstreamError   EmbeddingProviderAdmissionOutcome = "downstream_error"
	EmbeddingProviderAdmissionOutcomeCapacityTimeout   EmbeddingProviderAdmissionOutcome = "capacity_timeout"
	EmbeddingProviderAdmissionOutcomeCanceled          EmbeddingProviderAdmissionOutcome = "canceled"
	EmbeddingProviderAdmissionOutcomeCoordinationError EmbeddingProviderAdmissionOutcome = "coordination_error"
)

// EmbeddingProviderAdmissionEvent 是 Application 交给 Observability 的安全事件。
// 不包含输入文本、向量、模型密钥或远程响应正文。
type EmbeddingProviderAdmissionEvent struct {
	Type                 EmbeddingProviderAdmissionEventType
	Origin               EmbeddingProviderCallOrigin
	Outcome              EmbeddingProviderAdmissionOutcome
	WaitDuration         time.Duration
	ExecutionDuration    time.Duration
	OriginInFlight       int
	OriginMaxConcurrency int
	InFlight             int
	MaxConcurrency       int
	Err                  error
}

// EmbeddingProviderAdmissionObserver 是容量闸门向外输出观测事件的端口。
type EmbeddingProviderAdmissionObserver interface {
	ObserveEmbeddingProviderAdmissionEvent(
		context.Context,
		EmbeddingProviderAdmissionEvent,
	)
}

type embeddingProviderSlots struct {
	slots          chan struct{}
	maxConcurrency int
	inFlight       atomic.Int64
}

// EmbeddingProviderGate 同时保存后台、在线和提供方全局三组槽位。
//
// Worker 与在线调用先取得自己的分类槽位，再取得全局槽位。分类上限之和不能
// 超过全局上限，因此后台任务即使堆积也无法占用在线预留容量。
type EmbeddingProviderGate struct {
	provider *embeddingProviderSlots
	worker   *embeddingProviderSlots
	online   *embeddingProviderSlots
}

// NewEmbeddingProviderGate 创建带分类隔离的远程 Embedding 容量闸门。
func NewEmbeddingProviderGate(
	providerMaxConcurrency int,
	workerMaxConcurrency int,
	onlineMaxConcurrency int,
) (*EmbeddingProviderGate, error) {
	if providerMaxConcurrency <= 0 ||
		workerMaxConcurrency <= 0 ||
		onlineMaxConcurrency <= 0 ||
		workerMaxConcurrency > providerMaxConcurrency-onlineMaxConcurrency {
		return nil, ErrEmbeddingProviderGateConfiguration
	}

	return &EmbeddingProviderGate{
		provider: newEmbeddingProviderSlots(providerMaxConcurrency),
		worker:   newEmbeddingProviderSlots(workerMaxConcurrency),
		online:   newEmbeddingProviderSlots(onlineMaxConcurrency),
	}, nil
}

func newEmbeddingProviderSlots(maxConcurrency int) *embeddingProviderSlots {
	return &embeddingProviderSlots{
		slots:          make(chan struct{}, maxConcurrency),
		maxConcurrency: maxConcurrency,
	}
}

func (g *EmbeddingProviderGate) originSlots(
	origin EmbeddingProviderCallOrigin,
) *embeddingProviderSlots {
	if origin == EmbeddingProviderCallOriginWorker {
		return g.worker
	}
	return g.online
}

// GatedEmbedder 在调用真实 Embedder 前依次取得分类槽位和全局槽位，
// 并在调用结束后归还两者。
//
// waitTimeout=0 表示只受 ctx 控制，适合允许排队的后台 Worker；正数表示最多
// 等待该时长，适合需要及时向用户返回“服务繁忙”的在线请求。
type GatedEmbedder struct {
	next        embeddingdomain.Embedder
	gate        *EmbeddingProviderGate
	events      EmbeddingProviderAdmissionObserver
	origin      EmbeddingProviderCallOrigin
	waitTimeout time.Duration
}

var _ embeddingdomain.Embedder = (*GatedEmbedder)(nil)

// NewGatedEmbedder 创建分类隔离与全局保护的容量包装器。
func NewGatedEmbedder(
	next embeddingdomain.Embedder,
	gate *EmbeddingProviderGate,
	events EmbeddingProviderAdmissionObserver,
	origin EmbeddingProviderCallOrigin,
	waitTimeout time.Duration,
) (*GatedEmbedder, error) {
	if next == nil || gate == nil || events == nil {
		return nil, ErrEmbeddingProviderGateDependencies
	}
	if (origin != EmbeddingProviderCallOriginWorker &&
		origin != EmbeddingProviderCallOriginOnline) || waitTimeout < 0 {
		return nil, ErrEmbeddingProviderGateConfiguration
	}

	return &GatedEmbedder{
		next:        next,
		gate:        gate,
		events:      events,
		origin:      origin,
		waitTimeout: waitTimeout,
	}, nil
}

// Embed 等待分类与全局槽位、调用下游，并保证所有返回路径都会释放槽位。
func (e *GatedEmbedder) Embed(
	ctx context.Context,
	request embeddingdomain.EmbedRequest,
) (result embeddingdomain.EmbedResult, err error) {
	if err := ctx.Err(); err != nil {
		e.observeRejected(ctx, 0, EmbeddingProviderAdmissionOutcomeCanceled)
		return embeddingdomain.EmbedResult{}, err
	}

	waitStartedAt := time.Now()
	if err := e.acquire(ctx); err != nil {
		outcome := EmbeddingProviderAdmissionOutcomeCapacityTimeout
		if errors.Is(err, context.Canceled) ||
			errors.Is(err, context.DeadlineExceeded) {
			outcome = EmbeddingProviderAdmissionOutcomeCanceled
		}
		e.observeRejected(ctx, time.Since(waitStartedAt), outcome)
		return embeddingdomain.EmbedResult{}, err
	}

	waitDuration := time.Since(waitStartedAt)
	originSlots := e.gate.originSlots(e.origin)
	providerInFlight := int(e.gate.provider.inFlight.Add(1))
	originInFlight := int(originSlots.inFlight.Add(1))
	e.events.ObserveEmbeddingProviderAdmissionEvent(
		ctx,
		EmbeddingProviderAdmissionEvent{
			Type:                 EmbeddingProviderAdmissionEventAdmitted,
			Origin:               e.origin,
			WaitDuration:         waitDuration,
			OriginInFlight:       originInFlight,
			OriginMaxConcurrency: originSlots.maxConcurrency,
			InFlight:             providerInFlight,
			MaxConcurrency:       e.gate.provider.maxConcurrency,
		},
	)

	executionStartedAt := time.Now()
	defer func() {
		remainingProviderInFlight := int(e.gate.provider.inFlight.Add(-1))
		<-e.gate.provider.slots
		remainingOriginInFlight := int(originSlots.inFlight.Add(-1))
		<-originSlots.slots

		outcome := EmbeddingProviderAdmissionOutcomeSucceeded
		if errors.Is(err, context.Canceled) ||
			errors.Is(err, context.DeadlineExceeded) {
			outcome = EmbeddingProviderAdmissionOutcomeCanceled
		} else if err != nil {
			outcome = EmbeddingProviderAdmissionOutcomeDownstreamError
		}

		e.events.ObserveEmbeddingProviderAdmissionEvent(
			ctx,
			EmbeddingProviderAdmissionEvent{
				Type:                 EmbeddingProviderAdmissionEventReleased,
				Origin:               e.origin,
				Outcome:              outcome,
				WaitDuration:         waitDuration,
				ExecutionDuration:    time.Since(executionStartedAt),
				OriginInFlight:       remainingOriginInFlight,
				OriginMaxConcurrency: originSlots.maxConcurrency,
				InFlight:             remainingProviderInFlight,
				MaxConcurrency:       e.gate.provider.maxConcurrency,
			},
		)
	}()

	return e.next.Embed(ctx, request)
}

func (e *GatedEmbedder) acquire(ctx context.Context) error {
	var timeout <-chan time.Time
	if e.waitTimeout > 0 {
		timer := time.NewTimer(e.waitTimeout)
		defer timer.Stop()
		timeout = timer.C
	}

	originSlots := e.gate.originSlots(e.origin)
	if err := acquireEmbeddingProviderSlot(ctx, originSlots.slots, timeout); err != nil {
		return err
	}
	if err := acquireEmbeddingProviderSlot(
		ctx,
		e.gate.provider.slots,
		timeout,
	); err != nil {
		<-originSlots.slots
		return err
	}
	return nil
}

func acquireEmbeddingProviderSlot(
	ctx context.Context,
	slots chan<- struct{},
	timeout <-chan time.Time,
) error {
	select {
	case slots <- struct{}{}:
		return nil
	case <-ctx.Done():
		return ctx.Err()
	case <-timeout:
		return ErrEmbeddingProviderCapacityExhausted
	}
}

func (e *GatedEmbedder) observeRejected(
	ctx context.Context,
	waitDuration time.Duration,
	outcome EmbeddingProviderAdmissionOutcome,
) {
	originSlots := e.gate.originSlots(e.origin)
	e.events.ObserveEmbeddingProviderAdmissionEvent(
		ctx,
		EmbeddingProviderAdmissionEvent{
			Type:                 EmbeddingProviderAdmissionEventRejected,
			Origin:               e.origin,
			Outcome:              outcome,
			WaitDuration:         waitDuration,
			OriginInFlight:       int(originSlots.inFlight.Load()),
			OriginMaxConcurrency: originSlots.maxConcurrency,
			InFlight:             int(e.gate.provider.inFlight.Load()),
			MaxConcurrency:       e.gate.provider.maxConcurrency,
		},
	)
}
