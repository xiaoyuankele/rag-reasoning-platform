package document

import (
	"context"
	"errors"
	"testing"
	"time"

	documentdomain "rag-reasoning-platform/backend/internal/domain/document"
)

type fakeQueueDocumentFinder struct {
	getByIDFunc  func(context.Context, int64) (documentdomain.Document, error)
	getByIDCalls int
}

func (f *fakeQueueDocumentFinder) GetByID(
	ctx context.Context,
	id int64,
) (documentdomain.Document, error) {
	f.getByIDCalls++
	return f.getByIDFunc(ctx, id)
}

type fakeProcessingJobCreator struct {
	createFunc  func(context.Context, int64) (documentdomain.ProcessingJob, error)
	createCalls int
}

func (f *fakeProcessingJobCreator) CreateProcessingJob(
	ctx context.Context,
	documentID int64,
) (documentdomain.ProcessingJob, error) {
	f.createCalls++
	return f.createFunc(ctx, documentID)
}

func TestQueueProcessingServiceRejectsInvalidID(t *testing.T) {
	documents := newFailOnCallQueueFinder(t)
	jobs := newFailOnCallJobCreator(t)
	service := NewQueueProcessingService(documents, jobs)

	_, err := service.Queue(context.Background(), 0)

	if !errors.Is(err, ErrInvalidID) {
		t.Fatalf("Queue() error = %v, want ErrInvalidID", err)
	}
	assertQueueCalls(t, documents, jobs, 0, 0)
}

func TestQueueProcessingServicePreservesNotFound(t *testing.T) {
	documents := &fakeQueueDocumentFinder{
		getByIDFunc: func(
			context.Context,
			int64,
		) (documentdomain.Document, error) {
			return documentdomain.Document{}, documentdomain.ErrNotFound
		},
	}
	jobs := newFailOnCallJobCreator(t)
	service := NewQueueProcessingService(documents, jobs)

	_, err := service.Queue(context.Background(), 99)

	if !errors.Is(err, documentdomain.ErrNotFound) {
		t.Fatalf("Queue() error = %v, want ErrNotFound", err)
	}
	assertQueueCalls(t, documents, jobs, 1, 0)
}

func TestQueueProcessingServiceRejectsNonProcessableStatus(t *testing.T) {
	testCases := []struct {
		name   string
		status documentdomain.Status
	}{
		{
			name:   "processing",
			status: documentdomain.StatusProcessing,
		},
		{
			name:   "ready",
			status: documentdomain.StatusReady,
		},
	}

	for _, testCase := range testCases {
		t.Run(testCase.name, func(t *testing.T) {
			documents := &fakeQueueDocumentFinder{
				getByIDFunc: func(
					_ context.Context,
					id int64,
				) (documentdomain.Document, error) {
					return documentdomain.Document{
						ID:     id,
						Status: testCase.status,
					}, nil
				},
			}
			jobs := newFailOnCallJobCreator(t)
			service := NewQueueProcessingService(documents, jobs)

			_, err := service.Queue(context.Background(), 7)

			if !errors.Is(err, ErrDocumentNotProcessable) {
				t.Fatalf(
					"Queue() error = %v, want ErrDocumentNotProcessable",
					err,
				)
			}
			assertQueueCalls(t, documents, jobs, 1, 0)
		})
	}
}

func TestQueueProcessingServicePreservesActiveJobConflict(t *testing.T) {
	documents := &fakeQueueDocumentFinder{
		getByIDFunc: func(
			_ context.Context,
			id int64,
		) (documentdomain.Document, error) {
			return documentdomain.Document{
				ID:     id,
				Status: documentdomain.StatusUploaded,
			}, nil
		},
	}
	jobs := &fakeProcessingJobCreator{
		createFunc: func(
			context.Context,
			int64,
		) (documentdomain.ProcessingJob, error) {
			return documentdomain.ProcessingJob{},
				documentdomain.ErrActiveProcessingJobExists
		},
	}
	service := NewQueueProcessingService(documents, jobs)

	_, err := service.Queue(context.Background(), 7)

	if !errors.Is(err, documentdomain.ErrActiveProcessingJobExists) {
		t.Fatalf(
			"Queue() error = %v, want ErrActiveProcessingJobExists",
			err,
		)
	}
	assertQueueCalls(t, documents, jobs, 1, 1)
}

func TestQueueProcessingServiceCreatesJob(t *testing.T) {
	testCases := []struct {
		name           string
		documentStatus documentdomain.Status
	}{
		{
			name:           "uploaded document",
			documentStatus: documentdomain.StatusUploaded,
		},
		{
			name:           "failed document retry",
			documentStatus: documentdomain.StatusFailed,
		},
	}

	for _, testCase := range testCases {
		t.Run(testCase.name, func(t *testing.T) {
			const documentID int64 = 7
			expectedJob := documentdomain.ProcessingJob{
				ID:           11,
				DocumentID:   documentID,
				Status:       documentdomain.ProcessingJobStatusQueued,
				AttemptCount: 0,
				CreatedAt:    time.Now(),
				UpdatedAt:    time.Now(),
			}

			documents := &fakeQueueDocumentFinder{
				getByIDFunc: func(
					_ context.Context,
					id int64,
				) (documentdomain.Document, error) {
					if id != documentID {
						t.Fatalf(
							"GetByID() id = %d, want %d",
							id,
							documentID,
						)
					}

					return documentdomain.Document{
						ID:     id,
						Status: testCase.documentStatus,
					}, nil
				},
			}
			jobs := &fakeProcessingJobCreator{
				createFunc: func(
					_ context.Context,
					actualDocumentID int64,
				) (documentdomain.ProcessingJob, error) {
					if actualDocumentID != documentID {
						t.Fatalf(
							"CreateProcessingJob() documentID = %d, want %d",
							actualDocumentID,
							documentID,
						)
					}

					return expectedJob, nil
				},
			}
			service := NewQueueProcessingService(documents, jobs)

			actualJob, err := service.Queue(
				context.Background(),
				documentID,
			)

			if err != nil {
				t.Fatalf("Queue() error = %v, want nil", err)
			}
			if actualJob != expectedJob {
				t.Fatalf(
					"Queue() job = %+v, want %+v",
					actualJob,
					expectedJob,
				)
			}
			assertQueueCalls(t, documents, jobs, 1, 1)
		})
	}
}

func newFailOnCallQueueFinder(
	t *testing.T,
) *fakeQueueDocumentFinder {
	t.Helper()

	return &fakeQueueDocumentFinder{
		getByIDFunc: func(
			context.Context,
			int64,
		) (documentdomain.Document, error) {
			t.Fatal("GetByID() must not be called")
			return documentdomain.Document{}, nil
		},
	}
}

func newFailOnCallJobCreator(
	t *testing.T,
) *fakeProcessingJobCreator {
	t.Helper()

	return &fakeProcessingJobCreator{
		createFunc: func(
			context.Context,
			int64,
		) (documentdomain.ProcessingJob, error) {
			t.Fatal("CreateProcessingJob() must not be called")
			return documentdomain.ProcessingJob{}, nil
		},
	}
}

func assertQueueCalls(
	t *testing.T,
	documents *fakeQueueDocumentFinder,
	jobs *fakeProcessingJobCreator,
	wantGetByIDCalls int,
	wantCreateCalls int,
) {
	t.Helper()

	if documents.getByIDCalls != wantGetByIDCalls {
		t.Fatalf(
			"GetByID() calls = %d, want %d",
			documents.getByIDCalls,
			wantGetByIDCalls,
		)
	}
	if jobs.createCalls != wantCreateCalls {
		t.Fatalf(
			"CreateProcessingJob() calls = %d, want %d",
			jobs.createCalls,
			wantCreateCalls,
		)
	}
}
