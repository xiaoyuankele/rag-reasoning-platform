package answer

import (
	"context"
	"errors"
	"time"

	accessdomain "rag-reasoning-platform/backend/internal/domain/access"
)

var (
	// ErrAnswerCapacityExhausted 表示全局等待区已满，或请求在限定时间内
	// 仍未获得执行槽位。API 层将它映射为可重试的 503。
	ErrAnswerCapacityExhausted = errors.New("answer concurrency capacity exhausted")

	// ErrAnswerOwnerCapacityExhausted 表示同一 Owner 已经占满自己的等待预算。
	// API 层将它映射为 429，避免单个用户挤占全局队列。
	ErrAnswerOwnerCapacityExhausted = errors.New("answer owner concurrency capacity exhausted")

	// ErrAnswerConcurrencyDependencies 表示并发包装器缺少下游问答服务或观察器。
	ErrAnswerConcurrencyDependencies = errors.New(
		"answer concurrency dependencies must be provided",
	)

	// ErrAnswerConcurrencyConfiguration 表示并发、等待容量或超时无法组成有效配置。
	ErrAnswerConcurrencyConfiguration = errors.New(
		"answer concurrency configuration is invalid",
	)
)

// AnswerAdmissionLimits 定义单进程内问答执行和等待的有界容量。
// 等待上限只计算尚未取得执行槽位的请求，不包含正在执行的请求。
type AnswerAdmissionLimits struct {
	MaxConcurrencyGlobal   int
	MaxConcurrencyPerOwner int
	MaxWaitersGlobal       int
	MaxWaitersPerOwner     int
	WaitTimeout            time.Duration
}

// IsValid 判断全局和 Owner 容量能否形成有效的准入规则。
func (l AnswerAdmissionLimits) IsValid() bool {
	return l.MaxConcurrencyGlobal > 0 &&
		l.MaxConcurrencyPerOwner > 0 &&
		l.MaxConcurrencyPerOwner <= l.MaxConcurrencyGlobal &&
		l.MaxWaitersGlobal > 0 &&
		l.MaxWaitersPerOwner > 0 &&
		l.MaxWaitersPerOwner <= l.MaxWaitersGlobal &&
		l.WaitTimeout > 0
}

// AnswerAdmissionEventType 表示问答请求进入并发闸门后的生命周期节点。
type AnswerAdmissionEventType string

const (
	AnswerAdmissionEventAdmitted            AnswerAdmissionEventType = "answer_request_admitted"
	AnswerAdmissionEventRejected            AnswerAdmissionEventType = "answer_request_rejected"
	AnswerAdmissionEventReleased            AnswerAdmissionEventType = "answer_request_released"
	AnswerDistributedAdmissionEventAdmitted AnswerAdmissionEventType = "answer_distributed_request_admitted"
	AnswerDistributedAdmissionEventRejected AnswerAdmissionEventType = "answer_distributed_request_rejected"
	AnswerDistributedAdmissionEventReleased AnswerAdmissionEventType = "answer_distributed_request_released"
)

// AnswerAdmissionOutcome 描述请求被拒绝或释放时的稳定结果分类。
type AnswerAdmissionOutcome string

const (
	AnswerAdmissionOutcomeSucceeded         AnswerAdmissionOutcome = "succeeded"
	AnswerAdmissionOutcomeDownstreamError   AnswerAdmissionOutcome = "downstream_error"
	AnswerAdmissionOutcomeCapacityTimeout   AnswerAdmissionOutcome = "capacity_timeout"
	AnswerAdmissionOutcomeOwnerCapacity     AnswerAdmissionOutcome = "owner_capacity_exhausted"
	AnswerAdmissionOutcomeGlobalCapacity    AnswerAdmissionOutcome = "global_capacity_exhausted"
	AnswerAdmissionOutcomeCanceled          AnswerAdmissionOutcome = "canceled"
	AnswerAdmissionOutcomeCoordinationError AnswerAdmissionOutcome = "coordination_error"
)

// AnswerAdmissionEvent 是并发闸门交给 Observability 的安全事件。
//
// 它只包含容量、等待和执行耗时，不包含 Owner ID、问题、Prompt、答案或证据正文。
type AnswerAdmissionEvent struct {
	Type                AnswerAdmissionEventType
	Outcome             AnswerAdmissionOutcome
	WaitDuration        time.Duration
	ExecutionDuration   time.Duration
	InFlight            int
	MaxConcurrency      int
	OwnerInFlight       int
	OwnerMaxConcurrency int
	Waiting             int
	MaxWaiting          int
	OwnerWaiting        int
	OwnerMaxWaiting     int
	Err                 error
}

// AnswerAdmissionEventObserver 是 Application 输出并发观测事件的端口。
type AnswerAdmissionEventObserver interface {
	ObserveAnswerAdmissionEvent(context.Context, AnswerAdmissionEvent)
}

// Answerer 是问答装饰器和 Handler 共同依赖的最小应用接口。
// 生产环境注入 *Service；测试可以注入不会调用数据库和远程模型的 Fake。
type Answerer interface {
	Answer(
		ctx context.Context,
		scope accessdomain.OwnerScope,
		input Input,
	) (Output, error)
}

// answerer 保留为包内兼容别名，已有 Worker 和测试仍可使用原名称。
type answerer = Answerer

var _ Answerer = (*Service)(nil)

// ConcurrentService 为完整问答链路提供 Owner 公平、有界并发和限时排队。
//
// 一个执行槽位覆盖“问题向量化 → 数据库检索 → 答案生成”的完整过程。
// 调度器只负责谁可以开始执行，不改变原有问答业务和错误结果。
type ConcurrentService struct {
	next      Answerer
	events    AnswerAdmissionEventObserver
	scheduler *answerAdmissionScheduler
}

var _ Answerer = (*ConcurrentService)(nil)

// NewConcurrentService 创建 Owner 公平的问答并发包装器。
func NewConcurrentService(
	next Answerer,
	events AnswerAdmissionEventObserver,
	limits AnswerAdmissionLimits,
) (*ConcurrentService, error) {
	if next == nil || events == nil {
		return nil, ErrAnswerConcurrencyDependencies
	}
	if !limits.IsValid() {
		return nil, ErrAnswerConcurrencyConfiguration
	}

	return &ConcurrentService{
		next:      next,
		events:    events,
		scheduler: newAnswerAdmissionScheduler(limits),
	}, nil
}

// Answer 等待 Owner 公平调度器分配槽位，执行结束后一定释放容量。
func (s *ConcurrentService) Answer(
	ctx context.Context,
	scope accessdomain.OwnerScope,
	input Input,
) (output Output, err error) {
	waitStartedAt := time.Now()
	admissionDeadline := waitStartedAt.Add(s.scheduler.limits.WaitTimeout)
	decision := s.scheduler.acquire(ctx, scope)
	waitDuration := time.Since(waitStartedAt)

	if decision.err != nil {
		// 无效 OwnerScope 是调用契约错误，不属于容量准入结果；若写成 rejected，
		// 会污染容量统计并生成一个没有合法 outcome 的观测事件。
		if errors.Is(decision.err, accessdomain.ErrInvalidOwnerScope) {
			return Output{}, decision.err
		}
		s.observe(
			ctx,
			AnswerAdmissionEventRejected,
			decision.outcome,
			waitDuration,
			0,
			decision.snapshot,
		)
		return Output{}, decision.err
	}

	s.observe(
		ctx,
		AnswerAdmissionEventAdmitted,
		"",
		waitDuration,
		0,
		decision.snapshot,
	)

	executionStartedAt := time.Now()
	defer func() {
		snapshot := s.scheduler.release(scope)
		outcome := AnswerAdmissionOutcomeSucceeded
		if err != nil {
			outcome = AnswerAdmissionOutcomeDownstreamError
		}
		s.observe(
			ctx,
			AnswerAdmissionEventReleased,
			outcome,
			waitDuration,
			time.Since(executionStartedAt),
			snapshot,
		)
	}()

	return s.next.Answer(
		context.WithValue(ctx, answerAdmissionDeadlineContextKey{}, admissionDeadline),
		scope,
		input,
	)
}

type answerAdmissionDeadlineContextKey struct{}

func answerAdmissionDeadlineFromContext(ctx context.Context) (time.Time, bool) {
	deadline, ok := ctx.Value(answerAdmissionDeadlineContextKey{}).(time.Time)
	return deadline, ok && !deadline.IsZero()
}

func (s *ConcurrentService) observe(
	ctx context.Context,
	eventType AnswerAdmissionEventType,
	outcome AnswerAdmissionOutcome,
	waitDuration time.Duration,
	executionDuration time.Duration,
	snapshot answerAdmissionSnapshot,
) {
	limits := s.scheduler.limits
	s.events.ObserveAnswerAdmissionEvent(ctx, AnswerAdmissionEvent{
		Type:                eventType,
		Outcome:             outcome,
		WaitDuration:        waitDuration,
		ExecutionDuration:   executionDuration,
		InFlight:            snapshot.inFlight,
		MaxConcurrency:      limits.MaxConcurrencyGlobal,
		OwnerInFlight:       snapshot.ownerInFlight,
		OwnerMaxConcurrency: limits.MaxConcurrencyPerOwner,
		Waiting:             snapshot.waiting,
		MaxWaiting:          limits.MaxWaitersGlobal,
		OwnerWaiting:        snapshot.ownerWaiting,
		OwnerMaxWaiting:     limits.MaxWaitersPerOwner,
	})
}
