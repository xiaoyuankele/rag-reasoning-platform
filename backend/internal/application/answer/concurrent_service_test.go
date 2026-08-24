package answer

import (
	"context"
	"errors"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	accessdomain "rag-reasoning-platform/backend/internal/domain/access"
)

type fakeAnswerer struct {
	answer func(context.Context, accessdomain.OwnerScope, Input) (Output, error)
}

func (f *fakeAnswerer) Answer(
	ctx context.Context,
	scope accessdomain.OwnerScope,
	input Input,
) (Output, error) {
	return f.answer(ctx, scope, input)
}

type recordingAnswerAdmissionObserver struct {
	mu     sync.Mutex
	events []AnswerAdmissionEvent
}

func (o *recordingAnswerAdmissionObserver) ObserveAnswerAdmissionEvent(
	_ context.Context,
	event AnswerAdmissionEvent,
) {
	o.mu.Lock()
	defer o.mu.Unlock()
	o.events = append(o.events, event)
}

func (o *recordingAnswerAdmissionObserver) snapshot() []AnswerAdmissionEvent {
	o.mu.Lock()
	defer o.mu.Unlock()
	return append([]AnswerAdmissionEvent(nil), o.events...)
}

func TestNewConcurrentServiceRejectsInvalidDependenciesAndConfiguration(t *testing.T) {
	fake := &fakeAnswerer{answer: func(
		context.Context,
		accessdomain.OwnerScope,
		Input,
	) (Output, error) {
		return Output{}, nil
	}}
	observer := &recordingAnswerAdmissionObserver{}

	testCases := []struct {
		name      string
		next      answerer
		events    AnswerAdmissionEventObserver
		limits    AnswerAdmissionLimits
		wantError error
	}{
		{name: "missing service", events: observer, limits: testAnswerAdmissionLimits(1, time.Second), wantError: ErrAnswerConcurrencyDependencies},
		{name: "missing observer", next: fake, limits: testAnswerAdmissionLimits(1, time.Second), wantError: ErrAnswerConcurrencyDependencies},
		{name: "zero concurrency", next: fake, events: observer, limits: AnswerAdmissionLimits{MaxConcurrencyPerOwner: 1, MaxWaitersGlobal: 1, MaxWaitersPerOwner: 1, WaitTimeout: time.Second}, wantError: ErrAnswerConcurrencyConfiguration},
		{name: "owner concurrency exceeds global", next: fake, events: observer, limits: AnswerAdmissionLimits{MaxConcurrencyGlobal: 1, MaxConcurrencyPerOwner: 2, MaxWaitersGlobal: 1, MaxWaitersPerOwner: 1, WaitTimeout: time.Second}, wantError: ErrAnswerConcurrencyConfiguration},
		{name: "owner waiting exceeds global", next: fake, events: observer, limits: AnswerAdmissionLimits{MaxConcurrencyGlobal: 1, MaxConcurrencyPerOwner: 1, MaxWaitersGlobal: 1, MaxWaitersPerOwner: 2, WaitTimeout: time.Second}, wantError: ErrAnswerConcurrencyConfiguration},
		{name: "zero timeout", next: fake, events: observer, limits: AnswerAdmissionLimits{MaxConcurrencyGlobal: 1, MaxConcurrencyPerOwner: 1, MaxWaitersGlobal: 1, MaxWaitersPerOwner: 1}, wantError: ErrAnswerConcurrencyConfiguration},
	}

	for _, testCase := range testCases {
		t.Run(testCase.name, func(t *testing.T) {
			_, err := NewConcurrentService(
				testCase.next,
				testCase.events,
				testCase.limits,
			)
			if !errors.Is(err, testCase.wantError) {
				t.Fatalf("NewConcurrentService() error = %v, want %v", err, testCase.wantError)
			}
		})
	}
}

func TestConcurrentServiceLimitsParallelAnswersAndRejectsAfterTimeout(t *testing.T) {
	const maxConcurrency = 2

	started := make(chan struct{}, maxConcurrency)
	release := make(chan struct{})
	var active atomic.Int64
	var maximumActive atomic.Int64
	var callCount atomic.Int64

	fake := &fakeAnswerer{answer: func(
		ctx context.Context,
		_ accessdomain.OwnerScope,
		_ Input,
	) (Output, error) {
		callCount.Add(1)
		current := active.Add(1)
		defer active.Add(-1)
		for {
			previous := maximumActive.Load()
			if current <= previous || maximumActive.CompareAndSwap(previous, current) {
				break
			}
		}
		started <- struct{}{}
		select {
		case <-release:
			return Output{Answer: "ok"}, nil
		case <-ctx.Done():
			return Output{}, ctx.Err()
		}
	}}
	observer := &recordingAnswerAdmissionObserver{}
	service, err := NewConcurrentService(
		fake,
		observer,
		testAnswerAdmissionLimits(maxConcurrency, 40*time.Millisecond),
	)
	if err != nil {
		t.Fatalf("NewConcurrentService() error = %v", err)
	}

	results := make(chan error, maxConcurrency)
	for range maxConcurrency {
		go func() {
			_, answerErr := service.Answer(
				t.Context(),
				testAnswerOwnerScope(t),
				Input{Query: "control"},
			)
			results <- answerErr
		}()
	}
	for range maxConcurrency {
		select {
		case <-started:
		case <-time.After(time.Second):
			t.Fatal("timed out waiting for admitted answer")
		}
	}

	_, rejectedErr := service.Answer(
		t.Context(),
		testAnswerOwnerScope(t),
		Input{Query: "third request"},
	)
	if !errors.Is(rejectedErr, ErrAnswerCapacityExhausted) {
		t.Fatalf("third Answer() error = %v, want ErrAnswerCapacityExhausted", rejectedErr)
	}
	if callCount.Load() != maxConcurrency {
		t.Fatalf("downstream calls = %d, want %d", callCount.Load(), maxConcurrency)
	}
	if maximumActive.Load() != maxConcurrency {
		t.Fatalf("maximum active = %d, want %d", maximumActive.Load(), maxConcurrency)
	}

	close(release)
	for range maxConcurrency {
		if answerErr := <-results; answerErr != nil {
			t.Fatalf("admitted Answer() error = %v, want nil", answerErr)
		}
	}

	events := observer.snapshot()
	assertAnswerAdmissionEventCount(t, events, AnswerAdmissionEventAdmitted, 2)
	assertAnswerAdmissionEventCount(t, events, AnswerAdmissionEventRejected, 1)
	assertAnswerAdmissionEventCount(t, events, AnswerAdmissionEventReleased, 2)

	foundCapacityTimeout := false
	for _, event := range events {
		if event.Type == AnswerAdmissionEventRejected &&
			event.Outcome == AnswerAdmissionOutcomeCapacityTimeout {
			foundCapacityTimeout = true
			if event.InFlight != maxConcurrency || event.MaxConcurrency != maxConcurrency {
				t.Fatalf("rejected event = %+v, want full capacity", event)
			}
		}
	}
	if !foundCapacityTimeout {
		t.Fatal("capacity timeout event was not observed")
	}
}

func TestConcurrentServiceReleasesSlotAfterDownstreamError(t *testing.T) {
	downstreamError := errors.New("generation failed")
	var callCount atomic.Int64
	fake := &fakeAnswerer{answer: func(
		context.Context,
		accessdomain.OwnerScope,
		Input,
	) (Output, error) {
		if callCount.Add(1) == 1 {
			return Output{}, downstreamError
		}
		return Output{Answer: "recovered"}, nil
	}}
	observer := &recordingAnswerAdmissionObserver{}
	service, err := NewConcurrentService(
		fake,
		observer,
		testAnswerAdmissionLimits(1, time.Second),
	)
	if err != nil {
		t.Fatalf("NewConcurrentService() error = %v", err)
	}

	if _, err := service.Answer(t.Context(), testAnswerOwnerScope(t), Input{}); !errors.Is(err, downstreamError) {
		t.Fatalf("first Answer() error = %v, want downstream error", err)
	}
	result, err := service.Answer(t.Context(), testAnswerOwnerScope(t), Input{})
	if err != nil || result.Answer != "recovered" {
		t.Fatalf("second Answer() = %+v, %v; want recovered, nil", result, err)
	}

	events := observer.snapshot()
	foundDownstreamError := false
	for _, event := range events {
		if event.Type == AnswerAdmissionEventReleased &&
			event.Outcome == AnswerAdmissionOutcomeDownstreamError {
			foundDownstreamError = true
		}
	}
	if !foundDownstreamError {
		t.Fatal("downstream error release event was not observed")
	}
}

func TestConcurrentServiceRejectsInvalidOwnerWithoutCapacityEvent(t *testing.T) {
	fake := &fakeAnswerer{answer: func(
		context.Context,
		accessdomain.OwnerScope,
		Input,
	) (Output, error) {
		t.Fatal("downstream service must not receive an invalid OwnerScope")
		return Output{}, nil
	}}
	observer := &recordingAnswerAdmissionObserver{}
	service, err := NewConcurrentService(
		fake,
		observer,
		testAnswerAdmissionLimits(1, time.Second),
	)
	if err != nil {
		t.Fatalf("NewConcurrentService() error = %v", err)
	}

	_, err = service.Answer(t.Context(), accessdomain.OwnerScope{}, Input{})
	if !errors.Is(err, accessdomain.ErrInvalidOwnerScope) {
		t.Fatalf("Answer() error = %v, want ErrInvalidOwnerScope", err)
	}
	if events := observer.snapshot(); len(events) != 0 {
		t.Fatalf("invalid OwnerScope events = %+v, want none", events)
	}
}

func TestConcurrentServiceStopsWaitingWhenContextIsCanceled(t *testing.T) {
	started := make(chan struct{})
	release := make(chan struct{})
	fake := &fakeAnswerer{answer: func(
		ctx context.Context,
		_ accessdomain.OwnerScope,
		_ Input,
	) (Output, error) {
		close(started)
		select {
		case <-release:
			return Output{}, nil
		case <-ctx.Done():
			return Output{}, ctx.Err()
		}
	}}
	observer := &recordingAnswerAdmissionObserver{}
	service, err := NewConcurrentService(
		fake,
		observer,
		testAnswerAdmissionLimits(1, time.Second),
	)
	if err != nil {
		t.Fatalf("NewConcurrentService() error = %v", err)
	}

	firstDone := make(chan error, 1)
	go func() {
		_, answerErr := service.Answer(t.Context(), testAnswerOwnerScope(t), Input{})
		firstDone <- answerErr
	}()
	<-started

	waitingContext, cancel := context.WithCancel(t.Context())
	cancel()
	_, canceledErr := service.Answer(waitingContext, testAnswerOwnerScope(t), Input{})
	if !errors.Is(canceledErr, context.Canceled) {
		t.Fatalf("waiting Answer() error = %v, want context.Canceled", canceledErr)
	}

	close(release)
	if err := <-firstDone; err != nil {
		t.Fatalf("first Answer() error = %v, want nil", err)
	}
}

func testAnswerAdmissionLimits(
	maxConcurrency int,
	waitTimeout time.Duration,
) AnswerAdmissionLimits {
	return AnswerAdmissionLimits{
		MaxConcurrencyGlobal:   maxConcurrency,
		MaxConcurrencyPerOwner: maxConcurrency,
		MaxWaitersGlobal:       10,
		MaxWaitersPerOwner:     10,
		WaitTimeout:            waitTimeout,
	}
}

func assertAnswerAdmissionEventCount(
	t *testing.T,
	events []AnswerAdmissionEvent,
	eventType AnswerAdmissionEventType,
	want int,
) {
	t.Helper()
	actual := 0
	for _, event := range events {
		if event.Type == eventType {
			actual++
		}
	}
	if actual != want {
		t.Fatalf("event %q count = %d, want %d; events = %+v", eventType, actual, want, events)
	}
}
