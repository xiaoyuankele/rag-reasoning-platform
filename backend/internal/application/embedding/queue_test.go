package embedding

import (
	"context"
	"errors"
	"testing"
	"time"

	documentdomain "rag-reasoning-platform/backend/internal/domain/document"
	embeddingdomain "rag-reasoning-platform/backend/internal/domain/embedding"
)

type fakeDocumentFinder struct {
	getByIDFunc  func(context.Context, int64) (documentdomain.Document, error)
	getByIDCalls int
}

func (f *fakeDocumentFinder) GetByID(
	ctx context.Context,
	id int64,
) (documentdomain.Document, error) {
	f.getByIDCalls++
	return f.getByIDFunc(ctx, id)
}

type fakeJobCreator struct {
	createFunc  func(context.Context, int64, string, int) (embeddingdomain.Job, error)
	createCalls int
}

func (f *fakeJobCreator) CreateEmbeddingJob(
	ctx context.Context,
	documentID int64,
	modelName string,
	dimensions int,
) (embeddingdomain.Job, error) {
	f.createCalls++
	return f.createFunc(ctx, documentID, modelName, dimensions)
}

func TestQueueServiceRejectsInvalidDocumentID(t *testing.T) {
	documents := failOnDocumentLookup(t)
	jobs := failOnJobCreation(t)
	service := NewQueueService(documents, jobs, "test-model", 8)

	_, err := service.Queue(context.Background(), 0)

	if !errors.Is(err, ErrInvalidDocumentID) {
		t.Fatalf("Queue() error = %v, want ErrInvalidDocumentID", err)
	}
	assertCalls(t, documents, jobs, 0, 0)
}

func TestQueueServicePreservesDocumentNotFound(t *testing.T) {
	documents := &fakeDocumentFinder{
		getByIDFunc: func(
			context.Context,
			int64,
		) (documentdomain.Document, error) {
			return documentdomain.Document{}, documentdomain.ErrNotFound
		},
	}
	jobs := failOnJobCreation(t)
	service := NewQueueService(documents, jobs, "test-model", 8)

	_, err := service.Queue(context.Background(), 99)

	if !errors.Is(err, documentdomain.ErrNotFound) {
		t.Fatalf("Queue() error = %v, want ErrNotFound", err)
	}
	assertCalls(t, documents, jobs, 1, 0)
}

func TestQueueServiceRequiresReadyDocument(t *testing.T) {
	statuses := []documentdomain.Status{
		documentdomain.StatusUploaded,
		documentdomain.StatusProcessing,
		documentdomain.StatusFailed,
	}

	for _, status := range statuses {
		t.Run(string(status), func(t *testing.T) {
			documents := &fakeDocumentFinder{
				getByIDFunc: func(
					_ context.Context,
					id int64,
				) (documentdomain.Document, error) {
					return documentdomain.Document{
						ID:     id,
						Status: status,
					}, nil
				},
			}
			jobs := failOnJobCreation(t)
			service := NewQueueService(documents, jobs, "test-model", 8)

			_, err := service.Queue(context.Background(), 7)

			if !errors.Is(err, ErrDocumentNotReady) {
				t.Fatalf("Queue() error = %v, want ErrDocumentNotReady", err)
			}
			assertCalls(t, documents, jobs, 1, 0)
		})
	}
}

func TestQueueServiceCreatesConfiguredJob(t *testing.T) {
	const (
		documentID int64 = 7
		modelName        = "text-embedding-3-small"
		dimensions       = 1536
	)
	expectedJob := embeddingdomain.Job{
		ID:           11,
		DocumentID:   documentID,
		ModelName:    modelName,
		Dimensions:   dimensions,
		Status:       embeddingdomain.JobStatusQueued,
		AttemptCount: 0,
		CreatedAt:    time.Now(),
		UpdatedAt:    time.Now(),
	}

	documents := &fakeDocumentFinder{
		getByIDFunc: func(
			_ context.Context,
			id int64,
		) (documentdomain.Document, error) {
			return documentdomain.Document{
				ID:     id,
				Status: documentdomain.StatusReady,
			}, nil
		},
	}
	jobs := &fakeJobCreator{
		createFunc: func(
			_ context.Context,
			actualDocumentID int64,
			actualModelName string,
			actualDimensions int,
		) (embeddingdomain.Job, error) {
			if actualDocumentID != documentID ||
				actualModelName != modelName ||
				actualDimensions != dimensions {
				t.Fatalf(
					"CreateEmbeddingJob() args = (%d, %q, %d), want (%d, %q, %d)",
					actualDocumentID,
					actualModelName,
					actualDimensions,
					documentID,
					modelName,
					dimensions,
				)
			}
			return expectedJob, nil
		},
	}
	service := NewQueueService(documents, jobs, modelName, dimensions)

	actualJob, err := service.Queue(context.Background(), documentID)

	if err != nil {
		t.Fatalf("Queue() error = %v, want nil", err)
	}
	if actualJob != expectedJob {
		t.Fatalf("Queue() job = %+v, want %+v", actualJob, expectedJob)
	}
	assertCalls(t, documents, jobs, 1, 1)
}

func TestQueueServicePreservesActiveJobConflict(t *testing.T) {
	documents := &fakeDocumentFinder{
		getByIDFunc: func(
			_ context.Context,
			id int64,
		) (documentdomain.Document, error) {
			return documentdomain.Document{
				ID:     id,
				Status: documentdomain.StatusReady,
			}, nil
		},
	}
	jobs := &fakeJobCreator{
		createFunc: func(
			context.Context,
			int64,
			string,
			int,
		) (embeddingdomain.Job, error) {
			return embeddingdomain.Job{}, embeddingdomain.ErrActiveJobExists
		},
	}
	service := NewQueueService(documents, jobs, "test-model", 8)

	_, err := service.Queue(context.Background(), 7)

	if !errors.Is(err, embeddingdomain.ErrActiveJobExists) {
		t.Fatalf("Queue() error = %v, want ErrActiveJobExists", err)
	}
	assertCalls(t, documents, jobs, 1, 1)
}

func failOnDocumentLookup(t *testing.T) *fakeDocumentFinder {
	t.Helper()
	return &fakeDocumentFinder{
		getByIDFunc: func(
			context.Context,
			int64,
		) (documentdomain.Document, error) {
			t.Fatal("GetByID() must not be called")
			return documentdomain.Document{}, nil
		},
	}
}

func failOnJobCreation(t *testing.T) *fakeJobCreator {
	t.Helper()
	return &fakeJobCreator{
		createFunc: func(
			context.Context,
			int64,
			string,
			int,
		) (embeddingdomain.Job, error) {
			t.Fatal("CreateEmbeddingJob() must not be called")
			return embeddingdomain.Job{}, nil
		},
	}
}

func assertCalls(
	t *testing.T,
	documents *fakeDocumentFinder,
	jobs *fakeJobCreator,
	wantDocumentCalls int,
	wantJobCalls int,
) {
	t.Helper()

	if documents.getByIDCalls != wantDocumentCalls {
		t.Fatalf(
			"GetByID() calls = %d, want %d",
			documents.getByIDCalls,
			wantDocumentCalls,
		)
	}
	if jobs.createCalls != wantJobCalls {
		t.Fatalf(
			"CreateEmbeddingJob() calls = %d, want %d",
			jobs.createCalls,
			wantJobCalls,
		)
	}
}
