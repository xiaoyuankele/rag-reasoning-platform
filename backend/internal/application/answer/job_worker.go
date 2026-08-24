package answer

import (
	"context"
	"errors"
	"fmt"
	"time"

	embeddingapplication "rag-reasoning-platform/backend/internal/application/embedding"
	accessdomain "rag-reasoning-platform/backend/internal/domain/access"
	documentdomain "rag-reasoning-platform/backend/internal/domain/document"
	embeddingdomain "rag-reasoning-platform/backend/internal/domain/embedding"
	generationdomain "rag-reasoning-platform/backend/internal/domain/generation"
)

const (
	safeAnswerJobRetryMessage    = "answer service temporarily unavailable"
	safeAnswerJobFailureMessage  = "answer generation failed"
	safeAnswerJobRecoveryMessage = "answer worker was interrupted and the task was requeued"
)

var (
	ErrAnswerJobWorkerDependencies = errors.New(
		"answer job worker dependencies must be provided",
	)
	ErrInvalidAnswerJobWorkerTimeout = errors.New(
		"answer job processing timeout must be positive",
	)
	ErrInvalidAnswerJobRetryPolicy = errors.New(
		"answer job retry policy is invalid",
	)
)

// JobEventType 是异步问答 Worker 的安全观测节点。
type JobEventType string

const (
	JobEventStarted     JobEventType = "answer_job_started"
	JobEventSucceeded   JobEventType = "answer_job_succeeded"
	JobEventRequeued    JobEventType = "answer_job_requeued"
	JobEventFailed      JobEventType = "answer_job_failed"
	JobEventInterrupted JobEventType = "answer_job_interrupted"
	JobEventUnfinished  JobEventType = "answer_job_unfinished"
)

// JobErrorCategory 是后端报告使用的稳定诊断分类。
// 它不替代返回前端的 JobErrorCode，也不会包含供应商原始响应。
type JobErrorCategory string

const (
	JobErrorCategoryInvalidInput             JobErrorCategory = "invalid_input"
	JobErrorCategoryDocumentNotFound         JobErrorCategory = "document_not_found"
	JobErrorCategoryEmbeddingsNotReady       JobErrorCategory = "document_embeddings_not_ready"
	JobErrorCategoryAnswerCapacity           JobErrorCategory = "answer_capacity"
	JobErrorCategoryEmbeddingCapacity        JobErrorCategory = "embedding_capacity"
	JobErrorCategoryEmbeddingAuthentication  JobErrorCategory = "embedding_authentication"
	JobErrorCategoryEmbeddingQuota           JobErrorCategory = "embedding_quota"
	JobErrorCategoryEmbeddingRateLimit       JobErrorCategory = "embedding_rate_limit"
	JobErrorCategoryEmbeddingRequest         JobErrorCategory = "embedding_request_rejected"
	JobErrorCategoryEmbeddingResponse        JobErrorCategory = "embedding_invalid_response"
	JobErrorCategoryEmbeddingUnavailable     JobErrorCategory = "embedding_unavailable"
	JobErrorCategoryGenerationAuthentication JobErrorCategory = "generation_authentication"
	JobErrorCategoryGenerationQuota          JobErrorCategory = "generation_quota"
	JobErrorCategoryGenerationRateLimit      JobErrorCategory = "generation_rate_limit"
	JobErrorCategoryGenerationRequest        JobErrorCategory = "generation_request_rejected"
	JobErrorCategoryGenerationResponse       JobErrorCategory = "generation_invalid_response"
	JobErrorCategoryGenerationUnavailable    JobErrorCategory = "generation_unavailable"
	JobErrorCategoryTimeout                  JobErrorCategory = "timeout"
	JobErrorCategoryCanceled                 JobErrorCategory = "canceled"
	JobErrorCategoryInternal                 JobErrorCategory = "internal"
)

// JobEvent 不包含 Owner ID、问题、Prompt、答案或来源正文。
type JobEvent struct {
	Type              JobEventType
	JobID             int64
	Status            JobStatus
	AttemptCount      int
	RetryCount        int
	Recovered         bool
	QueueWait         time.Duration
	ExecutionDuration time.Duration
	TotalDuration     time.Duration
	QueueStats        *JobQueueStats
	QueueStatsError   error
	NextAttemptAt     *time.Time
	ErrorCode         JobErrorCode
	ErrorCategory     JobErrorCategory
	Err               error
}

// JobEventObserver 是 Worker 向 Observability 输出事件的端口。
type JobEventObserver interface {
	ObserveAnswerJobEvent(context.Context, JobEvent)
}

// JobRetryPolicy 控制临时失败的有限指数退避。
type JobRetryPolicy struct {
	maxAttempts int
	baseDelay   time.Duration
	maxDelay    time.Duration
}

// NewJobRetryPolicy 创建异步问答重试策略。
func NewJobRetryPolicy(
	maxAttempts int,
	baseDelay time.Duration,
	maxDelay time.Duration,
) (JobRetryPolicy, error) {
	if maxAttempts <= 0 || baseDelay <= 0 || maxDelay < baseDelay {
		return JobRetryPolicy{}, ErrInvalidAnswerJobRetryPolicy
	}
	return JobRetryPolicy{
		maxAttempts: maxAttempts,
		baseDelay:   baseDelay,
		maxDelay:    maxDelay,
	}, nil
}

func (p JobRetryPolicy) nextAttempt(
	attemptCount int,
	now time.Time,
) (time.Time, bool) {
	if attemptCount >= p.maxAttempts {
		return time.Time{}, false
	}
	delay := p.baseDelay
	for retryIndex := 1; retryIndex < attemptCount; retryIndex++ {
		if delay >= p.maxDelay/2 {
			delay = p.maxDelay
			break
		}
		delay *= 2
	}
	if delay > p.maxDelay {
		delay = p.maxDelay
	}
	return now.Add(delay), true
}

// Worker 领取一条持久化任务，并调用已有问答服务完成完整 RAG 链路。
type JobWorker struct {
	jobs              JobWorkerRepository
	answerer          answerer
	events            JobEventObserver
	processingTimeout time.Duration
	retryPolicy       JobRetryPolicy
	now               func() time.Time
}

// NewJobWorker 创建异步问答 Worker。
func NewJobWorker(
	jobs JobWorkerRepository,
	answerer answerer,
	events JobEventObserver,
	processingTimeout time.Duration,
	retryPolicy JobRetryPolicy,
) (*JobWorker, error) {
	if jobs == nil || answerer == nil || events == nil {
		return nil, ErrAnswerJobWorkerDependencies
	}
	if processingTimeout <= 0 {
		return nil, ErrInvalidAnswerJobWorkerTimeout
	}
	if retryPolicy.maxAttempts <= 0 {
		return nil, ErrInvalidAnswerJobRetryPolicy
	}
	return &JobWorker{
		jobs:              jobs,
		answerer:          answerer,
		events:            events,
		processingTimeout: processingTimeout,
		retryPolicy:       retryPolicy,
		now:               time.Now,
	}, nil
}

// RunOnce 领取并执行一条任务；空队列返回 handled=false、err=nil。
func (w *JobWorker) RunOnce(ctx context.Context) (handled bool, err error) {
	job, err := w.jobs.ClaimNextAnswerJob(ctx)
	if errors.Is(err, ErrNoQueuedAnswerJob) {
		return false, nil
	}
	if err != nil {
		return false, fmt.Errorf("claim next answer job: %w", err)
	}

	startedAt := time.Now()
	queueWait := answerJobQueueWait(job, startedAt)
	retryCount := max(job.AttemptCount-1, 0)
	finalEvent := JobEvent{
		Type:         JobEventUnfinished,
		JobID:        job.ID,
		Status:       JobStatusProcessing,
		AttemptCount: job.AttemptCount,
		RetryCount:   retryCount,
		QueueWait:    queueWait,
	}
	w.observeJobEvent(ctx, JobEvent{
		Type:         JobEventStarted,
		JobID:        job.ID,
		Status:       JobStatusProcessing,
		AttemptCount: job.AttemptCount,
		RetryCount:   retryCount,
		QueueWait:    queueWait,
	})
	defer func() {
		finishedAt := time.Now()
		finalEvent.ExecutionDuration = finishedAt.Sub(startedAt)
		finalEvent.TotalDuration = answerJobTotalDuration(
			job,
			finishedAt,
			finalEvent.ExecutionDuration,
		)
		finalEvent.Recovered = finalEvent.Type == JobEventSucceeded && retryCount > 0
		finalEvent.ErrorCategory = classifyAnswerJobError(err)
		finalEvent.Err = err
		w.observeJobEvent(ctx, finalEvent)
	}()

	scope, scopeErr := accessdomain.NewOwnerScope(job.OwnerUserID)
	if scopeErr != nil {
		err = w.failJob(ctx, job, scopeErr)
		finalEvent.Type = JobEventFailed
		finalEvent.Status = JobStatusFailed
		finalEvent.ErrorCode = JobErrorCodeExecutionFailed
		return true, err
	}

	processingContext, cancel := context.WithTimeout(ctx, w.processingTimeout)
	output, processingErr := w.answerer.Answer(
		processingContext,
		scope,
		Input{
			Query:            job.Query,
			DocumentID:       job.DocumentID,
			TopK:             job.TopK,
			ResponseLanguage: job.RequestedResponseLanguage,
		},
	)
	cancel()

	// shutdown 期间不执行数据库收尾；启动恢复会把遗留 processing 重新排队。
	if ctx.Err() != nil {
		finalEvent.Type = JobEventInterrupted
		finalEvent.ErrorCode = JobErrorCodeWorkerInterrupted
		err = fmt.Errorf("answer job %d interrupted: %w", job.ID, ctx.Err())
		return true, err
	}

	if processingErr != nil {
		if !isPermanentAnswerJobError(processingErr) {
			if nextAttemptAt, retry := w.retryPolicy.nextAttempt(job.AttemptCount, w.now()); retry {
				requeueErr := w.jobs.RequeueAnswerJob(
					ctx,
					job.ID,
					nextAttemptAt,
					JobErrorCodeTemporarilyUnavailable,
					safeAnswerJobRetryMessage,
				)
				finalEvent.Type = JobEventRequeued
				finalEvent.Status = JobStatusQueued
				finalEvent.ErrorCode = JobErrorCodeTemporarilyUnavailable
				finalEvent.NextAttemptAt = &nextAttemptAt
				if requeueErr != nil {
					err = errors.Join(
						processingErr,
						fmt.Errorf("requeue answer job %d: %w", job.ID, requeueErr),
					)
					finalEvent.Type = JobEventUnfinished
					finalEvent.Status = JobStatusProcessing
					return true, err
				}
				err = processingErr
				return true, err
			}
		}

		err = w.failJob(ctx, job, processingErr)
		finalEvent.Type = JobEventFailed
		finalEvent.Status = JobStatusFailed
		finalEvent.ErrorCode = JobErrorCodeExecutionFailed
		return true, err
	}

	if finalizeErr := w.jobs.MarkAnswerJobSucceeded(ctx, job.ID, output); finalizeErr != nil {
		err = fmt.Errorf("mark answer job %d succeeded: %w", job.ID, finalizeErr)
		return true, err
	}
	finalEvent.Type = JobEventSucceeded
	finalEvent.Status = JobStatusSucceeded
	return true, nil
}

// observeJobEvent 尽力附加数据库队列快照。观测查询失败只写入事件，
// 不能改变已经完成的任务状态或 RunOnce 返回值。
func (w *JobWorker) observeJobEvent(ctx context.Context, event JobEvent) {
	if ctx.Err() == nil {
		stats, err := w.jobs.GetAnswerJobQueueStats(ctx)
		if err != nil {
			event.QueueStatsError = err
		} else {
			event.QueueStats = &stats
		}
	}
	w.events.ObserveAnswerJobEvent(ctx, event)
}

// answerJobQueueWait 计算“本次到期可领取”到数据库真正领取之间的时间。
// 重试任务从 next_attempt_at 开始计算，指数退避本身由 total_ms 体现。
func answerJobQueueWait(job Job, fallbackStartedAt time.Time) time.Duration {
	startedAt := fallbackStartedAt
	if job.StartedAt != nil && !job.StartedAt.IsZero() {
		startedAt = *job.StartedAt
	}
	readyAt := job.NextAttemptAt
	if readyAt.IsZero() {
		readyAt = job.CreatedAt
	}
	if readyAt.IsZero() || startedAt.Before(readyAt) {
		return 0
	}
	return startedAt.Sub(readyAt)
}

// answerJobTotalDuration 计算用户创建任务到当前事件的端到端时间。
func answerJobTotalDuration(
	job Job,
	finishedAt time.Time,
	fallback time.Duration,
) time.Duration {
	if job.CreatedAt.IsZero() || finishedAt.Before(job.CreatedAt) {
		return max(fallback, 0)
	}
	return finishedAt.Sub(job.CreatedAt)
}

func (w *JobWorker) failJob(
	ctx context.Context,
	job Job,
	processingErr error,
) error {
	if err := w.jobs.MarkAnswerJobFailed(
		ctx,
		job.ID,
		JobErrorCodeExecutionFailed,
		safeAnswerJobFailureMessage,
	); err != nil {
		return errors.Join(
			processingErr,
			fmt.Errorf("mark answer job %d failed: %w", job.ID, err),
		)
	}
	return processingErr
}

func isPermanentAnswerJobError(err error) bool {
	return errors.Is(err, embeddingapplication.ErrSemanticSearchQueryRequired) ||
		errors.Is(err, embeddingapplication.ErrSemanticSearchQueryInvalidUTF8) ||
		errors.Is(err, embeddingapplication.ErrSemanticSearchQueryTooLong) ||
		errors.Is(err, embeddingapplication.ErrInvalidSemanticSearchTopK) ||
		errors.Is(err, embeddingapplication.ErrInvalidDocumentID) ||
		errors.Is(err, embeddingapplication.ErrDocumentEmbeddingsNotReady) ||
		errors.Is(err, ErrInvalidResponseLanguage) ||
		errors.Is(err, documentdomain.ErrNotFound) ||
		errors.Is(err, embeddingdomain.ErrEmbeddingAuthentication) ||
		errors.Is(err, embeddingdomain.ErrEmbeddingQuotaExceeded) ||
		errors.Is(err, embeddingdomain.ErrEmbeddingRequestRejected) ||
		errors.Is(err, embeddingdomain.ErrInvalidEmbeddingResponse) ||
		errors.Is(err, generationdomain.ErrGenerationAuthentication) ||
		errors.Is(err, generationdomain.ErrGenerationQuotaExceeded) ||
		errors.Is(err, generationdomain.ErrGenerationRequestRejected) ||
		errors.Is(err, generationdomain.ErrInvalidGenerationResponse)
}

func classifyAnswerJobError(err error) JobErrorCategory {
	switch {
	case err == nil:
		return ""
	case errors.Is(err, context.DeadlineExceeded):
		return JobErrorCategoryTimeout
	case errors.Is(err, context.Canceled):
		return JobErrorCategoryCanceled
	case errors.Is(err, ErrAnswerCapacityExhausted),
		errors.Is(err, ErrAnswerOwnerCapacityExhausted):
		return JobErrorCategoryAnswerCapacity
	case errors.Is(err, embeddingapplication.ErrEmbeddingProviderCapacityExhausted):
		return JobErrorCategoryEmbeddingCapacity
	case errors.Is(err, embeddingapplication.ErrDocumentEmbeddingsNotReady):
		return JobErrorCategoryEmbeddingsNotReady
	case errors.Is(err, documentdomain.ErrNotFound):
		return JobErrorCategoryDocumentNotFound
	case errors.Is(err, embeddingapplication.ErrSemanticSearchQueryRequired),
		errors.Is(err, embeddingapplication.ErrSemanticSearchQueryInvalidUTF8),
		errors.Is(err, embeddingapplication.ErrSemanticSearchQueryTooLong),
		errors.Is(err, embeddingapplication.ErrInvalidSemanticSearchTopK),
		errors.Is(err, embeddingapplication.ErrInvalidDocumentID),
		errors.Is(err, ErrInvalidResponseLanguage):
		return JobErrorCategoryInvalidInput
	case errors.Is(err, embeddingdomain.ErrEmbeddingAuthentication):
		return JobErrorCategoryEmbeddingAuthentication
	case errors.Is(err, embeddingdomain.ErrEmbeddingQuotaExceeded):
		return JobErrorCategoryEmbeddingQuota
	case errors.Is(err, embeddingdomain.ErrEmbeddingRateLimited):
		return JobErrorCategoryEmbeddingRateLimit
	case errors.Is(err, embeddingdomain.ErrEmbeddingRequestRejected):
		return JobErrorCategoryEmbeddingRequest
	case errors.Is(err, embeddingdomain.ErrInvalidEmbeddingResponse):
		return JobErrorCategoryEmbeddingResponse
	case errors.Is(err, embeddingdomain.ErrEmbeddingUnavailable):
		return JobErrorCategoryEmbeddingUnavailable
	case errors.Is(err, generationdomain.ErrGenerationAuthentication):
		return JobErrorCategoryGenerationAuthentication
	case errors.Is(err, generationdomain.ErrGenerationQuotaExceeded):
		return JobErrorCategoryGenerationQuota
	case errors.Is(err, generationdomain.ErrGenerationRateLimited):
		return JobErrorCategoryGenerationRateLimit
	case errors.Is(err, generationdomain.ErrGenerationRequestRejected):
		return JobErrorCategoryGenerationRequest
	case errors.Is(err, generationdomain.ErrInvalidGenerationResponse):
		return JobErrorCategoryGenerationResponse
	case errors.Is(err, generationdomain.ErrGenerationUnavailable):
		return JobErrorCategoryGenerationUnavailable
	default:
		return JobErrorCategoryInternal
	}
}

// InterruptedJobRecoveryService 编排启动时恢复遗留 processing 任务。
type InterruptedJobRecoveryService struct {
	jobs InterruptedJobRecoverer
	now  func() time.Time
}

// NewInterruptedJobRecoveryService 创建启动恢复服务。
func NewInterruptedJobRecoveryService(
	jobs InterruptedJobRecoverer,
) (*InterruptedJobRecoveryService, error) {
	if jobs == nil {
		return nil, ErrAnswerJobWorkerDependencies
	}
	return &InterruptedJobRecoveryService{jobs: jobs, now: time.Now}, nil
}

// Recover 把上次异常退出遗留的 processing 任务放回 queued。
func (s *InterruptedJobRecoveryService) Recover(
	ctx context.Context,
) (int64, error) {
	count, err := s.jobs.RequeueInterruptedAnswerJobs(
		ctx,
		s.now(),
		JobErrorCodeWorkerInterrupted,
		safeAnswerJobRecoveryMessage,
	)
	if err != nil {
		return 0, fmt.Errorf("recover interrupted answer jobs: %w", err)
	}
	return count, nil
}
