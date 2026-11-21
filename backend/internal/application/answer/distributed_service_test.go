package answer

import (
	"context"
	"errors"
	"fmt"
	"sync"
	"testing"
	"time"

	accessdomain "rag-reasoning-platform/backend/internal/domain/access"
)

type fakeDistributedAnswerCapacityStore struct {
	mu           sync.Mutex
	acquired     bool
	acquireErr   error
	releaseCalls int
	acquireKeys  []string
	releaseKeys  []string
}

func (f *fakeDistributedAnswerCapacityStore) AcquireCapacity(
	_ context.Context,
	keys []string,
	_ []int,
	_ time.Duration,
) (string, []int, bool, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.acquireKeys = append([]string(nil), keys...)
	if f.acquireErr != nil {
		return "", nil, false, f.acquireErr
	}
	if f.acquired {
		return "", []int{1, 1}, false, nil
	}
	f.acquired = true
	return "answer-token", []int{1, 1}, true, nil
}

func (f *fakeDistributedAnswerCapacityStore) ReleaseCapacity(
	_ context.Context,
	keys []string,
	_ string,
) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.releaseCalls++
	f.releaseKeys = append([]string(nil), keys...)
	f.acquired = false
	return nil
}

func TestDistributedAnswerServiceAcquiresAndReleasesCapacity(t *testing.T) {
	store := &fakeDistributedAnswerCapacityStore{}
	observer := &recordingAnswerAdmissionObserver{}
	next := &fakeAnswerer{answer: func(
		context.Context,
		accessdomain.OwnerScope,
		Input,
	) (Output, error) {
		return Output{Answer: "distributed answer"}, nil
	}}
	service, err := NewDistributedService(
		next,
		store,
		observer,
		testDistributedAnswerConfig(),
	)
	if err != nil {
		t.Fatalf("NewDistributedService() error = %v", err)
	}

	output, err := service.Answer(t.Context(), testAnswerOwnerScope(t), Input{})
	if err != nil || output.Answer != "distributed answer" {
		t.Fatalf("Answer() = (%+v, %v)", output, err)
	}
	if store.releaseCalls != 1 {
		t.Fatalf("release calls = %d, want 1", store.releaseCalls)
	}
	wantKeys := []string{
		"{test-capacity-answer-5eea56c918953fe3c2b162f1}:global",
		"{test-capacity-answer-5eea56c918953fe3c2b162f1}:owner:42",
	}
	if fmt.Sprint(store.acquireKeys) != fmt.Sprint(wantKeys) ||
		fmt.Sprint(store.releaseKeys) != fmt.Sprint(wantKeys) {
		t.Fatalf("capacity keys acquire=%v release=%v want=%v", store.acquireKeys, store.releaseKeys, wantKeys)
	}
	events := observer.snapshot()
	if len(events) != 2 ||
		events[0].Type != AnswerDistributedAdmissionEventAdmitted ||
		events[1].Type != AnswerDistributedAdmissionEventReleased {
		t.Fatalf("distributed events = %+v", events)
	}
}

func TestProviderCapacityKeyBuilderPreservesLegacyKeysAndIsolatesModels(t *testing.T) {
	builder, err := NewProviderCapacityKeyBuilder("rag-capacity", "dashscope", "qwen3.6-flash")
	if err != nil {
		t.Fatalf("NewProviderCapacityKeyBuilder() error = %v", err)
	}
	keys, err := builder(42)
	if err != nil {
		t.Fatalf("builder() error = %v", err)
	}
	want := []string{
		"{rag-capacity-answer-987b8848e27794a9bb1856e5}:global",
		"{rag-capacity-answer-987b8848e27794a9bb1856e5}:owner:42",
	}
	if fmt.Sprint(keys) != fmt.Sprint(want) {
		t.Fatalf("keys = %v, want %v", keys, want)
	}
	sameBuilder, _ := NewProviderCapacityKeyBuilder("rag-capacity", "dashscope", "qwen3.6-flash")
	sameKeys, _ := sameBuilder(42)
	otherBuilder, _ := NewProviderCapacityKeyBuilder("rag-capacity", "dashscope", "qwen-plus")
	otherKeys, _ := otherBuilder(42)
	if fmt.Sprint(sameKeys) != fmt.Sprint(keys) || fmt.Sprint(otherKeys) == fmt.Sprint(keys) {
		t.Fatalf("same=%v other=%v base=%v", sameKeys, otherKeys, keys)
	}
	if _, err := builder(0); !errors.Is(err, accessdomain.ErrInvalidOwnerScope) {
		t.Fatalf("builder(invalid owner) error = %v", err)
	}
}

func TestDistributedAnswerServiceHonorsLocalAdmissionDeadline(t *testing.T) {
	store := &fakeDistributedAnswerCapacityStore{acquired: true}
	service, err := NewDistributedService(
		&fakeAnswerer{answer: func(context.Context, accessdomain.OwnerScope, Input) (Output, error) {
			t.Fatal("downstream answerer must not run without capacity")
			return Output{}, nil
		}},
		store,
		&recordingAnswerAdmissionObserver{},
		testDistributedAnswerConfig(),
	)
	if err != nil {
		t.Fatalf("NewDistributedService() error = %v", err)
	}

	deadlineContext := context.WithValue(
		t.Context(),
		answerAdmissionDeadlineContextKey{},
		time.Now().Add(15*time.Millisecond),
	)
	_, err = service.Answer(deadlineContext, testAnswerOwnerScope(t), Input{})
	if !errors.Is(err, ErrAnswerCapacityExhausted) {
		t.Fatalf("Answer() error = %v, want capacity exhausted", err)
	}
}

func TestDistributedAnswerServiceWrapsCoordinationFailure(t *testing.T) {
	store := &fakeDistributedAnswerCapacityStore{
		acquireErr: errors.New("Redis unavailable"),
	}
	service, err := NewDistributedService(
		&fakeAnswerer{answer: func(context.Context, accessdomain.OwnerScope, Input) (Output, error) {
			return Output{}, nil
		}},
		store,
		&recordingAnswerAdmissionObserver{},
		testDistributedAnswerConfig(),
	)
	if err != nil {
		t.Fatalf("NewDistributedService() error = %v", err)
	}

	_, err = service.Answer(t.Context(), testAnswerOwnerScope(t), Input{})
	if !errors.Is(err, ErrAnswerCapacityExhausted) ||
		!errors.Is(err, ErrAnswerCapacityCoordinationUnavailable) {
		t.Fatalf("Answer() error = %v, want capacity and coordination errors", err)
	}
}

func TestDistributedAnswerServiceStopsBeforeRedisWhenContextIsCanceled(t *testing.T) {
	store := &fakeDistributedAnswerCapacityStore{}
	service, err := NewDistributedService(
		&fakeAnswerer{answer: func(context.Context, accessdomain.OwnerScope, Input) (Output, error) {
			t.Fatal("canceled request must not reach downstream answerer")
			return Output{}, nil
		}},
		store,
		&recordingAnswerAdmissionObserver{},
		testDistributedAnswerConfig(),
	)
	if err != nil {
		t.Fatalf("NewDistributedService() error = %v", err)
	}
	ctx, cancel := context.WithCancel(t.Context())
	cancel()

	_, err = service.Answer(ctx, testAnswerOwnerScope(t), Input{})
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("Answer() error = %v, want context canceled", err)
	}
	if store.acquired || store.releaseCalls != 0 {
		t.Fatalf("canceled request touched capacity store: %+v", store)
	}
}

func TestNewDistributedAnswerServiceRejectsInvalidConfiguration(t *testing.T) {
	_, err := NewDistributedService(
		&fakeAnswerer{answer: func(context.Context, accessdomain.OwnerScope, Input) (Output, error) {
			return Output{}, nil
		}},
		&fakeDistributedAnswerCapacityStore{},
		&recordingAnswerAdmissionObserver{},
		DistributedAnswerConfig{},
	)
	if !errors.Is(err, ErrDistributedAnswerConfiguration) {
		t.Fatalf("constructor error = %v", err)
	}
}

func testDistributedAnswerConfig() DistributedAnswerConfig {
	return DistributedAnswerConfig{
		Namespace:              "test-capacity",
		Provider:               "fake",
		Model:                  "fake-generation",
		MaxConcurrencyGlobal:   2,
		MaxConcurrencyPerOwner: 1,
		LeaseTTL:               time.Minute,
		RetryInterval:          time.Millisecond,
		WaitTimeout:            50 * time.Millisecond,
	}
}
