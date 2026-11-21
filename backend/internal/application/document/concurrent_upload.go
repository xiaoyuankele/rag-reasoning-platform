package document

import (
	"context"
	"errors"
	"io"
	"sync"
	"time"

	accessdomain "rag-reasoning-platform/backend/internal/domain/access"
)

var (
	// ErrUploadOwnerCapacityExhausted 表示当前用户在等待时间内没有取得上传槽位。
	ErrUploadOwnerCapacityExhausted = errors.New(
		"owner upload concurrency capacity exhausted",
	)

	// ErrUploadGlobalCapacityExhausted 表示整个后端在等待时间内没有上传槽位。
	ErrUploadGlobalCapacityExhausted = errors.New(
		"global upload concurrency capacity exhausted",
	)

	// ErrUploadConcurrencyDependencies 表示并发包装器缺少下游服务或观测器。
	ErrUploadConcurrencyDependencies = errors.New(
		"upload concurrency dependencies must be provided",
	)

	// ErrUploadConcurrencyConfiguration 表示并发上限或等待超时配置非法。
	ErrUploadConcurrencyConfiguration = errors.New(
		"upload concurrency configuration is invalid",
	)
)

// UploadAdmissionEventType 表示上传请求经过容量闸门时的生命周期节点。
type UploadAdmissionEventType string

const (
	UploadAdmissionEventAdmitted UploadAdmissionEventType = "upload_request_admitted"
	UploadAdmissionEventRejected UploadAdmissionEventType = "upload_request_rejected"
	UploadAdmissionEventReleased UploadAdmissionEventType = "upload_request_released"
)

// UploadAdmissionOutcome 是上传被拒绝或完成时的稳定结果分类。
type UploadAdmissionOutcome string

const (
	UploadAdmissionOutcomeSucceeded             UploadAdmissionOutcome = "succeeded"
	UploadAdmissionOutcomeDownstreamError       UploadAdmissionOutcome = "downstream_error"
	UploadAdmissionOutcomeOwnerCapacityTimeout  UploadAdmissionOutcome = "owner_capacity_timeout"
	UploadAdmissionOutcomeGlobalCapacityTimeout UploadAdmissionOutcome = "global_capacity_timeout"
	UploadAdmissionOutcomeCanceled              UploadAdmissionOutcome = "canceled"
)

// UploadAdmissionEvent 是 Application 交给 Observability 的安全事件。
// 它不包含文件名、文件内容、哈希、存储路径或用户标识。
type UploadAdmissionEvent struct {
	Type                 UploadAdmissionEventType
	Outcome              UploadAdmissionOutcome
	WaitDuration         time.Duration
	ExecutionDuration    time.Duration
	OwnerInFlight        int
	OwnerMaxConcurrency  int
	GlobalInFlight       int
	GlobalMaxConcurrency int
	BytesRead            int64
	Duplicate            bool
}

// UploadAdmissionEventObserver 是上传容量闸门向外输出观测事件的端口。
type UploadAdmissionEventObserver interface {
	ObserveUploadAdmissionEvent(context.Context, UploadAdmissionEvent)
}

// documentUploader 是并发包装器对下游上传用例需要的最小接口。
type documentUploader interface {
	Upload(
		context.Context,
		accessdomain.OwnerScope,
		UploadInput,
	) (UploadResult, error)
}

var _ documentUploader = (*UploadService)(nil)

type uploadCapacityReason int

const (
	uploadCapacityAvailable uploadCapacityReason = iota
	uploadCapacityOwnerFull
	uploadCapacityGlobalFull
)

// uploadCapacityGate 使用一把互斥锁原子检查单用户和全局两个计数器。
// changed 在槽位释放时被关闭并替换，用于唤醒所有等待者重新竞争。
type uploadCapacityGate struct {
	mu                   sync.Mutex
	changed              chan struct{}
	ownerInFlight        map[int64]int
	globalInFlight       int
	ownerMaxConcurrency  int
	globalMaxConcurrency int
}

func newUploadCapacityGate(
	ownerMaxConcurrency int,
	globalMaxConcurrency int,
) *uploadCapacityGate {
	return &uploadCapacityGate{
		changed:              make(chan struct{}),
		ownerInFlight:        make(map[int64]int),
		ownerMaxConcurrency:  ownerMaxConcurrency,
		globalMaxConcurrency: globalMaxConcurrency,
	}
}

// ConcurrentUploadService 为完整上传链路提供单用户和全局有界并发。
// 槽位覆盖文件流读取、物理存储、数据库写入以及必要的补偿删除。
type ConcurrentUploadService struct {
	next        documentUploader
	events      UploadAdmissionEventObserver
	gate        *uploadCapacityGate
	waitTimeout time.Duration
}

var _ documentUploader = (*ConcurrentUploadService)(nil)

// NewConcurrentUploadService 创建上传并发包装器。
func NewConcurrentUploadService(
	next documentUploader,
	events UploadAdmissionEventObserver,
	ownerMaxConcurrency int,
	globalMaxConcurrency int,
	waitTimeout time.Duration,
) (*ConcurrentUploadService, error) {
	if next == nil || events == nil {
		return nil, ErrUploadConcurrencyDependencies
	}
	if ownerMaxConcurrency <= 0 ||
		globalMaxConcurrency < ownerMaxConcurrency ||
		waitTimeout <= 0 {
		return nil, ErrUploadConcurrencyConfiguration
	}

	return &ConcurrentUploadService{
		next:   next,
		events: events,
		gate: newUploadCapacityGate(
			ownerMaxConcurrency,
			globalMaxConcurrency,
		),
		waitTimeout: waitTimeout,
	}, nil
}

// Upload 等待同时满足单用户和全局容量后调用原服务，并保证所有返回路径释放槽位。
func (s *ConcurrentUploadService) Upload(
	ctx context.Context,
	scope accessdomain.OwnerScope,
	input UploadInput,
) (result UploadResult, err error) {
	if !scope.IsValid() {
		return UploadResult{}, accessdomain.ErrInvalidOwnerScope
	}

	waitStartedAt := time.Now()
	ownerInFlight, globalInFlight, err := s.acquire(
		ctx,
		scope.OwnerUserID(),
		waitStartedAt,
	)
	if err != nil {
		return UploadResult{}, err
	}
	waitDuration := time.Since(waitStartedAt)
	countedContent := &uploadCountingReader{reader: input.Content}
	if input.Content != nil {
		input.Content = countedContent
	}
	executionStartedAt := time.Now()
	defer func() {
		remainingOwnerInFlight, remainingGlobalInFlight := s.gate.release(
			scope.OwnerUserID(),
		)

		outcome := UploadAdmissionOutcomeSucceeded
		if errors.Is(err, context.Canceled) ||
			errors.Is(err, context.DeadlineExceeded) {
			outcome = UploadAdmissionOutcomeCanceled
		} else if err != nil {
			outcome = UploadAdmissionOutcomeDownstreamError
		}

		s.events.ObserveUploadAdmissionEvent(ctx, UploadAdmissionEvent{
			Type:                 UploadAdmissionEventReleased,
			Outcome:              outcome,
			WaitDuration:         waitDuration,
			ExecutionDuration:    time.Since(executionStartedAt),
			OwnerInFlight:        remainingOwnerInFlight,
			OwnerMaxConcurrency:  s.gate.ownerMaxConcurrency,
			GlobalInFlight:       remainingGlobalInFlight,
			GlobalMaxConcurrency: s.gate.globalMaxConcurrency,
			BytesRead:            countedContent.bytesRead,
			Duplicate:            result.Duplicate,
		})
	}()
	s.events.ObserveUploadAdmissionEvent(ctx, UploadAdmissionEvent{
		Type:                 UploadAdmissionEventAdmitted,
		WaitDuration:         waitDuration,
		OwnerInFlight:        ownerInFlight,
		OwnerMaxConcurrency:  s.gate.ownerMaxConcurrency,
		GlobalInFlight:       globalInFlight,
		GlobalMaxConcurrency: s.gate.globalMaxConcurrency,
	})

	return s.next.Upload(ctx, scope, input)
}

func (s *ConcurrentUploadService) acquire(
	ctx context.Context,
	ownerUserID int64,
	waitStartedAt time.Time,
) (int, int, error) {
	if err := ctx.Err(); err != nil {
		ownerInFlight, globalInFlight := s.gate.snapshot(ownerUserID)
		s.observeRejected(
			ctx,
			0,
			UploadAdmissionOutcomeCanceled,
			ownerInFlight,
			globalInFlight,
		)
		return 0, 0, err
	}

	timer := time.NewTimer(s.waitTimeout)
	defer timer.Stop()

	for {
		acquired, reason, ownerInFlight, globalInFlight, changed :=
			s.gate.tryAcquire(ownerUserID)
		if acquired {
			return ownerInFlight, globalInFlight, nil
		}

		select {
		case <-changed:
			continue
		case <-ctx.Done():
			s.observeRejected(
				ctx,
				time.Since(waitStartedAt),
				UploadAdmissionOutcomeCanceled,
				ownerInFlight,
				globalInFlight,
			)
			return 0, 0, ctx.Err()
		case <-timer.C:
			// 槽位释放和计时器触发可能同时发生。最后再原子尝试一次，
			// 避免容量已经可用却仍然返回超时。
			acquired, reason, ownerInFlight, globalInFlight, _ =
				s.gate.tryAcquire(ownerUserID)
			if acquired {
				return ownerInFlight, globalInFlight, nil
			}

			outcome := UploadAdmissionOutcomeGlobalCapacityTimeout
			capacityErr := ErrUploadGlobalCapacityExhausted
			if reason == uploadCapacityOwnerFull {
				outcome = UploadAdmissionOutcomeOwnerCapacityTimeout
				capacityErr = ErrUploadOwnerCapacityExhausted
			}
			s.observeRejected(
				ctx,
				time.Since(waitStartedAt),
				outcome,
				ownerInFlight,
				globalInFlight,
			)
			return 0, 0, capacityErr
		}
	}
}

func (s *ConcurrentUploadService) observeRejected(
	ctx context.Context,
	waitDuration time.Duration,
	outcome UploadAdmissionOutcome,
	ownerInFlight int,
	globalInFlight int,
) {
	s.events.ObserveUploadAdmissionEvent(ctx, UploadAdmissionEvent{
		Type:                 UploadAdmissionEventRejected,
		Outcome:              outcome,
		WaitDuration:         waitDuration,
		OwnerInFlight:        ownerInFlight,
		OwnerMaxConcurrency:  s.gate.ownerMaxConcurrency,
		GlobalInFlight:       globalInFlight,
		GlobalMaxConcurrency: s.gate.globalMaxConcurrency,
	})
}

func (g *uploadCapacityGate) tryAcquire(
	ownerUserID int64,
) (bool, uploadCapacityReason, int, int, <-chan struct{}) {
	g.mu.Lock()
	defer g.mu.Unlock()

	ownerInFlight := g.ownerInFlight[ownerUserID]
	if ownerInFlight >= g.ownerMaxConcurrency {
		return false,
			uploadCapacityOwnerFull,
			ownerInFlight,
			g.globalInFlight,
			g.changed
	}
	if g.globalInFlight >= g.globalMaxConcurrency {
		return false,
			uploadCapacityGlobalFull,
			ownerInFlight,
			g.globalInFlight,
			g.changed
	}

	ownerInFlight++
	g.ownerInFlight[ownerUserID] = ownerInFlight
	g.globalInFlight++
	return true,
		uploadCapacityAvailable,
		ownerInFlight,
		g.globalInFlight,
		nil
}

func (g *uploadCapacityGate) release(ownerUserID int64) (int, int) {
	g.mu.Lock()
	defer g.mu.Unlock()

	ownerInFlight := g.ownerInFlight[ownerUserID] - 1
	if ownerInFlight <= 0 {
		delete(g.ownerInFlight, ownerUserID)
		ownerInFlight = 0
	} else {
		g.ownerInFlight[ownerUserID] = ownerInFlight
	}
	if g.globalInFlight > 0 {
		g.globalInFlight--
	}

	close(g.changed)
	g.changed = make(chan struct{})
	return ownerInFlight, g.globalInFlight
}

func (g *uploadCapacityGate) snapshot(ownerUserID int64) (int, int) {
	g.mu.Lock()
	defer g.mu.Unlock()
	return g.ownerInFlight[ownerUserID], g.globalInFlight
}

// uploadCountingReader 只统计实际从 HTTP 文件流读取的字节数，不缓存内容。
type uploadCountingReader struct {
	reader    io.Reader
	bytesRead int64
}

func (r *uploadCountingReader) Read(buffer []byte) (int, error) {
	readCount, err := r.reader.Read(buffer)
	r.bytesRead += int64(readCount)
	return readCount, err
}
