package embedding

import (
	"context"
	"errors"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	embeddingdomain "rag-reasoning-platform/backend/internal/domain/embedding"
)

type fakeEmbeddingProviderAdmissionObserver struct {
	mu     sync.Mutex
	events []EmbeddingProviderAdmissionEvent
}

func (f *fakeEmbeddingProviderAdmissionObserver) ObserveEmbeddingProviderAdmissionEvent(
	_ context.Context,
	event EmbeddingProviderAdmissionEvent,
) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.events = append(f.events, event)
}

func (f *fakeEmbeddingProviderAdmissionObserver) snapshot() []EmbeddingProviderAdmissionEvent {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]EmbeddingProviderAdmissionEvent(nil), f.events...)
}

type blockingEmbedder struct {
	started   chan struct{}
	release   chan struct{}
	active    atomic.Int64
	maxActive atomic.Int64
}

func newBlockingEmbedder() *blockingEmbedder {
	return &blockingEmbedder{
		started: make(chan struct{}, 8),
		release: make(chan struct{}, 8),
	}
}

func (f *blockingEmbedder) Embed(
	ctx context.Context,
	_ embeddingdomain.EmbedRequest,
) (embeddingdomain.EmbedResult, error) {
	active := f.active.Add(1)
	defer f.active.Add(-1)
	for {
		maximum := f.maxActive.Load()
		if active <= maximum || f.maxActive.CompareAndSwap(maximum, active) {
			break
		}
	}
	f.started <- struct{}{}

	select {
	case <-f.release:
		return embeddingdomain.EmbedResult{}, nil
	case <-ctx.Done():
		return embeddingdomain.EmbedResult{}, ctx.Err()
	}
}

type fixedResultEmbedder struct {
	result embeddingdomain.EmbedResult
	err    error
}

func (f fixedResultEmbedder) Embed(
	context.Context,
	embeddingdomain.EmbedRequest,
) (embeddingdomain.EmbedResult, error) {
	return f.result, f.err
}

func TestGatedEmbeddersShareOneProviderCapacity(t *testing.T) {
	gate, err := NewEmbeddingProviderGate(4, 2, 2)
	if err != nil {
		t.Fatalf("NewEmbeddingProviderGate() error = %v", err)
	}
	observer := &fakeEmbeddingProviderAdmissionObserver{}
	provider := newBlockingEmbedder()
	worker, err := NewGatedEmbedder(
		provider,
		gate,
		observer,
		EmbeddingProviderCallOriginWorker,
		0,
	)
	if err != nil {
		t.Fatalf("NewGatedEmbedder(worker) error = %v", err)
	}
	online, err := NewGatedEmbedder(
		provider,
		gate,
		observer,
		EmbeddingProviderCallOriginOnline,
		20*time.Millisecond,
	)
	if err != nil {
		t.Fatalf("NewGatedEmbedder(online) error = %v", err)
	}

	results := make(chan error, 4)
	for range 2 {
		go func() {
			_, callErr := worker.Embed(context.Background(), embeddingdomain.EmbedRequest{})
			results <- callErr
		}()
	}
	waitForProviderStarts(t, provider.started, 2)

	// 第三个 Worker 只能等待自己的分类容量，不能借用在线预留槽位。
	workerWaitContext, cancelWorkerWait := context.WithTimeout(
		context.Background(),
		20*time.Millisecond,
	)
	_, err = worker.Embed(
		workerWaitContext,
		embeddingdomain.EmbedRequest{},
	)
	cancelWorkerWait()
	if !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("third worker Embed() error = %v, want context deadline", err)
	}

	// Worker 已经占满自己的两个槽位，但在线预留的两个槽位仍可使用。
	for range 2 {
		go func() {
			_, callErr := online.Embed(context.Background(), embeddingdomain.EmbedRequest{})
			results <- callErr
		}()
	}
	waitForProviderStarts(t, provider.started, 2)

	_, err = online.Embed(context.Background(), embeddingdomain.EmbedRequest{})
	if !errors.Is(err, ErrEmbeddingProviderCapacityExhausted) {
		t.Fatalf("third Embed() error = %v, want capacity exhausted", err)
	}
	if maximum := provider.maxActive.Load(); maximum != 4 {
		t.Fatalf("provider max active = %d, want 4", maximum)
	}

	for range 4 {
		provider.release <- struct{}{}
	}
	for range 4 {
		if callErr := <-results; callErr != nil {
			t.Fatalf("admitted Embed() error = %v", callErr)
		}
	}

	events := observer.snapshot()
	for _, event := range events {
		if event.InFlight > event.MaxConcurrency {
			t.Fatalf("event in-flight exceeds capacity: %+v", event)
		}
		if event.OriginInFlight > event.OriginMaxConcurrency {
			t.Fatalf("event origin in-flight exceeds capacity: %+v", event)
		}
	}
	if !containsProviderAdmissionEvent(
		events,
		EmbeddingProviderAdmissionEventRejected,
		EmbeddingProviderCallOriginOnline,
		EmbeddingProviderAdmissionOutcomeCapacityTimeout,
	) {
		t.Fatalf("events = %+v, want online capacity timeout", events)
	}
}

func TestGatedEmbedderReleasesSlotAfterDownstreamError(t *testing.T) {
	gate, err := NewEmbeddingProviderGate(2, 1, 1)
	if err != nil {
		t.Fatalf("NewEmbeddingProviderGate() error = %v", err)
	}
	observer := &fakeEmbeddingProviderAdmissionObserver{}
	downstreamErr := errors.New("provider failed")
	failing, err := NewGatedEmbedder(
		fixedResultEmbedder{err: downstreamErr},
		gate,
		observer,
		EmbeddingProviderCallOriginWorker,
		0,
	)
	if err != nil {
		t.Fatalf("NewGatedEmbedder(failing) error = %v", err)
	}
	wantResult := embeddingdomain.EmbedResult{PromptTokens: 3, TotalTokens: 3}
	succeeding, err := NewGatedEmbedder(
		fixedResultEmbedder{result: wantResult},
		gate,
		observer,
		EmbeddingProviderCallOriginOnline,
		20*time.Millisecond,
	)
	if err != nil {
		t.Fatalf("NewGatedEmbedder(succeeding) error = %v", err)
	}

	if _, err := failing.Embed(context.Background(), embeddingdomain.EmbedRequest{}); !errors.Is(err, downstreamErr) {
		t.Fatalf("failing Embed() error = %v, want downstream error", err)
	}
	result, err := succeeding.Embed(context.Background(), embeddingdomain.EmbedRequest{})
	if err != nil {
		t.Fatalf("succeeding Embed() error = %v, want nil", err)
	}
	if result.PromptTokens != wantResult.PromptTokens ||
		result.TotalTokens != wantResult.TotalTokens ||
		len(result.Vectors) != 0 {
		t.Fatalf("result = %+v, want %+v", result, wantResult)
	}

	events := observer.snapshot()
	if !containsProviderAdmissionEvent(
		events,
		EmbeddingProviderAdmissionEventReleased,
		EmbeddingProviderCallOriginWorker,
		EmbeddingProviderAdmissionOutcomeDownstreamError,
	) {
		t.Fatalf("events = %+v, want downstream error release", events)
	}
}

func TestGatedEmbedderStopsWaitingWhenContextEnds(t *testing.T) {
	gate, err := NewEmbeddingProviderGate(2, 1, 1)
	if err != nil {
		t.Fatalf("NewEmbeddingProviderGate() error = %v", err)
	}
	observer := &fakeEmbeddingProviderAdmissionObserver{}
	provider := newBlockingEmbedder()
	worker, err := NewGatedEmbedder(
		provider,
		gate,
		observer,
		EmbeddingProviderCallOriginWorker,
		0,
	)
	if err != nil {
		t.Fatalf("NewGatedEmbedder() error = %v", err)
	}

	firstResult := make(chan error, 1)
	go func() {
		_, callErr := worker.Embed(context.Background(), embeddingdomain.EmbedRequest{})
		firstResult <- callErr
	}()
	waitForProviderStarts(t, provider.started, 1)

	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Millisecond)
	defer cancel()
	_, err = worker.Embed(ctx, embeddingdomain.EmbedRequest{})
	if !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("waiting Embed() error = %v, want context deadline", err)
	}

	provider.release <- struct{}{}
	if err := <-firstResult; err != nil {
		t.Fatalf("first Embed() error = %v", err)
	}

	events := observer.snapshot()
	if !containsProviderAdmissionEvent(
		events,
		EmbeddingProviderAdmissionEventRejected,
		EmbeddingProviderCallOriginWorker,
		EmbeddingProviderAdmissionOutcomeCanceled,
	) {
		t.Fatalf("events = %+v, want canceled worker wait", events)
	}
}

func TestEmbeddingProviderGateRejectsInvalidConstruction(t *testing.T) {
	invalidCapacities := []struct {
		name     string
		provider int
		worker   int
		online   int
	}{
		{name: "zero provider", provider: 0, worker: 1, online: 1},
		{name: "zero worker", provider: 2, worker: 0, online: 1},
		{name: "zero online", provider: 2, worker: 1, online: 0},
		{name: "class sum exceeds provider", provider: 3, worker: 2, online: 2},
	}
	for _, capacity := range invalidCapacities {
		t.Run(capacity.name, func(t *testing.T) {
			_, err := NewEmbeddingProviderGate(
				capacity.provider,
				capacity.worker,
				capacity.online,
			)
			if !errors.Is(err, ErrEmbeddingProviderGateConfiguration) {
				t.Fatalf("NewEmbeddingProviderGate() error = %v", err)
			}
		})
	}

	gate, err := NewEmbeddingProviderGate(2, 1, 1)
	if err != nil {
		t.Fatalf("NewEmbeddingProviderGate() error = %v", err)
	}
	observer := &fakeEmbeddingProviderAdmissionObserver{}
	provider := fixedResultEmbedder{}

	tests := []struct {
		name        string
		next        embeddingdomain.Embedder
		gate        *EmbeddingProviderGate
		observer    EmbeddingProviderAdmissionObserver
		origin      EmbeddingProviderCallOrigin
		waitTimeout time.Duration
		wantErr     error
	}{
		{name: "missing downstream", gate: gate, observer: observer, origin: EmbeddingProviderCallOriginWorker, wantErr: ErrEmbeddingProviderGateDependencies},
		{name: "missing gate", next: provider, observer: observer, origin: EmbeddingProviderCallOriginWorker, wantErr: ErrEmbeddingProviderGateDependencies},
		{name: "missing observer", next: provider, gate: gate, origin: EmbeddingProviderCallOriginWorker, wantErr: ErrEmbeddingProviderGateDependencies},
		{name: "unknown origin", next: provider, gate: gate, observer: observer, origin: "unknown", wantErr: ErrEmbeddingProviderGateConfiguration},
		{name: "negative timeout", next: provider, gate: gate, observer: observer, origin: EmbeddingProviderCallOriginOnline, waitTimeout: -time.Second, wantErr: ErrEmbeddingProviderGateConfiguration},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			_, err := NewGatedEmbedder(
				test.next,
				test.gate,
				test.observer,
				test.origin,
				test.waitTimeout,
			)
			if !errors.Is(err, test.wantErr) {
				t.Fatalf("NewGatedEmbedder() error = %v, want %v", err, test.wantErr)
			}
		})
	}
}

func waitForProviderStarts(t *testing.T, started <-chan struct{}, count int) {
	t.Helper()
	for range count {
		select {
		case <-started:
		case <-time.After(time.Second):
			t.Fatal("timed out waiting for provider call to start")
		}
	}
}

func containsProviderAdmissionEvent(
	events []EmbeddingProviderAdmissionEvent,
	eventType EmbeddingProviderAdmissionEventType,
	origin EmbeddingProviderCallOrigin,
	outcome EmbeddingProviderAdmissionOutcome,
) bool {
	for _, event := range events {
		if event.Type == eventType &&
			event.Origin == origin &&
			event.Outcome == outcome {
			return true
		}
	}
	return false
}
