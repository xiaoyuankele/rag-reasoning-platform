package embedding

import (
	"context"
	"errors"
	"testing"
	"time"

	accessdomain "rag-reasoning-platform/backend/internal/domain/access"
	documentdomain "rag-reasoning-platform/backend/internal/domain/document"
	embeddingdomain "rag-reasoning-platform/backend/internal/domain/embedding"
)

// fakeJobRequester 是 QueueService 的测试替身。它只实现 Application 真正
// 依赖的一个端口，并记录调用次数，避免单元测试连接 PostgreSQL。
type fakeJobRequester struct {
	requestFunc  func(context.Context, accessdomain.OwnerScope, int64, string, int) (embeddingdomain.JobRequestResult, error)
	requestCalls int
}

func (f *fakeJobRequester) RequestEmbeddingJob(
	ctx context.Context,
	scope accessdomain.OwnerScope,
	documentID int64,
	modelName string,
	dimensions int,
) (embeddingdomain.JobRequestResult, error) {
	f.requestCalls++
	return f.requestFunc(ctx, scope, documentID, modelName, dimensions)
}

func TestQueueServiceRejectsInvalidDocumentID(t *testing.T) {
	jobs := failOnJobRequest(t)
	service := NewQueueService(jobs, "test-model", 8)

	_, err := service.Queue(context.Background(), testEmbeddingOwnerScope(t), 0)

	if !errors.Is(err, ErrInvalidDocumentID) {
		t.Fatalf("Queue() error = %v, want ErrInvalidDocumentID", err)
	}
	if jobs.requestCalls != 0 {
		t.Fatalf("RequestEmbeddingJob() calls = %d, want 0", jobs.requestCalls)
	}
}

func TestQueueServicePreservesDocumentNotFound(t *testing.T) {
	jobs := &fakeJobRequester{
		requestFunc: func(
			context.Context,
			accessdomain.OwnerScope,
			int64,
			string,
			int,
		) (embeddingdomain.JobRequestResult, error) {
			return embeddingdomain.JobRequestResult{}, documentdomain.ErrNotFound
		},
	}
	service := NewQueueService(jobs, "test-model", 8)

	_, err := service.Queue(context.Background(), testEmbeddingOwnerScope(t), 99)

	if !errors.Is(err, documentdomain.ErrNotFound) {
		t.Fatalf("Queue() error = %v, want ErrNotFound", err)
	}
	if jobs.requestCalls != 1 {
		t.Fatalf("RequestEmbeddingJob() calls = %d, want 1", jobs.requestCalls)
	}
}

func TestQueueServicePreservesAdmissionErrors(t *testing.T) {
	for _, admissionErr := range []error{
		embeddingdomain.ErrOwnerActiveJobLimitExceeded,
		embeddingdomain.ErrGlobalActiveJobLimitExceeded,
	} {
		t.Run(admissionErr.Error(), func(t *testing.T) {
			jobs := &fakeJobRequester{
				requestFunc: func(
					context.Context,
					accessdomain.OwnerScope,
					int64,
					string,
					int,
				) (embeddingdomain.JobRequestResult, error) {
					return embeddingdomain.JobRequestResult{}, admissionErr
				},
			}
			service := NewQueueService(jobs, "test-model", 8)

			_, err := service.Queue(context.Background(), testEmbeddingOwnerScope(t), 7)
			if !errors.Is(err, admissionErr) {
				t.Fatalf("Queue() error = %v, want wrapped %v", err, admissionErr)
			}
		})
	}
}

func TestQueueServiceRequestsConfiguredJob(t *testing.T) {
	const (
		documentID int64 = 7
		modelName        = "text-embedding-3-small"
		dimensions       = 1536
	)
	expectedResult := embeddingdomain.JobRequestResult{Job: embeddingdomain.Job{
		ID:           11,
		DocumentID:   documentID,
		ModelName:    modelName,
		Dimensions:   dimensions,
		Status:       embeddingdomain.JobStatusWaitingDocument,
		AttemptCount: 0,
		CreatedAt:    time.Now(),
		UpdatedAt:    time.Now(),
	}, Created: true}

	jobs := &fakeJobRequester{
		requestFunc: func(
			_ context.Context,
			scope accessdomain.OwnerScope,
			actualDocumentID int64,
			actualModelName string,
			actualDimensions int,
		) (embeddingdomain.JobRequestResult, error) {
			if scope.OwnerUserID() != testEmbeddingOwnerUserID {
				t.Fatalf(
					"RequestEmbeddingJob() scope owner = %d, want %d",
					scope.OwnerUserID(),
					testEmbeddingOwnerUserID,
				)
			}
			if actualDocumentID != documentID ||
				actualModelName != modelName ||
				actualDimensions != dimensions {
				t.Fatalf(
					"RequestEmbeddingJob() args = (%d, %q, %d), want (%d, %q, %d)",
					actualDocumentID,
					actualModelName,
					actualDimensions,
					documentID,
					modelName,
					dimensions,
				)
			}
			return expectedResult, nil
		},
	}
	service := NewQueueService(jobs, modelName, dimensions)

	actualResult, err := service.Queue(
		context.Background(),
		testEmbeddingOwnerScope(t),
		documentID,
	)

	if err != nil {
		t.Fatalf("Queue() error = %v, want nil", err)
	}
	if actualResult != expectedResult {
		t.Fatalf("Queue() result = %+v, want %+v", actualResult, expectedResult)
	}
	if jobs.requestCalls != 1 {
		t.Fatalf("RequestEmbeddingJob() calls = %d, want 1", jobs.requestCalls)
	}
}

func TestQueueServicePreservesExistingActiveJob(t *testing.T) {
	expectedResult := embeddingdomain.JobRequestResult{
		Job: embeddingdomain.Job{
			ID:         12,
			DocumentID: 7,
			Status:     embeddingdomain.JobStatusQueued,
		},
		Created: false,
	}
	jobs := &fakeJobRequester{
		requestFunc: func(
			context.Context,
			accessdomain.OwnerScope,
			int64,
			string,
			int,
		) (embeddingdomain.JobRequestResult, error) {
			return expectedResult, nil
		},
	}
	service := NewQueueService(jobs, "test-model", 8)

	actualResult, err := service.Queue(context.Background(), testEmbeddingOwnerScope(t), 7)

	if err != nil {
		t.Fatalf("Queue() error = %v, want nil", err)
	}
	if actualResult != expectedResult {
		t.Fatalf("Queue() result = %+v, want %+v", actualResult, expectedResult)
	}
	if jobs.requestCalls != 1 {
		t.Fatalf("RequestEmbeddingJob() calls = %d, want 1", jobs.requestCalls)
	}
}

func failOnJobRequest(t *testing.T) *fakeJobRequester {
	t.Helper()
	return &fakeJobRequester{
		requestFunc: func(
			context.Context,
			accessdomain.OwnerScope,
			int64,
			string,
			int,
		) (embeddingdomain.JobRequestResult, error) {
			t.Fatal("RequestEmbeddingJob() must not be called")
			return embeddingdomain.JobRequestResult{}, nil
		},
	}
}

func TestQueueServiceRejectsInvalidBatch(t *testing.T) {
	jobs := failOnJobRequest(t)
	service := NewQueueService(jobs, "test-model", 8)

	testCases := []struct {
		name        string
		documentIDs []int64
		wantErr     error
	}{
		{name: "empty", documentIDs: nil, wantErr: ErrEmptyEmbeddingBatch},
		{name: "invalid ID", documentIDs: []int64{1, 0}, wantErr: ErrInvalidDocumentID},
		{name: "too many", documentIDs: make([]int64, MaxEmbeddingBatchDocumentCount+1), wantErr: ErrEmbeddingBatchTooLarge},
	}

	for _, testCase := range testCases {
		t.Run(testCase.name, func(t *testing.T) {
			_, err := service.QueueBatch(context.Background(), testEmbeddingOwnerScope(t), testCase.documentIDs)
			if !errors.Is(err, testCase.wantErr) {
				t.Fatalf("QueueBatch() error = %v, want %v", err, testCase.wantErr)
			}
		})
	}
	if jobs.requestCalls != 0 {
		t.Fatalf("RequestEmbeddingJob() calls = %d, want 0", jobs.requestCalls)
	}
}

func TestQueueServiceQueuesBatchIndependentlyAndDeduplicates(t *testing.T) {
	jobs := &fakeJobRequester{
		requestFunc: func(
			_ context.Context,
			_ accessdomain.OwnerScope,
			documentID int64,
			_ string,
			_ int,
		) (embeddingdomain.JobRequestResult, error) {
			if documentID == 2 {
				return embeddingdomain.JobRequestResult{}, documentdomain.ErrNotFound
			}
			return embeddingdomain.JobRequestResult{
				Job:     embeddingdomain.Job{ID: documentID + 100, DocumentID: documentID},
				Created: documentID == 1,
			}, nil
		},
	}
	service := NewQueueService(jobs, "test-model", 8)

	output, err := service.QueueBatch(
		context.Background(),
		testEmbeddingOwnerScope(t),
		[]int64{1, 2, 1, 3},
	)

	if err != nil {
		t.Fatalf("QueueBatch() error = %v, want nil", err)
	}
	if jobs.requestCalls != 3 {
		t.Fatalf("RequestEmbeddingJob() calls = %d, want 3 unique documents", jobs.requestCalls)
	}
	if len(output.Items) != 3 {
		t.Fatalf("QueueBatch() items = %d, want 3", len(output.Items))
	}
	if !output.Items[0].Result.Created || output.Items[0].Result.Job.DocumentID != 1 {
		t.Fatalf("first item = %+v, want newly created document 1 job", output.Items[0])
	}
	if !errors.Is(output.Items[1].Err, documentdomain.ErrNotFound) {
		t.Fatalf("second item error = %v, want ErrNotFound", output.Items[1].Err)
	}
	if output.Items[2].Result.Created || output.Items[2].Result.Job.DocumentID != 3 {
		t.Fatalf("third item = %+v, want existing document 3 job", output.Items[2])
	}
}
