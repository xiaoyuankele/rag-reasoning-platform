package answer

import (
	"context"
	"errors"
	"sync/atomic"
	"time"

	accessdomain "rag-reasoning-platform/backend/internal/domain/access"
)

var (
	// ErrAnswerCapacityExhausted 表示问答并发槽位在规定等待时间内仍不可用。
	// API 层会把它映射为可重试的 503，而不会把它误报成业务参数错误。
	ErrAnswerCapacityExhausted = errors.New("answer concurrency capacity exhausted")

	// ErrAnswerConcurrencyDependencies 表示并发包装器缺少下游问答服务或观测器。
	ErrAnswerConcurrencyDependencies = errors.New(
		"answer concurrency dependencies must be provided",
	)

	// ErrAnswerConcurrencyConfiguration 表示并发数或等待超时不是正数。
	ErrAnswerConcurrencyConfiguration = errors.New(
		"answer concurrency configuration is invalid",
	)
)

// AnswerAdmissionEventType 表示问答请求进入并发闸门后的生命周期节点。
type AnswerAdmissionEventType string

const (
	AnswerAdmissionEventAdmitted AnswerAdmissionEventType = "answer_request_admitted"
	AnswerAdmissionEventRejected AnswerAdmissionEventType = "answer_request_rejected"
	AnswerAdmissionEventReleased AnswerAdmissionEventType = "answer_request_released"
)

// AnswerAdmissionOutcome 描述请求被拒绝或释放时的稳定结果分类。
type AnswerAdmissionOutcome string

const (
	AnswerAdmissionOutcomeSucceeded       AnswerAdmissionOutcome = "succeeded"
	AnswerAdmissionOutcomeDownstreamError AnswerAdmissionOutcome = "downstream_error"
	AnswerAdmissionOutcomeCapacityTimeout AnswerAdmissionOutcome = "capacity_timeout"
	AnswerAdmissionOutcomeCanceled        AnswerAdmissionOutcome = "canceled"
)

// AnswerAdmissionEvent 是并发闸门交给 Observability 的安全事件。
//
// 它只包含容量、等待和执行耗时，不包含问题、Prompt、答案或证据正文。
type AnswerAdmissionEvent struct {
	Type              AnswerAdmissionEventType
	Outcome           AnswerAdmissionOutcome
	WaitDuration      time.Duration
	ExecutionDuration time.Duration
	InFlight          int
	MaxConcurrency    int
}

// AnswerAdmissionEventObserver 是 Application 输出并发观测事件的端口。
type AnswerAdmissionEventObserver interface {
	ObserveAnswerAdmissionEvent(context.Context, AnswerAdmissionEvent)
}

// answerer 是并发包装器对下游问答用例需要的最小接口。
// 生产环境注入 *Service；测试可以注入不会调用数据库和远程模型的 Fake。
type answerer interface {
	Answer(
		ctx context.Context,
		scope accessdomain.OwnerScope,
		input Input,
	) (Output, error)
}

var _ answerer = (*Service)(nil)

// ConcurrentService 为完整问答链路提供进程内有界并发和限时排队。
//
// 一个槽位覆盖“问题向量化 → 数据库检索 → 答案生成”的完整执行过程，
// 因此并发上限能够同时保护远程 API、数据库连接池和本机资源。
type ConcurrentService struct {
	next           answerer
	events         AnswerAdmissionEventObserver
	slots          chan struct{}
	waitTimeout    time.Duration
	maxConcurrency int
	inFlight       atomic.Int64
}

var _ answerer = (*ConcurrentService)(nil)

// NewConcurrentService 创建问答并发包装器。
func NewConcurrentService(
	next answerer,
	events AnswerAdmissionEventObserver,
	maxConcurrency int,
	waitTimeout time.Duration,
) (*ConcurrentService, error) {
	if next == nil || events == nil {
		return nil, ErrAnswerConcurrencyDependencies
	}
	if maxConcurrency <= 0 || waitTimeout <= 0 {
		return nil, ErrAnswerConcurrencyConfiguration
	}

	return &ConcurrentService{
		next:           next,
		events:         events,
		slots:          make(chan struct{}, maxConcurrency),
		waitTimeout:    waitTimeout,
		maxConcurrency: maxConcurrency,
	}, nil
}

// Answer 等待并发槽位；获得槽位后调用原有 Service，结束时一定归还槽位。
func (s *ConcurrentService) Answer(
	ctx context.Context,
	scope accessdomain.OwnerScope,
	input Input,
) (output Output, err error) {
	if err := ctx.Err(); err != nil {
		s.observeRejected(ctx, 0, AnswerAdmissionOutcomeCanceled)
		return Output{}, err
	}

	waitStartedAt := time.Now()
	timer := time.NewTimer(s.waitTimeout)
	defer timer.Stop()

	select {
	case s.slots <- struct{}{}:
		waitDuration := time.Since(waitStartedAt)
		inFlight := int(s.inFlight.Add(1))
		s.events.ObserveAnswerAdmissionEvent(ctx, AnswerAdmissionEvent{
			Type:           AnswerAdmissionEventAdmitted,
			WaitDuration:   waitDuration,
			InFlight:       inFlight,
			MaxConcurrency: s.maxConcurrency,
		})

		executionStartedAt := time.Now()
		defer func() {
			remainingInFlight := int(s.inFlight.Add(-1))
			<-s.slots

			outcome := AnswerAdmissionOutcomeSucceeded
			if err != nil {
				outcome = AnswerAdmissionOutcomeDownstreamError
			}

			s.events.ObserveAnswerAdmissionEvent(ctx, AnswerAdmissionEvent{
				Type:              AnswerAdmissionEventReleased,
				Outcome:           outcome,
				WaitDuration:      waitDuration,
				ExecutionDuration: time.Since(executionStartedAt),
				InFlight:          remainingInFlight,
				MaxConcurrency:    s.maxConcurrency,
			})
		}()

		return s.next.Answer(ctx, scope, input)

	case <-ctx.Done():
		s.observeRejected(
			ctx,
			time.Since(waitStartedAt),
			AnswerAdmissionOutcomeCanceled,
		)
		return Output{}, ctx.Err()

	case <-timer.C:
		s.observeRejected(
			ctx,
			time.Since(waitStartedAt),
			AnswerAdmissionOutcomeCapacityTimeout,
		)
		return Output{}, ErrAnswerCapacityExhausted
	}
}

func (s *ConcurrentService) observeRejected(
	ctx context.Context,
	waitDuration time.Duration,
	outcome AnswerAdmissionOutcome,
) {
	s.events.ObserveAnswerAdmissionEvent(ctx, AnswerAdmissionEvent{
		Type:           AnswerAdmissionEventRejected,
		Outcome:        outcome,
		WaitDuration:   waitDuration,
		InFlight:       int(s.inFlight.Load()),
		MaxConcurrency: s.maxConcurrency,
	})
}
