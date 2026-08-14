package embedding

import (
	"context"
	"errors"
	"testing"
	"time"

	embeddingdomain "rag-reasoning-platform/backend/internal/domain/embedding"
)

// fakeEmbeddingJobFinder 是 JobFinder 的测试实现。
// 测试通过函数字段安排返回结果，无需连接真实 PostgreSQL。
type fakeEmbeddingJobFinder struct {
	getByIDFunc  func(context.Context, int64) (embeddingdomain.Job, error)
	getByIDCalls int
}

func (f *fakeEmbeddingJobFinder) GetEmbeddingJobByID(
	ctx context.Context,
	jobID int64,
) (embeddingdomain.Job, error) {
	f.getByIDCalls++
	return f.getByIDFunc(ctx, jobID)
}

func TestJobQueryServiceRejectsInvalidID(t *testing.T) {
	finder := &fakeEmbeddingJobFinder{
		getByIDFunc: func(
			context.Context,
			int64,
		) (embeddingdomain.Job, error) {
			t.Fatal("GetEmbeddingJobByID() must not be called")
			return embeddingdomain.Job{}, nil
		},
	}
	service := NewJobQueryService(finder)

	_, err := service.GetByID(context.Background(), 0)

	if !errors.Is(err, ErrInvalidEmbeddingJobID) {
		t.Fatalf(
			"GetByID() error = %v, want ErrInvalidEmbeddingJobID",
			err,
		)
	}
	if finder.getByIDCalls != 0 {
		t.Fatalf(
			"GetEmbeddingJobByID() calls = %d, want 0",
			finder.getByIDCalls,
		)
	}
}

func TestJobQueryServiceReturnsJob(t *testing.T) {
	expectedJob := embeddingdomain.Job{
		ID:            23,
		DocumentID:    7,
		ModelName:     "text-embedding-v4",
		Dimensions:    1024,
		Status:        embeddingdomain.JobStatusQueued,
		AttemptCount:  0,
		NextAttemptAt: time.Now(),
		CreatedAt:     time.Now(),
		UpdatedAt:     time.Now(),
	}
	finder := &fakeEmbeddingJobFinder{
		getByIDFunc: func(
			_ context.Context,
			jobID int64,
		) (embeddingdomain.Job, error) {
			if jobID != expectedJob.ID {
				t.Fatalf(
					"GetEmbeddingJobByID() jobID = %d, want %d",
					jobID,
					expectedJob.ID,
				)
			}
			return expectedJob, nil
		},
	}
	service := NewJobQueryService(finder)

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
			"GetEmbeddingJobByID() calls = %d, want 1",
			finder.getByIDCalls,
		)
	}
}

func TestJobQueryServicePreservesNotFound(t *testing.T) {
	finder := &fakeEmbeddingJobFinder{
		getByIDFunc: func(
			context.Context,
			int64,
		) (embeddingdomain.Job, error) {
			return embeddingdomain.Job{}, embeddingdomain.ErrJobNotFound
		},
	}
	service := NewJobQueryService(finder)

	_, err := service.GetByID(context.Background(), 999)

	if !errors.Is(err, embeddingdomain.ErrJobNotFound) {
		t.Fatalf(
			"GetByID() error = %v, want ErrJobNotFound",
			err,
		)
	}
	if finder.getByIDCalls != 1 {
		t.Fatalf(
			"GetEmbeddingJobByID() calls = %d, want 1",
			finder.getByIDCalls,
		)
	}
}
