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

func TestConcurrentServiceLimitsOneOwnerWithoutBlockingAnother(t *testing.T) {
	started := make(chan int64, 4)
	release := make(chan struct{})
	fake := &fakeAnswerer{answer: func(
		ctx context.Context,
		scope accessdomain.OwnerScope,
		_ Input,
	) (Output, error) {
		started <- scope.OwnerUserID()
		select {
		case <-release:
			return Output{Answer: "ok"}, nil
		case <-ctx.Done():
			return Output{}, ctx.Err()
		}
	}}
	service := newTestConcurrentService(t, fake, AnswerAdmissionLimits{
		MaxConcurrencyGlobal:   3,
		MaxConcurrencyPerOwner: 2,
		MaxWaitersGlobal:       10,
		MaxWaitersPerOwner:     5,
		WaitTimeout:            time.Second,
	})

	ownerA := testAnswerOwnerScopeForID(t, 101)
	ownerB := testAnswerOwnerScopeForID(t, 202)
	done := make(chan error, 4)
	for range 3 {
		go runTestAnswer(service, ownerA, done)
	}

	for range 2 {
		if ownerID := receiveStartedOwner(t, started); ownerID != ownerA.OwnerUserID() {
			t.Fatalf("started owner = %d, want owner A", ownerID)
		}
	}
	waitForAdmissionSnapshot(t, service, ownerA.OwnerUserID(), func(snapshot answerAdmissionSnapshot) bool {
		return snapshot.ownerInFlight == 2 && snapshot.ownerWaiting == 1
	})

	go runTestAnswer(service, ownerB, done)
	if ownerID := receiveStartedOwner(t, started); ownerID != ownerB.OwnerUserID() {
		t.Fatalf("third active owner = %d, want owner B", ownerID)
	}

	close(release)
	for range 4 {
		if err := <-done; err != nil {
			t.Fatalf("Answer() error = %v, want nil", err)
		}
	}
}

func TestConcurrentServiceRejectsWhenOwnerWaitingBudgetIsFull(t *testing.T) {
	started := make(chan int64, 2)
	release := make(chan struct{})
	fake := blockingAnswerer(started, release)
	service := newTestConcurrentService(t, fake, AnswerAdmissionLimits{
		MaxConcurrencyGlobal:   1,
		MaxConcurrencyPerOwner: 1,
		MaxWaitersGlobal:       10,
		MaxWaitersPerOwner:     1,
		WaitTimeout:            time.Second,
	})
	owner := testAnswerOwnerScopeForID(t, 303)
	done := make(chan error, 2)

	go runTestAnswer(service, owner, done)
	receiveStartedOwner(t, started)
	go runTestAnswer(service, owner, done)
	waitForAdmissionSnapshot(t, service, owner.OwnerUserID(), func(snapshot answerAdmissionSnapshot) bool {
		return snapshot.ownerWaiting == 1
	})

	_, err := service.Answer(t.Context(), owner, Input{Query: "overflow"})
	if !errors.Is(err, ErrAnswerOwnerCapacityExhausted) {
		t.Fatalf("overflow Answer() error = %v, want ErrAnswerOwnerCapacityExhausted", err)
	}

	close(release)
	for range 2 {
		if err := <-done; err != nil {
			t.Fatalf("admitted Answer() error = %v", err)
		}
	}
}

func TestConcurrentServiceRejectsWhenGlobalWaitingBudgetIsFull(t *testing.T) {
	started := make(chan int64, 2)
	release := make(chan struct{})
	fake := blockingAnswerer(started, release)
	service := newTestConcurrentService(t, fake, AnswerAdmissionLimits{
		MaxConcurrencyGlobal:   1,
		MaxConcurrencyPerOwner: 1,
		MaxWaitersGlobal:       1,
		MaxWaitersPerOwner:     1,
		WaitTimeout:            time.Second,
	})
	ownerA := testAnswerOwnerScopeForID(t, 401)
	ownerB := testAnswerOwnerScopeForID(t, 402)
	ownerC := testAnswerOwnerScopeForID(t, 403)
	done := make(chan error, 2)

	go runTestAnswer(service, ownerA, done)
	receiveStartedOwner(t, started)
	go runTestAnswer(service, ownerB, done)
	waitForAdmissionSnapshot(t, service, ownerB.OwnerUserID(), func(snapshot answerAdmissionSnapshot) bool {
		return snapshot.waiting == 1
	})

	_, err := service.Answer(t.Context(), ownerC, Input{Query: "overflow"})
	if !errors.Is(err, ErrAnswerCapacityExhausted) {
		t.Fatalf("overflow Answer() error = %v, want ErrAnswerCapacityExhausted", err)
	}

	close(release)
	for range 2 {
		if err := <-done; err != nil {
			t.Fatalf("admitted Answer() error = %v", err)
		}
	}
}

func TestConcurrentServiceDispatchesWaitingOwnersRoundRobin(t *testing.T) {
	started := make(chan string, 4)
	releases := map[string]chan struct{}{
		"seed": make(chan struct{}),
		"a1":   make(chan struct{}),
		"a2":   make(chan struct{}),
		"b1":   make(chan struct{}),
	}
	fake := &fakeAnswerer{answer: func(
		ctx context.Context,
		_ accessdomain.OwnerScope,
		input Input,
	) (Output, error) {
		started <- input.Query
		select {
		case <-releases[input.Query]:
			return Output{Answer: "ok"}, nil
		case <-ctx.Done():
			return Output{}, ctx.Err()
		}
	}}
	service := newTestConcurrentService(t, fake, AnswerAdmissionLimits{
		MaxConcurrencyGlobal:   1,
		MaxConcurrencyPerOwner: 1,
		MaxWaitersGlobal:       10,
		MaxWaitersPerOwner:     5,
		WaitTimeout:            2 * time.Second,
	})
	seedOwner := testAnswerOwnerScopeForID(t, 500)
	ownerA := testAnswerOwnerScopeForID(t, 501)
	ownerB := testAnswerOwnerScopeForID(t, 502)
	done := make(chan error, 4)

	go runNamedTestAnswer(service, seedOwner, "seed", done)
	if query := receiveStartedQuery(t, started); query != "seed" {
		t.Fatalf("first query = %q, want seed", query)
	}
	go runNamedTestAnswer(service, ownerA, "a1", done)
	waitForAdmissionSnapshot(t, service, ownerA.OwnerUserID(), func(snapshot answerAdmissionSnapshot) bool {
		return snapshot.ownerWaiting == 1
	})
	go runNamedTestAnswer(service, ownerA, "a2", done)
	waitForAdmissionSnapshot(t, service, ownerA.OwnerUserID(), func(snapshot answerAdmissionSnapshot) bool {
		return snapshot.ownerWaiting == 2
	})
	go runNamedTestAnswer(service, ownerB, "b1", done)
	waitForAdmissionSnapshot(t, service, ownerB.OwnerUserID(), func(snapshot answerAdmissionSnapshot) bool {
		return snapshot.waiting == 3 && snapshot.ownerWaiting == 1
	})

	close(releases["seed"])
	if query := receiveStartedQuery(t, started); query != "a1" {
		t.Fatalf("second query = %q, want a1", query)
	}
	close(releases["a1"])
	if query := receiveStartedQuery(t, started); query != "b1" {
		t.Fatalf("third query = %q, want b1 for Owner round-robin", query)
	}
	close(releases["b1"])
	if query := receiveStartedQuery(t, started); query != "a2" {
		t.Fatalf("fourth query = %q, want a2", query)
	}
	close(releases["a2"])

	for range 4 {
		if err := <-done; err != nil {
			t.Fatalf("Answer() error = %v", err)
		}
	}
}

func TestConcurrentServiceRemovesCanceledQueuedRequest(t *testing.T) {
	started := make(chan int64, 1)
	release := make(chan struct{})
	service := newTestConcurrentService(t, blockingAnswerer(started, release), AnswerAdmissionLimits{
		MaxConcurrencyGlobal:   1,
		MaxConcurrencyPerOwner: 1,
		MaxWaitersGlobal:       2,
		MaxWaitersPerOwner:     1,
		WaitTimeout:            time.Second,
	})
	activeOwner := testAnswerOwnerScopeForID(t, 601)
	waitingOwner := testAnswerOwnerScopeForID(t, 602)
	activeDone := make(chan error, 1)
	go runTestAnswer(service, activeOwner, activeDone)
	receiveStartedOwner(t, started)

	waitingContext, cancel := context.WithCancel(t.Context())
	waitingDone := make(chan error, 1)
	go func() {
		_, err := service.Answer(waitingContext, waitingOwner, Input{Query: "cancel me"})
		waitingDone <- err
	}()
	waitForAdmissionSnapshot(t, service, waitingOwner.OwnerUserID(), func(snapshot answerAdmissionSnapshot) bool {
		return snapshot.waiting == 1 && snapshot.ownerWaiting == 1
	})

	cancel()
	if err := <-waitingDone; !errors.Is(err, context.Canceled) {
		t.Fatalf("waiting Answer() error = %v, want context.Canceled", err)
	}
	waitForAdmissionSnapshot(t, service, waitingOwner.OwnerUserID(), func(snapshot answerAdmissionSnapshot) bool {
		return snapshot.waiting == 0 && snapshot.ownerWaiting == 0
	})

	close(release)
	if err := <-activeDone; err != nil {
		t.Fatalf("active Answer() error = %v, want nil", err)
	}
}

func TestConcurrentServiceHandlesOneHundredOwnersWithoutPaidDependencies(t *testing.T) {
	const (
		ownerCount          = 100
		maxGlobalConcurrent = 10
	)
	var active atomic.Int64
	var maximumActive atomic.Int64
	fake := &fakeAnswerer{answer: func(
		ctx context.Context,
		_ accessdomain.OwnerScope,
		_ Input,
	) (Output, error) {
		current := active.Add(1)
		defer active.Add(-1)
		updateAtomicMaximum(&maximumActive, current)
		select {
		case <-time.After(2 * time.Millisecond):
			return Output{Answer: "fake"}, nil
		case <-ctx.Done():
			return Output{}, ctx.Err()
		}
	}}
	service := newTestConcurrentService(t, fake, AnswerAdmissionLimits{
		MaxConcurrencyGlobal:   maxGlobalConcurrent,
		MaxConcurrencyPerOwner: 2,
		MaxWaitersGlobal:       500,
		MaxWaitersPerOwner:     5,
		WaitTimeout:            2 * time.Second,
	})

	start := make(chan struct{})
	errorsByOwner := make(chan error, ownerCount)
	var requests sync.WaitGroup
	for ownerID := int64(1); ownerID <= ownerCount; ownerID++ {
		scope := testAnswerOwnerScopeForID(t, ownerID)
		requests.Add(1)
		go func() {
			defer requests.Done()
			<-start
			_, err := service.Answer(t.Context(), scope, Input{Query: "fake query"})
			errorsByOwner <- err
		}()
	}
	close(start)
	requests.Wait()
	close(errorsByOwner)

	for err := range errorsByOwner {
		if err != nil {
			t.Fatalf("one of 100 zero-cost requests failed: %v", err)
		}
	}
	if maximumActive.Load() > maxGlobalConcurrent {
		t.Fatalf("maximum active = %d, want <= %d", maximumActive.Load(), maxGlobalConcurrent)
	}
}

func blockingAnswerer(
	started chan<- int64,
	release <-chan struct{},
) *fakeAnswerer {
	return &fakeAnswerer{answer: func(
		ctx context.Context,
		scope accessdomain.OwnerScope,
		_ Input,
	) (Output, error) {
		started <- scope.OwnerUserID()
		select {
		case <-release:
			return Output{Answer: "ok"}, nil
		case <-ctx.Done():
			return Output{}, ctx.Err()
		}
	}}
}

func newTestConcurrentService(
	t *testing.T,
	next answerer,
	limits AnswerAdmissionLimits,
) *ConcurrentService {
	t.Helper()
	service, err := NewConcurrentService(
		next,
		&recordingAnswerAdmissionObserver{},
		limits,
	)
	if err != nil {
		t.Fatalf("NewConcurrentService() error = %v", err)
	}
	return service
}

func runTestAnswer(
	service *ConcurrentService,
	scope accessdomain.OwnerScope,
	done chan<- error,
) {
	runNamedTestAnswer(service, scope, "fake", done)
}

func runNamedTestAnswer(
	service *ConcurrentService,
	scope accessdomain.OwnerScope,
	query string,
	done chan<- error,
) {
	_, err := service.Answer(context.Background(), scope, Input{Query: query})
	done <- err
}

func receiveStartedOwner(t *testing.T, started <-chan int64) int64 {
	t.Helper()
	select {
	case ownerID := <-started:
		return ownerID
	case <-time.After(time.Second):
		t.Fatal("timed out waiting for fake answer execution")
		return 0
	}
}

func receiveStartedQuery(t *testing.T, started <-chan string) string {
	t.Helper()
	select {
	case query := <-started:
		return query
	case <-time.After(time.Second):
		t.Fatal("timed out waiting for fake answer execution")
		return ""
	}
}

func waitForAdmissionSnapshot(
	t *testing.T,
	service *ConcurrentService,
	ownerID int64,
	ready func(answerAdmissionSnapshot) bool,
) {
	t.Helper()
	deadline := time.Now().Add(time.Second)
	for time.Now().Before(deadline) {
		if ready(service.scheduler.snapshot(ownerID)) {
			return
		}
		time.Sleep(time.Millisecond)
	}
	t.Fatalf(
		"admission state did not become ready; snapshot = %+v",
		service.scheduler.snapshot(ownerID),
	)
}

func updateAtomicMaximum(maximum *atomic.Int64, current int64) {
	for {
		previous := maximum.Load()
		if current <= previous || maximum.CompareAndSwap(previous, current) {
			return
		}
	}
}
