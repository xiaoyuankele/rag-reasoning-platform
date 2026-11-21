package embedding

import (
	"context"
	"errors"
	"sync"
	"testing"
	"time"

	embeddingdomain "rag-reasoning-platform/backend/internal/domain/embedding"
)

type fakeDistributedEmbeddingCapacityStore struct {
	mu           sync.Mutex
	acquired     bool
	acquireErr   error
	releaseErr   error
	releaseCalls int
}

func (f *fakeDistributedEmbeddingCapacityStore) AcquireCapacity(
	_ context.Context,
	_ []string,
	_ []int,
	_ time.Duration,
) (string, []int, bool, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.acquireErr != nil {
		return "", nil, false, f.acquireErr
	}
	if f.acquired {
		return "", []int{1, 1}, false, nil
	}
	f.acquired = true
	return "lease-token", []int{1, 1}, true, nil
}

func (f *fakeDistributedEmbeddingCapacityStore) ReleaseCapacity(
	_ context.Context,
	_ []string,
	_ string,
) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.releaseCalls++
	f.acquired = false
	return f.releaseErr
}

func TestDistributedGatedEmbedderAcquiresAndReleasesCapacity(t *testing.T) {
	store := &fakeDistributedEmbeddingCapacityStore{}
	observer := &fakeEmbeddingProviderAdmissionObserver{}
	want := embeddingdomain.EmbedResult{PromptTokens: 2, TotalTokens: 2}
	gate, err := NewDistributedGatedEmbedder(
		fixedResultEmbedder{result: want},
		store,
		observer,
		testDistributedEmbeddingGateConfig(),
	)
	if err != nil {
		t.Fatalf("NewDistributedGatedEmbedder() error = %v", err)
	}

	actual, err := gate.Embed(t.Context(), embeddingdomain.EmbedRequest{})
	if err != nil || actual.PromptTokens != want.PromptTokens {
		t.Fatalf("Embed() = (%+v, %v), want (%+v, nil)", actual, err, want)
	}
	if store.releaseCalls != 1 {
		t.Fatalf("release calls = %d, want 1", store.releaseCalls)
	}
	events := observer.snapshot()
	if !containsProviderAdmissionEvent(
		events,
		EmbeddingProviderDistributedAdmissionEventAdmitted,
		EmbeddingProviderCallOriginOnline,
		"",
	) || !containsProviderAdmissionEvent(
		events,
		EmbeddingProviderDistributedAdmissionEventReleased,
		EmbeddingProviderCallOriginOnline,
		EmbeddingProviderAdmissionOutcomeSucceeded,
	) {
		t.Fatalf("distributed events = %+v", events)
	}
}

func TestDistributedGatedEmbedderRejectsWhenCapacityStaysFull(t *testing.T) {
	store := &fakeDistributedEmbeddingCapacityStore{acquired: true}
	observer := &fakeEmbeddingProviderAdmissionObserver{}
	config := testDistributedEmbeddingGateConfig()
	config.WaitTimeout = 15 * time.Millisecond
	config.RetryInterval = 2 * time.Millisecond
	gate, err := NewDistributedGatedEmbedder(
		fixedResultEmbedder{},
		store,
		observer,
		config,
	)
	if err != nil {
		t.Fatalf("NewDistributedGatedEmbedder() error = %v", err)
	}

	_, err = gate.Embed(t.Context(), embeddingdomain.EmbedRequest{})
	if !errors.Is(err, ErrEmbeddingProviderCapacityExhausted) {
		t.Fatalf("Embed() error = %v, want capacity exhausted", err)
	}
	if store.releaseCalls != 0 {
		t.Fatalf("release calls = %d, want 0", store.releaseCalls)
	}
}

func TestDistributedGatedEmbedderWrapsCoordinationFailure(t *testing.T) {
	redisErr := errors.New("Redis unavailable")
	store := &fakeDistributedEmbeddingCapacityStore{acquireErr: redisErr}
	gate, err := NewDistributedGatedEmbedder(
		fixedResultEmbedder{},
		store,
		&fakeEmbeddingProviderAdmissionObserver{},
		testDistributedEmbeddingGateConfig(),
	)
	if err != nil {
		t.Fatalf("NewDistributedGatedEmbedder() error = %v", err)
	}

	_, err = gate.Embed(t.Context(), embeddingdomain.EmbedRequest{})
	if !errors.Is(err, ErrEmbeddingProviderCapacityExhausted) ||
		!errors.Is(err, ErrEmbeddingProviderCoordinationUnavailable) {
		t.Fatalf("Embed() error = %v, want capacity and coordination errors", err)
	}
}

func TestDistributedGatedEmbedderStopsBeforeRedisWhenContextIsCanceled(t *testing.T) {
	store := &fakeDistributedEmbeddingCapacityStore{}
	gate, err := NewDistributedGatedEmbedder(
		fixedResultEmbedder{},
		store,
		&fakeEmbeddingProviderAdmissionObserver{},
		testDistributedEmbeddingGateConfig(),
	)
	if err != nil {
		t.Fatalf("NewDistributedGatedEmbedder() error = %v", err)
	}
	ctx, cancel := context.WithCancel(t.Context())
	cancel()

	_, err = gate.Embed(ctx, embeddingdomain.EmbedRequest{})
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("Embed() error = %v, want context canceled", err)
	}
	if store.acquired || store.releaseCalls != 0 {
		t.Fatalf("canceled request touched capacity store: %+v", store)
	}
}

func TestNewDistributedGatedEmbedderRejectsInvalidConfiguration(t *testing.T) {
	_, err := NewDistributedGatedEmbedder(
		fixedResultEmbedder{},
		&fakeDistributedEmbeddingCapacityStore{},
		&fakeEmbeddingProviderAdmissionObserver{},
		DistributedEmbeddingProviderGateConfig{},
	)
	if !errors.Is(err, ErrDistributedEmbeddingProviderGateConfiguration) {
		t.Fatalf("constructor error = %v", err)
	}
}

func testDistributedEmbeddingGateConfig() DistributedEmbeddingProviderGateConfig {
	return DistributedEmbeddingProviderGateConfig{
		Namespace:              "test-capacity",
		Provider:               "fake",
		Model:                  "fake-embedding",
		Dimensions:             3,
		Origin:                 EmbeddingProviderCallOriginOnline,
		ProviderMaxConcurrency: 2,
		OriginMaxConcurrency:   1,
		LeaseTTL:               time.Minute,
		RetryInterval:          time.Millisecond,
		WaitTimeout:            20 * time.Millisecond,
	}
}
