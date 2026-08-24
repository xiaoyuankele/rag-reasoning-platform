package answer

import (
	"context"
	"errors"
	"testing"
	"time"
)

type fakeAnswerJobRetentionRepository struct {
	completedBefore time.Time
	limit           int
	deletedCount    int64
	err             error
}

func (f *fakeAnswerJobRetentionRepository) DeleteExpiredAnswerJobs(
	_ context.Context,
	completedBefore time.Time,
	limit int,
) (int64, error) {
	f.completedBefore = completedBefore
	f.limit = limit
	return f.deletedCount, f.err
}

type recordingAnswerJobRetentionObserver struct {
	events []JobRetentionEvent
}

func (o *recordingAnswerJobRetentionObserver) ObserveAnswerJobRetention(
	_ context.Context,
	event JobRetentionEvent,
) {
	o.events = append(o.events, event)
}

func TestAnswerJobRetentionServiceDeletesOneBoundedBatch(t *testing.T) {
	now := time.Date(2026, 8, 25, 12, 0, 0, 0, time.UTC)
	repository := &fakeAnswerJobRetentionRepository{deletedCount: 250}
	observer := &recordingAnswerJobRetentionObserver{}
	service, err := NewJobRetentionService(
		repository,
		observer,
		7*24*time.Hour,
		250,
	)
	if err != nil {
		t.Fatalf("NewJobRetentionService() error = %v", err)
	}
	service.now = func() time.Time { return now }

	handled, err := service.RunOnce(t.Context())
	if err != nil || !handled {
		t.Fatalf("RunOnce() = %t, %v, want true nil", handled, err)
	}
	if !repository.completedBefore.Equal(now.Add(-7*24*time.Hour)) || repository.limit != 250 {
		t.Fatalf("repository call = %v limit %d", repository.completedBefore, repository.limit)
	}
	if len(observer.events) != 1 || observer.events[0].DeletedCount != 250 ||
		observer.events[0].BatchSize != 250 || observer.events[0].Duration < 0 {
		t.Fatalf("events = %+v, want one safe cleanup event", observer.events)
	}
}

func TestAnswerJobRetentionServiceReturnsIdleWithoutExpiredJobs(t *testing.T) {
	repository := &fakeAnswerJobRetentionRepository{}
	observer := &recordingAnswerJobRetentionObserver{}
	service, err := NewJobRetentionService(repository, observer, time.Hour, 10)
	if err != nil {
		t.Fatalf("NewJobRetentionService() error = %v", err)
	}

	handled, err := service.RunOnce(t.Context())
	if err != nil || handled || len(observer.events) != 0 {
		t.Fatalf("RunOnce() = %t, %v, events=%+v", handled, err, observer.events)
	}
}

func TestAnswerJobRetentionServicePreservesRepositoryError(t *testing.T) {
	repositoryError := errors.New("database unavailable")
	repository := &fakeAnswerJobRetentionRepository{err: repositoryError}
	service, err := NewJobRetentionService(
		repository,
		&recordingAnswerJobRetentionObserver{},
		time.Hour,
		10,
	)
	if err != nil {
		t.Fatalf("NewJobRetentionService() error = %v", err)
	}

	handled, err := service.RunOnce(t.Context())
	if handled || !errors.Is(err, repositoryError) {
		t.Fatalf("RunOnce() = %t, %v, want wrapped repository error", handled, err)
	}
}

func TestNewAnswerJobRetentionServiceRejectsInvalidConfiguration(t *testing.T) {
	repository := &fakeAnswerJobRetentionRepository{}
	observer := &recordingAnswerJobRetentionObserver{}
	testCases := []struct {
		name      string
		jobs      JobRetentionRepository
		observer  JobRetentionObserver
		retention time.Duration
		batchSize int
		want      error
	}{
		{name: "missing repository", observer: observer, retention: time.Hour, batchSize: 10, want: ErrAnswerJobRetentionDependencies},
		{name: "missing observer", jobs: repository, retention: time.Hour, batchSize: 10, want: ErrAnswerJobRetentionDependencies},
		{name: "zero retention", jobs: repository, observer: observer, batchSize: 10, want: ErrInvalidAnswerJobRetention},
		{name: "zero batch", jobs: repository, observer: observer, retention: time.Hour, want: ErrInvalidAnswerJobRetention},
	}

	for _, testCase := range testCases {
		t.Run(testCase.name, func(t *testing.T) {
			_, err := NewJobRetentionService(
				testCase.jobs,
				testCase.observer,
				testCase.retention,
				testCase.batchSize,
			)
			if !errors.Is(err, testCase.want) {
				t.Fatalf("NewJobRetentionService() error = %v, want %v", err, testCase.want)
			}
		})
	}
}
