package answer

import (
	"context"
	"errors"
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
}

func (f *fakeDistributedAnswerCapacityStore) AcquireCapacity(
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
	return "answer-token", []int{1, 1}, true, nil
}

func (f *fakeDistributedAnswerCapacityStore) ReleaseCapacity(
	_ context.Context,
	_ []string,
	_ string,
) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.releaseCalls++
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
	events := observer.snapshot()
	if len(events) != 2 ||
		events[0].Type != AnswerDistributedAdmissionEventAdmitted ||
		events[1].Type != AnswerDistributedAdmissionEventReleased {
		t.Fatalf("distributed events = %+v", events)
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
