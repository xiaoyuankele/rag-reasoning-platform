package embedding

import (
	"context"
	"errors"
	"reflect"
	"testing"
	"time"

	documentdomain "rag-reasoning-platform/backend/internal/domain/document"
	embeddingdomain "rag-reasoning-platform/backend/internal/domain/embedding"
)

// fakeEmbeddingJobWorkerRepository 是 Embedding Worker 测试使用的任务仓储替身。
//
// 它用函数字段模拟领取、成功、重试和永久失败四种数据库操作，用 Calls 字段记录每种
// 操作的调用次数。这样可以验证 Application 的状态决策，而不必连接真实 PostgreSQL。
type fakeEmbeddingJobWorkerRepository struct {
	claimFunc    func(context.Context) (embeddingdomain.Job, error)
	succeedFunc  func(context.Context, int64, embeddingdomain.JobCompletion) error
	requeueFunc  func(context.Context, int64, time.Time, string) error
	failFunc     func(context.Context, int64, string) error
	claimCalls   int
	succeedCalls int
	requeueCalls int
	failCalls    int
}

// ClaimNextEmbeddingJob 模拟原子领取下一条 queued 向量任务。
func (f *fakeEmbeddingJobWorkerRepository) ClaimNextEmbeddingJob(
	ctx context.Context,
) (embeddingdomain.Job, error) {
	f.claimCalls++
	return f.claimFunc(ctx)
}

// MarkEmbeddingJobSucceeded 模拟原子保存全部向量并把任务标记为 succeeded。
func (f *fakeEmbeddingJobWorkerRepository) MarkEmbeddingJobSucceeded(
	ctx context.Context,
	jobID int64,
	completion embeddingdomain.JobCompletion,
) error {
	f.succeedCalls++
	return f.succeedFunc(ctx, jobID, completion)
}

// RequeueEmbeddingJob 模拟把临时失败的任务重新排队到指定时间。
func (f *fakeEmbeddingJobWorkerRepository) RequeueEmbeddingJob(
	ctx context.Context,
	jobID int64,
	nextAttemptAt time.Time,
	errorMessage string,
) error {
	f.requeueCalls++
	return f.requeueFunc(ctx, jobID, nextAttemptAt, errorMessage)
}

// MarkEmbeddingJobFailed 模拟把无需继续重试的任务标记为 failed。
func (f *fakeEmbeddingJobWorkerRepository) MarkEmbeddingJobFailed(
	ctx context.Context,
	jobID int64,
	errorMessage string,
) error {
	f.failCalls++
	return f.failFunc(ctx, jobID, errorMessage)
}

// fakeEmbeddingChunkLister 模拟按 document ID 读取已经入库的全部文本块。
type fakeEmbeddingChunkLister struct {
	listFunc  func(context.Context, int64) ([]documentdomain.TextChunk, error)
	listCalls int
}

// ListByDocumentID 返回当前测试预先安排的 chunks 或错误。
func (f *fakeEmbeddingChunkLister) ListByDocumentID(
	ctx context.Context,
	documentID int64,
) ([]documentdomain.TextChunk, error) {
	f.listCalls++
	return f.listFunc(ctx, documentID)
}

// fakeEmbedder 模拟远程 Embedding API，不产生网络请求和模型费用。
type fakeEmbedder struct {
	embedFunc  func(context.Context, embeddingdomain.EmbedRequest) (embeddingdomain.EmbedResult, error)
	embedCalls int
}

// Embed 返回当前测试预先安排的向量结果或提供方错误。
func (f *fakeEmbedder) Embed(
	ctx context.Context,
	request embeddingdomain.EmbedRequest,
) (embeddingdomain.EmbedResult, error) {
	f.embedCalls++
	return f.embedFunc(ctx, request)
}

func TestEmbeddingWorkerReturnsIdleForEmptyQueue(t *testing.T) {
	jobs := &fakeEmbeddingJobWorkerRepository{
		claimFunc: func(context.Context) (embeddingdomain.Job, error) {
			return embeddingdomain.Job{}, embeddingdomain.ErrNoQueuedJob
		},
		succeedFunc: failOnEmbeddingSuccess(t),
		requeueFunc: failOnEmbeddingRequeue(t),
		failFunc:    failOnEmbeddingFailure(t),
	}
	chunks := &fakeEmbeddingChunkLister{
		listFunc: func(context.Context, int64) ([]documentdomain.TextChunk, error) {
			t.Fatal("ListByDocumentID() must not be called for an empty queue")
			return nil, nil
		},
	}
	embedder := &fakeEmbedder{
		embedFunc: func(context.Context, embeddingdomain.EmbedRequest) (embeddingdomain.EmbedResult, error) {
			t.Fatal("Embed() must not be called for an empty queue")
			return embeddingdomain.EmbedResult{}, nil
		},
	}
	events := newRecordingJobEventObserver()
	worker := newTestEmbeddingWorker(t, jobs, chunks, embedder, 2, time.Now, events)

	handled, err := worker.RunOnce(context.Background())

	if err != nil {
		t.Fatalf("RunOnce() error = %v, want nil", err)
	}
	if handled {
		t.Fatal("RunOnce() handled = true, want false")
	}
	assertEmbeddingFinalizationCalls(t, jobs, 0, 0, 0)
	if len(events.events) != 0 {
		t.Fatalf("empty queue events = %d, want 0", len(events.events))
	}
}

func TestEmbeddingWorkerBatchesChunksAndCompletesJob(t *testing.T) {
	const (
		jobID      int64 = 7
		documentID int64 = 11
	)
	job := embeddingdomain.Job{
		ID:           jobID,
		DocumentID:   documentID,
		ModelName:    "test-model",
		Dimensions:   2,
		Status:       embeddingdomain.JobStatusProcessing,
		AttemptCount: 1,
	}
	chunks := []documentdomain.TextChunk{
		{ID: 101, DocumentID: documentID, Index: 0, Content: "first"},
		{ID: 102, DocumentID: documentID, Index: 1, Content: "second"},
		{ID: 103, DocumentID: documentID, Index: 2, Content: "third"},
	}

	jobs := &fakeEmbeddingJobWorkerRepository{
		claimFunc: func(context.Context) (embeddingdomain.Job, error) {
			return job, nil
		},
		succeedFunc: func(
			_ context.Context,
			actualJobID int64,
			completion embeddingdomain.JobCompletion,
		) error {
			if actualJobID != jobID {
				t.Fatalf("MarkEmbeddingJobSucceeded() job ID = %d, want %d", actualJobID, jobID)
			}
			want := embeddingdomain.JobCompletion{
				Vectors: []embeddingdomain.ChunkVector{
					{ChunkID: 101, Values: []float32{1, 1}},
					{ChunkID: 102, Values: []float32{2, 2}},
					{ChunkID: 103, Values: []float32{3, 3}},
				},
				PromptTokens: 6,
				TotalTokens:  9,
			}
			if !reflect.DeepEqual(completion, want) {
				t.Fatalf("completion = %+v, want %+v", completion, want)
			}
			return nil
		},
		requeueFunc: failOnEmbeddingRequeue(t),
		failFunc:    failOnEmbeddingFailure(t),
	}
	chunkLister := &fakeEmbeddingChunkLister{
		listFunc: func(_ context.Context, actualDocumentID int64) ([]documentdomain.TextChunk, error) {
			if actualDocumentID != documentID {
				t.Fatalf("ListByDocumentID() document ID = %d, want %d", actualDocumentID, documentID)
			}
			return chunks, nil
		},
	}
	embedder := &fakeEmbedder{
		embedFunc: func(
			_ context.Context,
			request embeddingdomain.EmbedRequest,
		) (embeddingdomain.EmbedResult, error) {
			if request.Model != job.ModelName || request.Dimensions != job.Dimensions {
				t.Fatalf("Embed() config = (%q, %d), want (%q, %d)", request.Model, request.Dimensions, job.ModelName, job.Dimensions)
			}

			switch request.Inputs[0] {
			case "first":
				if !reflect.DeepEqual(request.Inputs, []string{"first", "second"}) {
					t.Fatalf("first batch inputs = %v", request.Inputs)
				}
				return embeddingdomain.EmbedResult{
					Vectors:      [][]float32{{1, 1}, {2, 2}},
					PromptTokens: 4,
					TotalTokens:  6,
				}, nil
			case "third":
				return embeddingdomain.EmbedResult{
					Vectors:      [][]float32{{3, 3}},
					PromptTokens: 2,
					TotalTokens:  3,
				}, nil
			default:
				t.Fatalf("unexpected batch inputs = %v", request.Inputs)
				return embeddingdomain.EmbedResult{}, nil
			}
		},
	}
	events := newRecordingJobEventObserver()
	worker := newTestEmbeddingWorker(t, jobs, chunkLister, embedder, 2, time.Now, events)

	handled, err := worker.RunOnce(context.Background())

	if err != nil {
		t.Fatalf("RunOnce() error = %v, want nil", err)
	}
	if !handled {
		t.Fatal("RunOnce() handled = false, want true")
	}
	if embedder.embedCalls != 2 {
		t.Fatalf("Embed() calls = %d, want 2", embedder.embedCalls)
	}
	assertEmbeddingFinalizationCalls(t, jobs, 1, 0, 0)
	if len(events.events) != 2 {
		t.Fatalf("embedding events = %d, want 2", len(events.events))
	}
	assertJobEventIdentity(
		t,
		events.events[1],
		JobEventSucceeded,
		embeddingdomain.JobStatusSucceeded,
		job,
	)
	if events.events[1].ProviderCallCount != 2 ||
		events.events[1].PromptTokens != 6 ||
		events.events[1].TotalTokens != 9 ||
		events.events[1].GeneratedVectorCount != 3 {
		t.Fatalf("succeeded metrics = %+v", events.events[1])
	}
}

func TestEmbeddingWorkerRequeuesTemporaryProviderFailure(t *testing.T) {
	fixedNow := time.Date(2026, time.August, 11, 10, 0, 0, 0, time.UTC)
	job := embeddingdomain.Job{
		ID: 7, DocumentID: 11, ModelName: "test-model", Dimensions: 2,
		Status: embeddingdomain.JobStatusProcessing, AttemptCount: 1,
	}
	jobs := &fakeEmbeddingJobWorkerRepository{
		claimFunc:   func(context.Context) (embeddingdomain.Job, error) { return job, nil },
		succeedFunc: failOnEmbeddingSuccess(t),
		requeueFunc: func(
			_ context.Context,
			jobID int64,
			nextAttemptAt time.Time,
			errorMessage string,
		) error {
			if jobID != job.ID {
				t.Fatalf("RequeueEmbeddingJob() job ID = %d, want %d", jobID, job.ID)
			}
			if !nextAttemptAt.Equal(fixedNow.Add(30 * time.Second)) {
				t.Fatalf("next attempt = %v, want %v", nextAttemptAt, fixedNow.Add(30*time.Second))
			}
			if errorMessage != safeEmbeddingRetryMessage {
				t.Fatalf("retry message = %q, want %q", errorMessage, safeEmbeddingRetryMessage)
			}
			return nil
		},
		failFunc: failOnEmbeddingFailure(t),
	}
	chunks := oneChunkLister()
	embedder := &fakeEmbedder{
		embedFunc: func(context.Context, embeddingdomain.EmbedRequest) (embeddingdomain.EmbedResult, error) {
			return embeddingdomain.EmbedResult{}, embeddingdomain.ErrEmbeddingRateLimited
		},
	}
	events := newRecordingJobEventObserver()
	worker := newTestEmbeddingWorker(
		t,
		jobs,
		chunks,
		embedder,
		2,
		func() time.Time { return fixedNow },
		events,
	)

	handled, err := worker.RunOnce(context.Background())

	if !handled {
		t.Fatal("RunOnce() handled = false, want true")
	}
	if !errors.Is(err, embeddingdomain.ErrEmbeddingRateLimited) {
		t.Fatalf("RunOnce() error = %v, want ErrEmbeddingRateLimited", err)
	}
	assertEmbeddingFinalizationCalls(t, jobs, 0, 1, 0)
	if len(events.events) != 2 {
		t.Fatalf("embedding events = %d, want 2", len(events.events))
	}
	assertJobEventIdentity(
		t,
		events.events[1],
		JobEventRequeued,
		embeddingdomain.JobStatusQueued,
		job,
	)
	if events.events[1].ErrorCategory != JobErrorCategoryProviderRateLimit {
		t.Fatalf("error category = %q, want rate limit", events.events[1].ErrorCategory)
	}
	if events.events[1].NextAttemptAt == nil ||
		!events.events[1].NextAttemptAt.Equal(fixedNow.Add(30*time.Second)) {
		t.Fatalf("next attempt = %v", events.events[1].NextAttemptAt)
	}
}

func TestEmbeddingWorkerPreservesPartialUsageBeforeLaterBatchFailure(t *testing.T) {
	fixedNow := time.Date(2026, time.August, 15, 10, 0, 0, 0, time.UTC)
	job := embeddingdomain.Job{
		ID: 27, DocumentID: 31, ModelName: "test-model", Dimensions: 2,
		Status: embeddingdomain.JobStatusProcessing, AttemptCount: 1,
	}
	jobs := &fakeEmbeddingJobWorkerRepository{
		claimFunc:   func(context.Context) (embeddingdomain.Job, error) { return job, nil },
		succeedFunc: failOnEmbeddingSuccess(t),
		requeueFunc: func(context.Context, int64, time.Time, string) error { return nil },
		failFunc:    failOnEmbeddingFailure(t),
	}
	chunks := &fakeEmbeddingChunkLister{
		listFunc: func(context.Context, int64) ([]documentdomain.TextChunk, error) {
			return []documentdomain.TextChunk{
				{ID: 301, DocumentID: job.DocumentID, Index: 0, Content: "first"},
				{ID: 302, DocumentID: job.DocumentID, Index: 1, Content: "second"},
			}, nil
		},
	}
	embedder := &fakeEmbedder{
		embedFunc: func(
			_ context.Context,
			request embeddingdomain.EmbedRequest,
		) (embeddingdomain.EmbedResult, error) {
			if request.Inputs[0] == "first" {
				return embeddingdomain.EmbedResult{
					Vectors:      [][]float32{{1, 1}},
					PromptTokens: 4,
					TotalTokens:  6,
				}, nil
			}
			return embeddingdomain.EmbedResult{}, embeddingdomain.ErrEmbeddingRateLimited
		},
	}
	events := newRecordingJobEventObserver()
	worker := newTestEmbeddingWorker(
		t,
		jobs,
		chunks,
		embedder,
		1,
		func() time.Time { return fixedNow },
		events,
	)

	_, err := worker.RunOnce(context.Background())

	if !errors.Is(err, embeddingdomain.ErrEmbeddingRateLimited) {
		t.Fatalf("RunOnce() error = %v, want rate limit", err)
	}
	assertEmbeddingFinalizationCalls(t, jobs, 0, 1, 0)
	finalEvent := events.events[1]
	if finalEvent.ProviderCallCount != 2 ||
		finalEvent.PromptTokens != 4 ||
		finalEvent.TotalTokens != 6 ||
		finalEvent.GeneratedVectorCount != 1 {
		t.Fatalf("partial usage metrics = %+v", finalEvent)
	}
}

func TestEmbeddingWorkerFailsWholeDocumentWhenLaterChunkVectorIsInvalid(t *testing.T) {
	job := embeddingdomain.Job{
		ID: 29, DocumentID: 33, ModelName: "test-model", Dimensions: 2,
		Status: embeddingdomain.JobStatusProcessing, AttemptCount: 1,
	}
	jobs := &fakeEmbeddingJobWorkerRepository{
		claimFunc:   func(context.Context) (embeddingdomain.Job, error) { return job, nil },
		succeedFunc: failOnEmbeddingSuccess(t),
		requeueFunc: func(context.Context, int64, time.Time, string) error {
			t.Fatal("invalid vector response must not be requeued")
			return nil
		},
		failFunc: func(_ context.Context, jobID int64, errorMessage string) error {
			if jobID != job.ID || errorMessage != safeEmbeddingFailureMessage {
				t.Fatalf("MarkEmbeddingJobFailed() received job=%d message=%q", jobID, errorMessage)
			}
			return nil
		},
	}
	chunks := &fakeEmbeddingChunkLister{
		listFunc: func(context.Context, int64) ([]documentdomain.TextChunk, error) {
			return []documentdomain.TextChunk{
				{ID: 401, DocumentID: job.DocumentID, Index: 0, Content: "valid chunk"},
				{ID: 402, DocumentID: job.DocumentID, Index: 1, Content: "invalid chunk"},
			}, nil
		},
	}
	embedder := &fakeEmbedder{
		embedFunc: func(_ context.Context, request embeddingdomain.EmbedRequest) (embeddingdomain.EmbedResult, error) {
			if request.Inputs[0] == "valid chunk" {
				return embeddingdomain.EmbedResult{Vectors: [][]float32{{1, 1}}}, nil
			}
			// 第二个 chunk 返回错误维度。第一批结果仍只存在于内存，
			// 不会调用成功收尾，也就不会向数据库提交任何新向量。
			return embeddingdomain.EmbedResult{Vectors: [][]float32{{2}}}, nil
		},
	}
	worker := newTestEmbeddingWorker(
		t,
		jobs,
		chunks,
		embedder,
		1,
		time.Now,
		newRecordingJobEventObserver(),
	)

	handled, err := worker.RunOnce(context.Background())

	if !handled {
		t.Fatal("RunOnce() handled = false, want true")
	}
	if !errors.Is(err, embeddingdomain.ErrInvalidEmbeddingResponse) {
		t.Fatalf("RunOnce() error = %v, want ErrInvalidEmbeddingResponse", err)
	}
	assertEmbeddingFinalizationCalls(t, jobs, 0, 0, 1)
}

func TestEmbeddingWorkerReportsUnfinishedWhenRequeueFails(t *testing.T) {
	providerError := embeddingdomain.ErrEmbeddingRateLimited
	requeueError := errors.New("database unavailable")
	job := embeddingdomain.Job{
		ID: 28, DocumentID: 32, ModelName: "test-model", Dimensions: 2,
		Status: embeddingdomain.JobStatusProcessing, AttemptCount: 1,
	}
	jobs := &fakeEmbeddingJobWorkerRepository{
		claimFunc:   func(context.Context) (embeddingdomain.Job, error) { return job, nil },
		succeedFunc: failOnEmbeddingSuccess(t),
		requeueFunc: func(context.Context, int64, time.Time, string) error { return requeueError },
		failFunc:    failOnEmbeddingFailure(t),
	}
	embedder := &fakeEmbedder{
		embedFunc: func(context.Context, embeddingdomain.EmbedRequest) (embeddingdomain.EmbedResult, error) {
			return embeddingdomain.EmbedResult{}, providerError
		},
	}
	events := newRecordingJobEventObserver()
	worker := newTestEmbeddingWorker(t, jobs, oneChunkLister(), embedder, 1, time.Now, events)

	_, err := worker.RunOnce(context.Background())

	if !errors.Is(err, providerError) || !errors.Is(err, requeueError) {
		t.Fatalf("RunOnce() error = %v, want provider and requeue errors", err)
	}
	assertJobEventIdentity(
		t,
		events.events[1],
		JobEventUnfinished,
		embeddingdomain.JobStatusProcessing,
		job,
	)
}

func TestEmbeddingWorkerFailsPermanentAuthenticationError(t *testing.T) {
	job := embeddingdomain.Job{
		ID: 7, DocumentID: 11, ModelName: "test-model", Dimensions: 2,
		Status: embeddingdomain.JobStatusProcessing, AttemptCount: 1,
	}
	jobs := &fakeEmbeddingJobWorkerRepository{
		claimFunc:   func(context.Context) (embeddingdomain.Job, error) { return job, nil },
		succeedFunc: failOnEmbeddingSuccess(t),
		requeueFunc: failOnEmbeddingRequeue(t),
		failFunc: func(_ context.Context, jobID int64, errorMessage string) error {
			if jobID != job.ID || errorMessage != safeEmbeddingFailureMessage {
				t.Fatalf("MarkEmbeddingJobFailed() args = (%d, %q)", jobID, errorMessage)
			}
			return nil
		},
	}
	embedder := &fakeEmbedder{
		embedFunc: func(context.Context, embeddingdomain.EmbedRequest) (embeddingdomain.EmbedResult, error) {
			return embeddingdomain.EmbedResult{}, embeddingdomain.ErrEmbeddingAuthentication
		},
	}
	events := newRecordingJobEventObserver()
	worker := newTestEmbeddingWorker(t, jobs, oneChunkLister(), embedder, 2, time.Now, events)

	_, err := worker.RunOnce(context.Background())

	if !errors.Is(err, embeddingdomain.ErrEmbeddingAuthentication) {
		t.Fatalf("RunOnce() error = %v, want ErrEmbeddingAuthentication", err)
	}
	assertEmbeddingFinalizationCalls(t, jobs, 0, 0, 1)
	assertJobEventIdentity(
		t,
		events.events[1],
		JobEventFailed,
		embeddingdomain.JobStatusFailed,
		job,
	)
	if events.events[1].ErrorCategory != JobErrorCategoryProviderAuthentication {
		t.Fatalf("error category = %q, want authentication", events.events[1].ErrorCategory)
	}
}

func TestEmbeddingWorkerFailsPermanentQuotaError(t *testing.T) {
	job := embeddingdomain.Job{
		ID: 7, DocumentID: 11, ModelName: "test-model", Dimensions: 2,
		Status: embeddingdomain.JobStatusProcessing, AttemptCount: 1,
	}
	jobs := &fakeEmbeddingJobWorkerRepository{
		claimFunc:   func(context.Context) (embeddingdomain.Job, error) { return job, nil },
		succeedFunc: failOnEmbeddingSuccess(t),
		requeueFunc: failOnEmbeddingRequeue(t),
		failFunc: func(_ context.Context, jobID int64, errorMessage string) error {
			if jobID != job.ID || errorMessage != safeEmbeddingFailureMessage {
				t.Fatalf("MarkEmbeddingJobFailed() args = (%d, %q)", jobID, errorMessage)
			}
			return nil
		},
	}
	embedder := &fakeEmbedder{
		embedFunc: func(context.Context, embeddingdomain.EmbedRequest) (embeddingdomain.EmbedResult, error) {
			return embeddingdomain.EmbedResult{}, embeddingdomain.ErrEmbeddingQuotaExceeded
		},
	}
	events := newRecordingJobEventObserver()
	worker := newTestEmbeddingWorker(t, jobs, oneChunkLister(), embedder, 2, time.Now, events)

	_, err := worker.RunOnce(context.Background())

	if !errors.Is(err, embeddingdomain.ErrEmbeddingQuotaExceeded) {
		t.Fatalf("RunOnce() error = %v, want ErrEmbeddingQuotaExceeded", err)
	}
	assertEmbeddingFinalizationCalls(t, jobs, 0, 0, 1)
	if events.events[1].ErrorCategory != JobErrorCategoryProviderQuota {
		t.Fatalf("error category = %q, want quota", events.events[1].ErrorCategory)
	}
}

func TestEmbeddingWorkerDoesNotFinalizeShutdownCancellation(t *testing.T) {
	job := embeddingdomain.Job{
		ID: 7, DocumentID: 11, ModelName: "test-model", Dimensions: 2,
		Status: embeddingdomain.JobStatusProcessing, AttemptCount: 1,
	}
	jobs := &fakeEmbeddingJobWorkerRepository{
		claimFunc:   func(context.Context) (embeddingdomain.Job, error) { return job, nil },
		succeedFunc: failOnEmbeddingSuccess(t),
		requeueFunc: failOnEmbeddingRequeue(t),
		failFunc:    failOnEmbeddingFailure(t),
	}
	ctx, cancel := context.WithCancel(context.Background())
	embedder := &fakeEmbedder{
		embedFunc: func(ctx context.Context, _ embeddingdomain.EmbedRequest) (embeddingdomain.EmbedResult, error) {
			cancel()
			<-ctx.Done()
			return embeddingdomain.EmbedResult{}, ctx.Err()
		},
	}
	events := newRecordingJobEventObserver()
	worker := newTestEmbeddingWorker(t, jobs, oneChunkLister(), embedder, 2, time.Now, events)

	handled, err := worker.RunOnce(ctx)

	if !handled {
		t.Fatal("RunOnce() handled = false, want true")
	}
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("RunOnce() error = %v, want context.Canceled", err)
	}
	assertEmbeddingFinalizationCalls(t, jobs, 0, 0, 0)
	assertJobEventIdentity(
		t,
		events.events[1],
		JobEventInterrupted,
		embeddingdomain.JobStatusProcessing,
		job,
	)
	if events.events[1].ErrorCategory != JobErrorCategoryCanceled {
		t.Fatalf("error category = %q, want canceled", events.events[1].ErrorCategory)
	}
}

func newTestEmbeddingWorker(
	t *testing.T,
	jobs embeddingJobWorkerRepository,
	chunks documentdomain.ChunkLister,
	embedder embeddingdomain.Embedder,
	batchSize int,
	now func() time.Time,
	observers ...JobEventObserver,
) *Worker {
	t.Helper()
	observer := JobEventObserver(newRecordingJobEventObserver())
	if len(observers) > 1 {
		t.Fatal("newTestEmbeddingWorker accepts at most one observer")
	}
	if len(observers) == 1 {
		observer = observers[0]
	}
	policy, err := newRetryPolicy(
		3,
		30*time.Second,
		5*time.Minute,
		func(delayLimit time.Duration) time.Duration { return delayLimit },
	)
	if err != nil {
		t.Fatalf("newRetryPolicy() error = %v", err)
	}
	worker, err := newWorker(
		jobs,
		chunks,
		embedder,
		observer,
		batchSize,
		time.Minute,
		policy,
		now,
	)
	if err != nil {
		t.Fatalf("newWorker() error = %v", err)
	}
	return worker
}

func oneChunkLister() *fakeEmbeddingChunkLister {
	return &fakeEmbeddingChunkLister{
		listFunc: func(_ context.Context, documentID int64) ([]documentdomain.TextChunk, error) {
			return []documentdomain.TextChunk{
				{ID: 101, DocumentID: documentID, Index: 0, Content: "text"},
			}, nil
		},
	}
}

func failOnEmbeddingSuccess(
	t *testing.T,
) func(context.Context, int64, embeddingdomain.JobCompletion) error {
	t.Helper()
	return func(context.Context, int64, embeddingdomain.JobCompletion) error {
		t.Fatal("MarkEmbeddingJobSucceeded() must not be called")
		return nil
	}
}

func failOnEmbeddingRequeue(
	t *testing.T,
) func(context.Context, int64, time.Time, string) error {
	t.Helper()
	return func(context.Context, int64, time.Time, string) error {
		t.Fatal("RequeueEmbeddingJob() must not be called")
		return nil
	}
}

func failOnEmbeddingFailure(
	t *testing.T,
) func(context.Context, int64, string) error {
	t.Helper()
	return func(context.Context, int64, string) error {
		t.Fatal("MarkEmbeddingJobFailed() must not be called")
		return nil
	}
}

func assertEmbeddingFinalizationCalls(
	t *testing.T,
	jobs *fakeEmbeddingJobWorkerRepository,
	wantSucceeded int,
	wantRequeued int,
	wantFailed int,
) {
	t.Helper()
	if jobs.succeedCalls != wantSucceeded ||
		jobs.requeueCalls != wantRequeued ||
		jobs.failCalls != wantFailed {
		t.Fatalf(
			"finalization calls = (succeeded:%d requeued:%d failed:%d), want (%d %d %d)",
			jobs.succeedCalls,
			jobs.requeueCalls,
			jobs.failCalls,
			wantSucceeded,
			wantRequeued,
			wantFailed,
		)
	}
}
