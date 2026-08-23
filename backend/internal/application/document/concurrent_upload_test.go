package document

import (
	"context"
	"errors"
	"io"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	accessdomain "rag-reasoning-platform/backend/internal/domain/access"
)

type fakeDocumentUploader struct {
	upload func(
		context.Context,
		accessdomain.OwnerScope,
		UploadInput,
	) (UploadResult, error)
}

func (f *fakeDocumentUploader) Upload(
	ctx context.Context,
	scope accessdomain.OwnerScope,
	input UploadInput,
) (UploadResult, error) {
	return f.upload(ctx, scope, input)
}

type recordingUploadAdmissionObserver struct {
	mu     sync.Mutex
	events []UploadAdmissionEvent
}

func (o *recordingUploadAdmissionObserver) ObserveUploadAdmissionEvent(
	_ context.Context,
	event UploadAdmissionEvent,
) {
	o.mu.Lock()
	defer o.mu.Unlock()
	o.events = append(o.events, event)
}

func (o *recordingUploadAdmissionObserver) snapshot() []UploadAdmissionEvent {
	o.mu.Lock()
	defer o.mu.Unlock()
	return append([]UploadAdmissionEvent(nil), o.events...)
}

func testUploadOwnerScope(t *testing.T, ownerUserID int64) accessdomain.OwnerScope {
	t.Helper()
	scope, err := accessdomain.NewOwnerScope(ownerUserID)
	if err != nil {
		t.Fatalf("NewOwnerScope(%d) error = %v", ownerUserID, err)
	}
	return scope
}

func TestNewConcurrentUploadServiceRejectsInvalidDependenciesAndConfiguration(
	t *testing.T,
) {
	fake := &fakeDocumentUploader{upload: func(
		context.Context,
		accessdomain.OwnerScope,
		UploadInput,
	) (UploadResult, error) {
		return UploadResult{}, nil
	}}
	observer := &recordingUploadAdmissionObserver{}

	testCases := []struct {
		name        string
		next        documentUploader
		events      UploadAdmissionEventObserver
		ownerLimit  int
		globalLimit int
		waitTimeout time.Duration
		wantedError error
	}{
		{
			name:        "missing service",
			events:      observer,
			ownerLimit:  1,
			globalLimit: 4,
			waitTimeout: time.Second,
			wantedError: ErrUploadConcurrencyDependencies,
		},
		{
			name:        "missing observer",
			next:        fake,
			ownerLimit:  1,
			globalLimit: 4,
			waitTimeout: time.Second,
			wantedError: ErrUploadConcurrencyDependencies,
		},
		{
			name:        "zero owner limit",
			next:        fake,
			events:      observer,
			globalLimit: 4,
			waitTimeout: time.Second,
			wantedError: ErrUploadConcurrencyConfiguration,
		},
		{
			name:        "global below owner",
			next:        fake,
			events:      observer,
			ownerLimit:  2,
			globalLimit: 1,
			waitTimeout: time.Second,
			wantedError: ErrUploadConcurrencyConfiguration,
		},
		{
			name:        "zero timeout",
			next:        fake,
			events:      observer,
			ownerLimit:  1,
			globalLimit: 4,
			wantedError: ErrUploadConcurrencyConfiguration,
		},
	}

	for _, testCase := range testCases {
		t.Run(testCase.name, func(t *testing.T) {
			_, err := NewConcurrentUploadService(
				testCase.next,
				testCase.events,
				testCase.ownerLimit,
				testCase.globalLimit,
				testCase.waitTimeout,
			)
			if !errors.Is(err, testCase.wantedError) {
				t.Fatalf(
					"NewConcurrentUploadService() error = %v, want %v",
					err,
					testCase.wantedError,
				)
			}
		})
	}
}

func TestConcurrentUploadServiceEnforcesOwnerAndGlobalCapacity(t *testing.T) {
	started := make(chan int64, 2)
	release := make(chan struct{})
	var callCount atomic.Int64
	fake := &fakeDocumentUploader{upload: func(
		ctx context.Context,
		scope accessdomain.OwnerScope,
		input UploadInput,
	) (UploadResult, error) {
		callCount.Add(1)
		if _, err := io.Copy(io.Discard, input.Content); err != nil {
			return UploadResult{}, err
		}
		started <- scope.OwnerUserID()
		select {
		case <-release:
			return UploadResult{}, nil
		case <-ctx.Done():
			return UploadResult{}, ctx.Err()
		}
	}}
	observer := &recordingUploadAdmissionObserver{}
	service, err := NewConcurrentUploadService(
		fake,
		observer,
		1,
		2,
		40*time.Millisecond,
	)
	if err != nil {
		t.Fatalf("NewConcurrentUploadService() error = %v", err)
	}

	ownerA := testUploadOwnerScope(t, 101)
	ownerB := testUploadOwnerScope(t, 102)
	ownerC := testUploadOwnerScope(t, 103)
	done := make(chan error, 2)
	go func() {
		_, uploadErr := service.Upload(
			t.Context(),
			ownerA,
			UploadInput{Content: strings.NewReader("owner-a")},
		)
		done <- uploadErr
	}()
	if ownerID := <-started; ownerID != ownerA.OwnerUserID() {
		t.Fatalf("first started owner = %d, want %d", ownerID, ownerA.OwnerUserID())
	}

	_, ownerLimitErr := service.Upload(
		t.Context(),
		ownerA,
		UploadInput{Content: strings.NewReader("owner-a-second")},
	)
	if !errors.Is(ownerLimitErr, ErrUploadOwnerCapacityExhausted) {
		t.Fatalf("same-owner upload error = %v, want owner capacity", ownerLimitErr)
	}

	go func() {
		_, uploadErr := service.Upload(
			t.Context(),
			ownerB,
			UploadInput{Content: strings.NewReader("owner-b")},
		)
		done <- uploadErr
	}()
	if ownerID := <-started; ownerID != ownerB.OwnerUserID() {
		t.Fatalf("second started owner = %d, want %d", ownerID, ownerB.OwnerUserID())
	}

	_, globalLimitErr := service.Upload(
		t.Context(),
		ownerC,
		UploadInput{Content: strings.NewReader("owner-c")},
	)
	if !errors.Is(globalLimitErr, ErrUploadGlobalCapacityExhausted) {
		t.Fatalf("third-owner upload error = %v, want global capacity", globalLimitErr)
	}
	if callCount.Load() != 2 {
		t.Fatalf("downstream calls = %d, want 2", callCount.Load())
	}

	close(release)
	for range 2 {
		if uploadErr := <-done; uploadErr != nil {
			t.Fatalf("admitted Upload() error = %v, want nil", uploadErr)
		}
	}

	events := observer.snapshot()
	assertUploadAdmissionEventCount(t, events, UploadAdmissionEventAdmitted, 2)
	assertUploadAdmissionEventCount(t, events, UploadAdmissionEventReleased, 2)
	assertUploadAdmissionOutcomeCount(
		t,
		events,
		UploadAdmissionOutcomeOwnerCapacityTimeout,
		1,
	)
	assertUploadAdmissionOutcomeCount(
		t,
		events,
		UploadAdmissionOutcomeGlobalCapacityTimeout,
		1,
	)
}

func TestConcurrentUploadServiceReleasesSlotAndRecordsBytesAfterError(
	t *testing.T,
) {
	downstreamError := errors.New("storage unavailable")
	var callCount atomic.Int64
	fake := &fakeDocumentUploader{upload: func(
		_ context.Context,
		_ accessdomain.OwnerScope,
		input UploadInput,
	) (UploadResult, error) {
		if _, err := io.Copy(io.Discard, input.Content); err != nil {
			return UploadResult{}, err
		}
		if callCount.Add(1) == 1 {
			return UploadResult{}, downstreamError
		}
		return UploadResult{Duplicate: true}, nil
	}}
	observer := &recordingUploadAdmissionObserver{}
	service, err := NewConcurrentUploadService(
		fake,
		observer,
		1,
		1,
		time.Second,
	)
	if err != nil {
		t.Fatalf("NewConcurrentUploadService() error = %v", err)
	}
	scope := testUploadOwnerScope(t, 201)

	if _, err := service.Upload(
		t.Context(),
		scope,
		UploadInput{Content: strings.NewReader("first")},
	); !errors.Is(err, downstreamError) {
		t.Fatalf("first Upload() error = %v, want downstream error", err)
	}
	result, err := service.Upload(
		t.Context(),
		scope,
		UploadInput{Content: strings.NewReader("second")},
	)
	if err != nil || !result.Duplicate {
		t.Fatalf("second Upload() = %+v, %v; want duplicate success", result, err)
	}

	var foundErrorRelease bool
	var foundSuccessRelease bool
	for _, event := range observer.snapshot() {
		if event.Type != UploadAdmissionEventReleased {
			continue
		}
		switch event.Outcome {
		case UploadAdmissionOutcomeDownstreamError:
			foundErrorRelease = event.BytesRead == int64(len("first"))
		case UploadAdmissionOutcomeSucceeded:
			foundSuccessRelease = event.BytesRead == int64(len("second")) &&
				event.Duplicate
		}
	}
	if !foundErrorRelease || !foundSuccessRelease {
		t.Fatalf(
			"release events = %+v, want byte counts and outcomes",
			observer.snapshot(),
		)
	}
}

func TestConcurrentUploadServiceStopsWaitingWhenContextIsCanceled(t *testing.T) {
	started := make(chan struct{})
	release := make(chan struct{})
	fake := &fakeDocumentUploader{upload: func(
		ctx context.Context,
		_ accessdomain.OwnerScope,
		_ UploadInput,
	) (UploadResult, error) {
		close(started)
		select {
		case <-release:
			return UploadResult{}, nil
		case <-ctx.Done():
			return UploadResult{}, ctx.Err()
		}
	}}
	observer := &recordingUploadAdmissionObserver{}
	service, err := NewConcurrentUploadService(
		fake,
		observer,
		1,
		1,
		time.Second,
	)
	if err != nil {
		t.Fatalf("NewConcurrentUploadService() error = %v", err)
	}
	scope := testUploadOwnerScope(t, 301)
	firstDone := make(chan error, 1)
	go func() {
		_, uploadErr := service.Upload(t.Context(), scope, UploadInput{})
		firstDone <- uploadErr
	}()
	<-started

	waitingContext, cancel := context.WithCancel(t.Context())
	cancel()
	_, canceledErr := service.Upload(waitingContext, scope, UploadInput{})
	if !errors.Is(canceledErr, context.Canceled) {
		t.Fatalf("waiting Upload() error = %v, want context.Canceled", canceledErr)
	}

	close(release)
	if err := <-firstDone; err != nil {
		t.Fatalf("first Upload() error = %v, want nil", err)
	}
	assertUploadAdmissionOutcomeCount(
		t,
		observer.snapshot(),
		UploadAdmissionOutcomeCanceled,
		1,
	)
}

func assertUploadAdmissionEventCount(
	t *testing.T,
	events []UploadAdmissionEvent,
	eventType UploadAdmissionEventType,
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
		t.Fatalf(
			"event %q count = %d, want %d; events = %+v",
			eventType,
			actual,
			want,
			events,
		)
	}
}

func assertUploadAdmissionOutcomeCount(
	t *testing.T,
	events []UploadAdmissionEvent,
	outcome UploadAdmissionOutcome,
	want int,
) {
	t.Helper()
	actual := 0
	for _, event := range events {
		if event.Outcome == outcome {
			actual++
		}
	}
	if actual != want {
		t.Fatalf(
			"outcome %q count = %d, want %d; events = %+v",
			outcome,
			actual,
			want,
			events,
		)
	}
}
