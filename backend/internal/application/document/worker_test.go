package document

import (
	"context"
	"errors"
	"testing"

	documentdomain "rag-reasoning-platform/backend/internal/domain/document"
)

// fakeProcessingJobClaimer 是文档处理 Worker 单元测试使用的测试替身（fake）。
//
// 它不会连接 PostgreSQL，而是通过三个可注入的函数模拟任务仓储的领取、成功收尾和
// 失败收尾行为。测试可以让这些函数返回指定结果，再通过 Calls 字段核对 Worker 是否
// 按预期调用了依赖。
//
// 虽然历史名称只写了 Claimer，但它实际上同时满足 ProcessingJobClaimer 和
// ProcessingJobFinalizer；这是因为 Worker 既需要领取任务，也需要更新最终状态。
type fakeProcessingJobClaimer struct {
	// 三个 Func 字段定义本次测试希望仓储表现出的行为。
	claimNextFunc     func(context.Context) (documentdomain.ProcessingJob, error)
	markSucceededFunc func(context.Context, int64, documentdomain.ProcessingCompletion) error
	markFailedFunc    func(context.Context, int64, string) error

	// 三个 Calls 字段记录对应方法实际被调用的次数，供测试断言使用。
	claimNextCalls     int
	markSucceededCalls int
	markFailedCalls    int
}

// ClaimNextProcessingJob 模拟从任务队列领取下一条文档处理任务。
func (f *fakeProcessingJobClaimer) ClaimNextProcessingJob(
	ctx context.Context,
) (documentdomain.ProcessingJob, error) {
	f.claimNextCalls++
	return f.claimNextFunc(ctx)
}

// MarkProcessingJobSucceeded 模拟任务成功后的仓储收尾，并记录调用次数。
func (f *fakeProcessingJobClaimer) MarkProcessingJobSucceeded(
	ctx context.Context,
	jobID int64,
	completion documentdomain.ProcessingCompletion,
) error {
	f.markSucceededCalls++
	if f.markSucceededFunc == nil {
		return nil
	}
	return f.markSucceededFunc(ctx, jobID, completion)
}

// MarkProcessingJobFailed 模拟任务失败后的仓储收尾，并记录调用次数。
func (f *fakeProcessingJobClaimer) MarkProcessingJobFailed(
	ctx context.Context,
	jobID int64,
	errorMessage string,
) error {
	f.markFailedCalls++
	if f.markFailedFunc == nil {
		return nil
	}
	return f.markFailedFunc(ctx, jobID, errorMessage)
}

func TestWorkerClaimNextReturnsIdleWhenQueueIsEmpty(t *testing.T) {
	claimer := &fakeProcessingJobClaimer{
		claimNextFunc: func(
			context.Context,
		) (documentdomain.ProcessingJob, error) {
			return documentdomain.ProcessingJob{},
				documentdomain.ErrNoQueuedProcessingJob
		},
	}
	worker := NewWorker(
		claimer,
		nil,
		nil,
		nil,
		newRecordingProcessingJobEventObserver(),
		testWorkerProcessingTimeout,
	)

	job, claimed, err := worker.ClaimNext(context.Background())

	if err != nil {
		t.Fatalf("ClaimNext() error = %v, want nil", err)
	}
	if claimed {
		t.Fatal("ClaimNext() claimed = true, want false")
	}
	if job != (documentdomain.ProcessingJob{}) {
		t.Fatalf("ClaimNext() job = %+v, want zero value", job)
	}
	if claimer.claimNextCalls != 1 {
		t.Fatalf(
			"ClaimNextProcessingJob() calls = %d, want 1",
			claimer.claimNextCalls,
		)
	}
}

func TestWorkerClaimNextReturnsClaimedJob(t *testing.T) {
	expectedJob := documentdomain.ProcessingJob{
		ID:           17,
		DocumentID:   7,
		Status:       documentdomain.ProcessingJobStatusProcessing,
		AttemptCount: 1,
	}
	claimer := &fakeProcessingJobClaimer{
		claimNextFunc: func(
			context.Context,
		) (documentdomain.ProcessingJob, error) {
			return expectedJob, nil
		},
	}
	worker := NewWorker(
		claimer,
		nil,
		nil,
		nil,
		newRecordingProcessingJobEventObserver(),
		testWorkerProcessingTimeout,
	)

	job, claimed, err := worker.ClaimNext(context.Background())

	if err != nil {
		t.Fatalf("ClaimNext() error = %v, want nil", err)
	}
	if !claimed {
		t.Fatal("ClaimNext() claimed = false, want true")
	}
	if job != expectedJob {
		t.Fatalf(
			"ClaimNext() job = %+v, want %+v",
			job,
			expectedJob,
		)
	}
	if claimer.claimNextCalls != 1 {
		t.Fatalf(
			"ClaimNextProcessingJob() calls = %d, want 1",
			claimer.claimNextCalls,
		)
	}
}

func TestWorkerClaimNextPreservesUnexpectedError(t *testing.T) {
	databaseError := errors.New("database unavailable")
	claimer := &fakeProcessingJobClaimer{
		claimNextFunc: func(
			context.Context,
		) (documentdomain.ProcessingJob, error) {
			return documentdomain.ProcessingJob{}, databaseError
		},
	}
	worker := NewWorker(
		claimer,
		nil,
		nil,
		nil,
		newRecordingProcessingJobEventObserver(),
		testWorkerProcessingTimeout,
	)

	job, claimed, err := worker.ClaimNext(context.Background())

	if !errors.Is(err, databaseError) {
		t.Fatalf(
			"ClaimNext() error = %v, want wrapped database error",
			err,
		)
	}
	if claimed {
		t.Fatal("ClaimNext() claimed = true, want false")
	}
	if job != (documentdomain.ProcessingJob{}) {
		t.Fatalf("ClaimNext() job = %+v, want zero value", job)
	}
	if claimer.claimNextCalls != 1 {
		t.Fatalf(
			"ClaimNextProcessingJob() calls = %d, want 1",
			claimer.claimNextCalls,
		)
	}
}
