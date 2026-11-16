package agent

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	answerapplication "rag-reasoning-platform/backend/internal/application/answer"
)

type fakeModelCapacityStore struct {
	mu                sync.Mutex
	alwaysFull        bool
	acquireErr        error
	releaseErr        error
	acquireCalls      int
	releaseCalls      int
	releaseContextErr error
	keys              []string
	limits            []int
	leaseTTL          time.Duration
}

func (f *fakeModelCapacityStore) AcquireCapacity(
	_ context.Context,
	keys []string,
	limits []int,
	leaseTTL time.Duration,
) (string, []int, bool, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.acquireCalls++
	f.keys = append([]string(nil), keys...)
	f.limits = append([]int(nil), limits...)
	f.leaseTTL = leaseTTL
	if f.acquireErr != nil {
		return "", []int{0, 0}, false, f.acquireErr
	}
	if f.alwaysFull {
		return "", []int{2, 1}, false, nil
	}
	return fmt.Sprintf("token-%d", f.acquireCalls), []int{1, 1}, true, nil
}

func (f *fakeModelCapacityStore) ReleaseCapacity(
	ctx context.Context,
	keys []string,
	_ string,
) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.releaseCalls++
	f.releaseContextErr = ctx.Err()
	if fmt.Sprint(keys) != fmt.Sprint(f.keys) {
		return errors.New("release keys differ")
	}
	return f.releaseErr
}

type fakeCapacityAgentModel struct {
	calls    atomic.Int64
	decision ModelDecision
	err      error
}

type capacityAgentModelFunc func(context.Context, ModelRequest) (ModelDecision, error)

func (f capacityAgentModelFunc) Decide(ctx context.Context, request ModelRequest) (ModelDecision, error) {
	return f(ctx, request)
}

func (f *fakeCapacityAgentModel) Decide(context.Context, ModelRequest) (ModelDecision, error) {
	f.calls.Add(1)
	return f.decision, f.err
}

type recordingModelCapacityObserver struct {
	mu     sync.Mutex
	events []ModelCapacityEvent
}

func (r *recordingModelCapacityObserver) ObserveModelCapacityEvent(_ context.Context, event ModelCapacityEvent) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.events = append(r.events, event)
}

func (r *recordingModelCapacityObserver) snapshot() []ModelCapacityEvent {
	r.mu.Lock()
	defer r.mu.Unlock()
	return append([]ModelCapacityEvent(nil), r.events...)
}

func TestCapacityModelAcquiresCallsProviderAndReleases(t *testing.T) {
	next := &fakeCapacityAgentModel{decision: validCapacityDecision()}
	store := &fakeModelCapacityStore{}
	observer := &recordingModelCapacityObserver{}
	keys, err := answerapplication.NewProviderCapacityKeyBuilder("rag-capacity", "dashscope", "qwen3.6-flash")
	if err != nil {
		t.Fatalf("NewProviderCapacityKeyBuilder() error = %v", err)
	}
	model, err := NewCapacityModel(next, store, observer, keys, testModelCapacityConfig())
	if err != nil {
		t.Fatalf("NewCapacityModel() error = %v", err)
	}
	decision, err := model.Decide(t.Context(), ModelRequest{OwnerUserID: 42})
	if err != nil || decision.Final == nil || next.calls.Load() != 1 {
		t.Fatalf("Decide() = %+v, %v calls=%d", decision, err, next.calls.Load())
	}
	store.mu.Lock()
	if store.acquireCalls != 1 || store.releaseCalls != 1 || fmt.Sprint(store.limits) != "[2 1]" ||
		store.leaseTTL != testModelCapacityConfig().LeaseTTL {
		t.Fatalf("store = %+v", store)
	}
	wantKeys := "[{rag-capacity-answer-987b8848e27794a9bb1856e5}:global {rag-capacity-answer-987b8848e27794a9bb1856e5}:owner:42]"
	if fmt.Sprint(store.keys) != wantKeys {
		t.Fatalf("Agent keys = %v, want %s", store.keys, wantKeys)
	}
	store.mu.Unlock()
	events := observer.snapshot()
	if len(events) != 2 || events[0].Type != ModelCapacityEventAdmitted ||
		events[1].Type != ModelCapacityEventReleased || events[1].Outcome != ModelCapacityOutcomeSucceeded ||
		events[1].InFlight != 0 || events[1].OwnerInFlight != 0 {
		t.Fatalf("events = %+v", events)
	}
}

func TestCapacityModelFailsClosedBeforeProvider(t *testing.T) {
	for _, test := range []struct {
		name        string
		store       *fakeModelCapacityStore
		request     ModelRequest
		keyBuilder  func(int64) ([]string, error)
		want        error
		wantOutcome ModelCapacityOutcome
	}{
		{name: "capacity full", store: &fakeModelCapacityStore{alwaysFull: true}, request: ModelRequest{OwnerUserID: 42}, want: ErrModelTemporarilyUnavailable, wantOutcome: ModelCapacityOutcomeCapacity},
		{name: "Redis error", store: &fakeModelCapacityStore{acquireErr: errors.New("redis-secret")}, request: ModelRequest{OwnerUserID: 42}, want: ErrModelTemporarilyUnavailable, wantOutcome: ModelCapacityOutcomeCoordination},
		{name: "invalid owner", store: &fakeModelCapacityStore{}, request: ModelRequest{}, want: ErrModelRequestRejected, wantOutcome: ModelCapacityOutcomeInvalidRequest},
		{name: "key builder error", store: &fakeModelCapacityStore{}, request: ModelRequest{OwnerUserID: 42}, keyBuilder: func(int64) ([]string, error) {
			return nil, errors.New("key-secret")
		}, want: ErrModelRequestRejected, wantOutcome: ModelCapacityOutcomeInvalidRequest},
	} {
		t.Run(test.name, func(t *testing.T) {
			next := &fakeCapacityAgentModel{decision: validCapacityDecision()}
			observer := &recordingModelCapacityObserver{}
			builder := testModelCapacityKeys
			if test.keyBuilder != nil {
				builder = test.keyBuilder
			}
			config := testModelCapacityConfig()
			config.WaitTimeout = 4 * time.Millisecond
			config.RetryInterval = time.Millisecond
			model, err := NewCapacityModel(next, test.store, observer, builder, config)
			if err != nil {
				t.Fatalf("NewCapacityModel() error = %v", err)
			}
			_, err = model.Decide(t.Context(), test.request)
			if !errors.Is(err, test.want) || next.calls.Load() != 0 ||
				strings.Contains(err.Error(), "redis-secret") || strings.Contains(err.Error(), "key-secret") {
				t.Fatalf("Decide() error=%v calls=%d", err, next.calls.Load())
			}
			test.store.mu.Lock()
			if test.store.releaseCalls != 0 {
				t.Fatalf("release calls = %d", test.store.releaseCalls)
			}
			if (test.name == "invalid owner" || test.name == "key builder error") && test.store.acquireCalls != 0 {
				t.Fatalf("acquire calls = %d", test.store.acquireCalls)
			}
			test.store.mu.Unlock()
			events := observer.snapshot()
			if len(events) != 1 || events[0].Type != ModelCapacityEventRejected || events[0].Outcome != test.wantOutcome {
				t.Fatalf("events = %+v", events)
			}
		})
	}
}

func TestCapacityModelPreservesContextAndDownstreamErrors(t *testing.T) {
	t.Run("pre-canceled", func(t *testing.T) {
		ctx, cancel := context.WithCancel(t.Context())
		cancel()
		next := &fakeCapacityAgentModel{}
		store := &fakeModelCapacityStore{}
		observer := &recordingModelCapacityObserver{}
		model, _ := NewCapacityModel(next, store, observer, testModelCapacityKeys, testModelCapacityConfig())
		_, err := model.Decide(ctx, ModelRequest{OwnerUserID: 42})
		if !errors.Is(err, context.Canceled) || next.calls.Load() != 0 || store.acquireCalls != 0 || store.releaseCalls != 0 {
			t.Fatalf("Decide(canceled) error=%v calls=%d store=%+v", err, next.calls.Load(), store)
		}
		events := observer.snapshot()
		if len(events) != 1 || events[0].Outcome != ModelCapacityOutcomeCanceled {
			t.Fatalf("canceled events = %+v", events)
		}
	})

	t.Run("caller deadline", func(t *testing.T) {
		ctx, cancel := context.WithTimeout(t.Context(), 4*time.Millisecond)
		defer cancel()
		next := &fakeCapacityAgentModel{}
		store := &fakeModelCapacityStore{alwaysFull: true}
		observer := &recordingModelCapacityObserver{}
		config := testModelCapacityConfig()
		config.WaitTimeout = time.Second
		config.RetryInterval = 10 * time.Millisecond
		model, _ := NewCapacityModel(next, store, observer, testModelCapacityKeys, config)
		_, err := model.Decide(ctx, ModelRequest{OwnerUserID: 42})
		if !errors.Is(err, context.DeadlineExceeded) || next.calls.Load() != 0 || store.releaseCalls != 0 {
			t.Fatalf("Decide(deadline) error=%v calls=%d releases=%d", err, next.calls.Load(), store.releaseCalls)
		}
		events := observer.snapshot()
		if len(events) != 1 || events[0].Outcome != ModelCapacityOutcomeCanceled {
			t.Fatalf("deadline events = %+v", events)
		}
	})

	for _, downstreamErr := range []error{
		ErrModelTemporarilyUnavailable,
		ErrModelRequestRejected,
		ErrInvalidModelDecision,
		context.Canceled,
		context.DeadlineExceeded,
	} {
		t.Run(downstreamErr.Error(), func(t *testing.T) {
			next := &fakeCapacityAgentModel{err: downstreamErr}
			store := &fakeModelCapacityStore{}
			observer := &recordingModelCapacityObserver{}
			model, _ := NewCapacityModel(next, store, observer, testModelCapacityKeys, testModelCapacityConfig())
			_, err := model.Decide(t.Context(), ModelRequest{OwnerUserID: 42})
			if !errors.Is(err, downstreamErr) || next.calls.Load() != 1 || store.releaseCalls != 1 {
				t.Fatalf("Decide() error=%v calls=%d releases=%d", err, next.calls.Load(), store.releaseCalls)
			}
			events := observer.snapshot()
			if len(events) != 2 || events[0].Type != ModelCapacityEventAdmitted ||
				events[1].Type != ModelCapacityEventReleased ||
				events[1].Outcome != ModelCapacityOutcomeDownstreamError {
				t.Fatalf("downstream events = %+v", events)
			}
		})
	}
}

func TestCapacityModelReleasesWithDetachedContextAfterCallerCancellation(t *testing.T) {
	started := make(chan struct{})
	next := capacityAgentModelFunc(func(ctx context.Context, _ ModelRequest) (ModelDecision, error) {
		close(started)
		<-ctx.Done()
		return ModelDecision{}, ctx.Err()
	})
	store := &fakeModelCapacityStore{}
	observer := &recordingModelCapacityObserver{}
	model, _ := NewCapacityModel(next, store, observer, testModelCapacityKeys, testModelCapacityConfig())
	ctx, cancel := context.WithCancel(t.Context())
	done := make(chan error, 1)
	go func() {
		_, err := model.Decide(ctx, ModelRequest{OwnerUserID: 42})
		done <- err
	}()
	<-started
	cancel()
	if err := <-done; !errors.Is(err, context.Canceled) {
		t.Fatalf("Decide() error = %v", err)
	}
	store.mu.Lock()
	defer store.mu.Unlock()
	if store.releaseCalls != 1 || store.releaseContextErr != nil {
		t.Fatalf("release calls/context = %d/%v", store.releaseCalls, store.releaseContextErr)
	}
}

func TestCapacityModelReleaseErrorPreservesSuccessfulDecision(t *testing.T) {
	next := &fakeCapacityAgentModel{decision: validCapacityDecision()}
	store := &fakeModelCapacityStore{releaseErr: errors.New("release-secret")}
	observer := &recordingModelCapacityObserver{}
	model, _ := NewCapacityModel(next, store, observer, testModelCapacityKeys, testModelCapacityConfig())
	decision, err := model.Decide(t.Context(), ModelRequest{OwnerUserID: 42})
	if err != nil || decision.Final == nil || next.calls.Load() != 1 || store.releaseCalls != 1 {
		t.Fatalf("Decide() = %+v, %v calls=%d releases=%d", decision, err, next.calls.Load(), store.releaseCalls)
	}
	events := observer.snapshot()
	if len(events) != 2 || events[1].Outcome != ModelCapacityOutcomeReleaseError ||
		events[1].ErrorCategory != ModelSafeErrorCoordination ||
		events[1].InFlight != 1 || events[1].OwnerInFlight != 1 {
		t.Fatalf("release events = %+v", events)
	}
}

func TestNewCapacityModelValidatesDependenciesAndConfig(t *testing.T) {
	observer := &recordingModelCapacityObserver{}
	store := &fakeModelCapacityStore{}
	next := &fakeCapacityAgentModel{}
	if _, err := NewCapacityModel(nil, store, observer, testModelCapacityKeys, testModelCapacityConfig()); !errors.Is(err, ErrModelCapacityDependencies) {
		t.Fatalf("nil dependency error = %v", err)
	}
	if _, err := NewCapacityModel(next, store, observer, testModelCapacityKeys, ModelCapacityConfig{}); !errors.Is(err, ErrModelCapacityConfiguration) {
		t.Fatalf("invalid config error = %v", err)
	}
}

func validCapacityDecision() ModelDecision {
	return ModelDecision{
		Final: &ModelFinalAnswer{Text: "answer", EvidenceRefs: []string{"step-1:item-1"}},
		Usage: TokenUsage{PromptTokens: 1, CompletionTokens: 1, TotalTokens: 2},
	}
}

func testModelCapacityConfig() ModelCapacityConfig {
	return ModelCapacityConfig{
		MaxConcurrencyGlobal: 2, MaxConcurrencyPerOwner: 1,
		LeaseTTL: time.Minute, RetryInterval: time.Millisecond, WaitTimeout: 20 * time.Millisecond,
	}
}

func testModelCapacityKeys(ownerID int64) ([]string, error) {
	if ownerID <= 0 {
		return nil, errors.New("invalid owner")
	}
	return []string{"{test}:global", fmt.Sprintf("{test}:owner:%d", ownerID)}, nil
}
