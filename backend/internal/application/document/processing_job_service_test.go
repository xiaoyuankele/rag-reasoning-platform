package document

import (
	"context"
	"errors"
	"testing"
	"time"

	documentdomain "rag-reasoning-platform/backend/internal/domain/document"
)

type fakeProcessingJobFinder struct {
	getByIDFunc  func(context.Context, int64) (documentdomain.ProcessingJob, error)
	getByIDCalls int
}

func (f *fakeProcessingJobFinder) GetProcessingJobByID(
	ctx context.Context,
	jobID int64,
) (documentdomain.ProcessingJob, error) {
	f.getByIDCalls++
	return f.getByIDFunc(ctx, jobID)
}

func TestProcessingJobServiceRejectsInvalidID(t *testing.T) {
	finder := &fakeProcessingJobFinder{
		getByIDFunc: func(
			context.Context,
			int64,
		) (documentdomain.ProcessingJob, error) {
			t.Fatal("GetProcessingJobByID() must not be called")
			return documentdomain.ProcessingJob{}, nil
		},
	}
	service := NewProcessingJobService(finder)

	_, err := service.GetByID(context.Background(), 0)

	if !errors.Is(err, ErrInvalidProcessingJobID) {
		t.Fatalf(
			"GetByID() error = %v, want ErrInvalidProcessingJobID",
			err,
		)
	}
	if finder.getByIDCalls != 0 {
		t.Fatalf(
			"GetProcessingJobByID() calls = %d, want 0",
			finder.getByIDCalls,
		)
	}
}

func TestProcessingJobServiceReturnsJob(t *testing.T) {
	expectedJob := documentdomain.ProcessingJob{
		ID:           17,
		DocumentID:   7,
		Status:       documentdomain.ProcessingJobStatusQueued,
		AttemptCount: 0,
		CreatedAt:    time.Now(),
		UpdatedAt:    time.Now(),
	}
	finder := &fakeProcessingJobFinder{
		getByIDFunc: func(
			_ context.Context,
			jobID int64,
		) (documentdomain.ProcessingJob, error) {
			if jobID != expectedJob.ID {
				t.Fatalf(
					"GetProcessingJobByID() jobID = %d, want %d",
					jobID,
					expectedJob.ID,
				)
			}

			return expectedJob, nil
		},
	}
	service := NewProcessingJobService(finder)

	actualJob, err := service.GetByID(
		context.Background(),
		expectedJob.ID,
	)

	if err != nil {
		t.Fatalf("GetByID() error = %v, want nil", err)
	}
	if actualJob != expectedJob {
		t.Fatalf(
			"GetByID() job = %+v, want %+v",
			actualJob,
			expectedJob,
		)
	}
	if finder.getByIDCalls != 1 {
		t.Fatalf(
			"GetProcessingJobByID() calls = %d, want 1",
			finder.getByIDCalls,
		)
	}
}

func TestProcessingJobServicePreservesNotFound(t *testing.T) {
	finder := &fakeProcessingJobFinder{
		getByIDFunc: func(
			context.Context,
			int64,
		) (documentdomain.ProcessingJob, error) {
			return documentdomain.ProcessingJob{},
				documentdomain.ErrProcessingJobNotFound
		},
	}
	service := NewProcessingJobService(finder)

	_, err := service.GetByID(context.Background(), 999)

	if !errors.Is(err, documentdomain.ErrProcessingJobNotFound) {
		t.Fatalf(
			"GetByID() error = %v, want ErrProcessingJobNotFound",
			err,
		)
	}
	if finder.getByIDCalls != 1 {
		t.Fatalf(
			"GetProcessingJobByID() calls = %d, want 1",
			finder.getByIDCalls,
		)
	}
}
