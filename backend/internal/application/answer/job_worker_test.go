package answer

import (
	"context"
	"errors"
	"sync"
	"testing"
	"time"

	accessdomain "rag-reasoning-platform/backend/internal/domain/access"
	generationdomain "rag-reasoning-platform/backend/internal/domain/generation"
)

type fakeAnswerJobWorkerRepository struct {
	job             Job
	claimErr        error
	succeededOutput Output
	succeededID     int64
	requeuedID      int64
	requeuedAt      time.Time
	requeueCode     JobErrorCode
	failedID        int64
	failureCode     JobErrorCode
}

func (f *fakeAnswerJobWorkerRepository) ClaimNextAnswerJob(context.Context) (Job, error) {
	return f.job, f.claimErr
}
func (f *fakeAnswerJobWorkerRepository) MarkAnswerJobSucceeded(
	_ context.Context,
	jobID int64,
	output Output,
) error {
	f.succeededID = jobID
	f.succeededOutput = output
	return nil
}
func (f *fakeAnswerJobWorkerRepository) RequeueAnswerJob(
	_ context.Context,
	jobID int64,
	next time.Time,
	code JobErrorCode,
	_ string,
) error {
	f.requeuedID = jobID
	f.requeuedAt = next
	f.requeueCode = code
	return nil
}
func (f *fakeAnswerJobWorkerRepository) MarkAnswerJobFailed(
	_ context.Context,
	jobID int64,
	code JobErrorCode,
	_ string,
) error {
	f.failedID = jobID
	f.failureCode = code
	return nil
}

type recordingAnswerJobObserver struct {
	mu     sync.Mutex
	events []JobEvent
}

func (o *recordingAnswerJobObserver) ObserveAnswerJobEvent(
	_ context.Context,
	event JobEvent,
) {
	o.mu.Lock()
	defer o.mu.Unlock()
	o.events = append(o.events, event)
}

func TestAnswerJobWorkerSucceedsWithExistingAnswerService(t *testing.T) {
	repository := &fakeAnswerJobWorkerRepository{job: Job{
		ID:                        11,
		OwnerUserID:               42,
		Query:                     "question",
		TopK:                      5,
		RequestedResponseLanguage: ResponseLanguageAuto,
		Status:                    JobStatusProcessing,
		AttemptCount:              1,
	}}
	wantedOutput := Output{
		Query:            "question",
		Answer:           "answer",
		ResponseLanguage: ResponseLanguageEnglish,
		Sources:          make([]Source, 0),
	}
	answerer := &fakeAnswerer{answer: func(
		_ context.Context,
		scope accessdomain.OwnerScope,
		input Input,
	) (Output, error) {
		if scope.OwnerUserID() != 42 || input.Query != "question" || input.TopK != 5 {
			t.Fatalf("Answer() scope/input = %+v, %+v", scope, input)
		}
		return wantedOutput, nil
	}}
	worker := newAnswerJobWorkerForTest(t, repository, answerer)

	handled, err := worker.RunOnce(t.Context())
	if err != nil || !handled {
		t.Fatalf("RunOnce() = %t, %v", handled, err)
	}
	if repository.succeededID != 11 || repository.succeededOutput.Answer != "answer" {
		t.Fatalf("success = id %d output %+v", repository.succeededID, repository.succeededOutput)
	}
}

func TestAnswerJobWorkerRequeuesTemporaryFailure(t *testing.T) {
	fixedNow := time.Date(2026, 8, 25, 10, 0, 0, 0, time.UTC)
	repository := &fakeAnswerJobWorkerRepository{job: testClaimedAnswerJob(12, 1)}
	answerer := &fakeAnswerer{answer: func(
		context.Context,
		accessdomain.OwnerScope,
		Input,
	) (Output, error) {
		return Output{}, generationdomain.ErrGenerationUnavailable
	}}
	worker := newAnswerJobWorkerForTest(t, repository, answerer)
	worker.now = func() time.Time { return fixedNow }

	handled, err := worker.RunOnce(t.Context())
	if !handled || !errors.Is(err, generationdomain.ErrGenerationUnavailable) {
		t.Fatalf("RunOnce() = %t, %v", handled, err)
	}
	if repository.requeuedID != 12 ||
		repository.requeueCode != JobErrorCodeTemporarilyUnavailable ||
		!repository.requeuedAt.Equal(fixedNow.Add(time.Second)) {
		t.Fatalf("requeue = id %d at %v code %q", repository.requeuedID, repository.requeuedAt, repository.requeueCode)
	}
}

func TestAnswerJobWorkerFailsPermanentErrorWithoutRetry(t *testing.T) {
	repository := &fakeAnswerJobWorkerRepository{job: testClaimedAnswerJob(13, 1)}
	answerer := &fakeAnswerer{answer: func(
		context.Context,
		accessdomain.OwnerScope,
		Input,
	) (Output, error) {
		return Output{}, generationdomain.ErrGenerationQuotaExceeded
	}}
	worker := newAnswerJobWorkerForTest(t, repository, answerer)

	handled, err := worker.RunOnce(t.Context())
	if !handled || !errors.Is(err, generationdomain.ErrGenerationQuotaExceeded) {
		t.Fatalf("RunOnce() = %t, %v", handled, err)
	}
	if repository.failedID != 13 || repository.requeuedID != 0 {
		t.Fatalf("failed ID = %d, requeued ID = %d", repository.failedID, repository.requeuedID)
	}
}

func TestAnswerJobWorkerReturnsIdleForEmptyQueue(t *testing.T) {
	repository := &fakeAnswerJobWorkerRepository{claimErr: ErrNoQueuedAnswerJob}
	answerer := &fakeAnswerer{answer: func(
		context.Context,
		accessdomain.OwnerScope,
		Input,
	) (Output, error) {
		t.Fatal("answerer must not run for empty queue")
		return Output{}, nil
	}}
	worker := newAnswerJobWorkerForTest(t, repository, answerer)

	handled, err := worker.RunOnce(t.Context())
	if handled || err != nil {
		t.Fatalf("RunOnce() = %t, %v, want false nil", handled, err)
	}
}

func newAnswerJobWorkerForTest(
	t *testing.T,
	repository JobWorkerRepository,
	answerer answerer,
) *JobWorker {
	t.Helper()
	retryPolicy, err := NewJobRetryPolicy(3, time.Second, 10*time.Second)
	if err != nil {
		t.Fatalf("NewJobRetryPolicy() error = %v", err)
	}
	worker, err := NewJobWorker(
		repository,
		answerer,
		&recordingAnswerJobObserver{},
		time.Minute,
		retryPolicy,
	)
	if err != nil {
		t.Fatalf("NewJobWorker() error = %v", err)
	}
	return worker
}

func testClaimedAnswerJob(id int64, attempt int) Job {
	return Job{
		ID:                        id,
		OwnerUserID:               42,
		Query:                     "question",
		TopK:                      5,
		RequestedResponseLanguage: ResponseLanguageAuto,
		Status:                    JobStatusProcessing,
		AttemptCount:              attempt,
	}
}
