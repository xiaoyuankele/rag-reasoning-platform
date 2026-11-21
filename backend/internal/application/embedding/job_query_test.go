package embedding

import (
	"context"
	"errors"
	"testing"
	"time"

	accessdomain "rag-reasoning-platform/backend/internal/domain/access"
	embeddingdomain "rag-reasoning-platform/backend/internal/domain/embedding"
)

// fakeEmbeddingJobFinder 是 JobFinder 的测试实现。
// 测试通过函数字段安排返回结果，无需连接真实 PostgreSQL。
type fakeEmbeddingJobFinder struct {
	getByIDFunc    func(context.Context, accessdomain.OwnerScope, int64) (embeddingdomain.Job, error)
	getLatestFunc  func(context.Context, accessdomain.OwnerScope, []int64) ([]embeddingdomain.Job, error)
	getByIDCalls   int
	getLatestCalls int
}

func (f *fakeEmbeddingJobFinder) FindLatestEmbeddingJobsByDocumentIDs(
	ctx context.Context,
	scope accessdomain.OwnerScope,
	documentIDs []int64,
) ([]embeddingdomain.Job, error) {
	f.getLatestCalls++
	return f.getLatestFunc(ctx, scope, documentIDs)
}

func (f *fakeEmbeddingJobFinder) GetEmbeddingJobByID(
	ctx context.Context,
	scope accessdomain.OwnerScope,
	jobID int64,
) (embeddingdomain.Job, error) {
	f.getByIDCalls++
	return f.getByIDFunc(ctx, scope, jobID)
}

func TestJobQueryServiceRejectsInvalidID(t *testing.T) {
	finder := &fakeEmbeddingJobFinder{
		getByIDFunc: func(
			context.Context,
			accessdomain.OwnerScope,
			int64,
		) (embeddingdomain.Job, error) {
			t.Fatal("GetEmbeddingJobByID() must not be called")
			return embeddingdomain.Job{}, nil
		},
	}
	service := NewJobQueryService(finder)

	_, err := service.GetByID(context.Background(), testEmbeddingOwnerScope(t), 0)

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
			scope accessdomain.OwnerScope,
			jobID int64,
		) (embeddingdomain.Job, error) {
			if scope.OwnerUserID() != testEmbeddingOwnerUserID {
				t.Fatalf("GetEmbeddingJobByID() scope owner = %d, want %d", scope.OwnerUserID(), testEmbeddingOwnerUserID)
			}
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
		testEmbeddingOwnerScope(t),
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
			accessdomain.OwnerScope,
			int64,
		) (embeddingdomain.Job, error) {
			return embeddingdomain.Job{}, embeddingdomain.ErrJobNotFound
		},
	}
	service := NewJobQueryService(finder)

	_, err := service.GetByID(context.Background(), testEmbeddingOwnerScope(t), 999)

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

func TestJobQueryServiceFindsLatestJobsInInputOrder(t *testing.T) {
	jobForSeven := embeddingdomain.Job{ID: 41, DocumentID: 7, Status: embeddingdomain.JobStatusSucceeded}
	jobForThree := embeddingdomain.Job{ID: 52, DocumentID: 3, Status: embeddingdomain.JobStatusProcessing}
	finder := &fakeEmbeddingJobFinder{
		getLatestFunc: func(
			_ context.Context,
			scope accessdomain.OwnerScope,
			documentIDs []int64,
		) ([]embeddingdomain.Job, error) {
			if scope.OwnerUserID() != testEmbeddingOwnerUserID {
				t.Fatalf("owner = %d, want %d", scope.OwnerUserID(), testEmbeddingOwnerUserID)
			}
			wantIDs := []int64{7, 9, 3}
			if len(documentIDs) != len(wantIDs) {
				t.Fatalf("document IDs = %v, want %v", documentIDs, wantIDs)
			}
			for index := range wantIDs {
				if documentIDs[index] != wantIDs[index] {
					t.Fatalf("document IDs = %v, want %v", documentIDs, wantIDs)
				}
			}
			// Repository 顺序不属于对外契约，Application 必须恢复输入顺序。
			return []embeddingdomain.Job{jobForThree, jobForSeven}, nil
		},
	}

	output, err := NewJobQueryService(finder).GetLatestByDocumentIDs(
		context.Background(),
		testEmbeddingOwnerScope(t),
		[]int64{7, 9, 7, 3},
	)
	if err != nil {
		t.Fatalf("GetLatestByDocumentIDs() error = %v", err)
	}
	if len(output.Items) != 3 {
		t.Fatalf("items = %d, want 3", len(output.Items))
	}
	if output.Items[0].DocumentID != 7 || output.Items[0].Job == nil || *output.Items[0].Job != jobForSeven {
		t.Fatalf("item 0 = %+v, want document 7 job", output.Items[0])
	}
	if output.Items[1].DocumentID != 9 || output.Items[1].Job != nil {
		t.Fatalf("item 1 = %+v, want document 9 nil job", output.Items[1])
	}
	if output.Items[2].DocumentID != 3 || output.Items[2].Job == nil || *output.Items[2].Job != jobForThree {
		t.Fatalf("item 2 = %+v, want document 3 job", output.Items[2])
	}
	if finder.getLatestCalls != 1 {
		t.Fatalf("latest calls = %d, want 1", finder.getLatestCalls)
	}
}

func TestJobQueryServiceRejectsInvalidLatestLookup(t *testing.T) {
	tests := []struct {
		name        string
		documentIDs []int64
		wantErr     error
	}{
		{name: "empty", documentIDs: nil, wantErr: ErrEmptyEmbeddingJobLookup},
		{name: "too large", documentIDs: make([]int64, MaxEmbeddingBatchDocumentCount+1), wantErr: ErrEmbeddingJobLookupTooLarge},
		{name: "invalid ID", documentIDs: []int64{7, 0}, wantErr: ErrInvalidDocumentID},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			finder := &fakeEmbeddingJobFinder{
				getLatestFunc: func(context.Context, accessdomain.OwnerScope, []int64) ([]embeddingdomain.Job, error) {
					t.Fatal("repository must not be called for invalid input")
					return nil, nil
				},
			}
			_, err := NewJobQueryService(finder).GetLatestByDocumentIDs(
				context.Background(),
				testEmbeddingOwnerScope(t),
				test.documentIDs,
			)
			if !errors.Is(err, test.wantErr) {
				t.Fatalf("error = %v, want %v", err, test.wantErr)
			}
			if finder.getLatestCalls != 0 {
				t.Fatalf("latest calls = %d, want 0", finder.getLatestCalls)
			}
		})
	}
}

func TestJobQueryServicePreservesLatestLookupError(t *testing.T) {
	repositoryErr := errors.New("database unavailable")
	finder := &fakeEmbeddingJobFinder{
		getLatestFunc: func(context.Context, accessdomain.OwnerScope, []int64) ([]embeddingdomain.Job, error) {
			return nil, repositoryErr
		},
	}
	_, err := NewJobQueryService(finder).GetLatestByDocumentIDs(
		context.Background(),
		testEmbeddingOwnerScope(t),
		[]int64{7},
	)
	if !errors.Is(err, repositoryErr) {
		t.Fatalf("error = %v, want wrapped repository error", err)
	}
}
