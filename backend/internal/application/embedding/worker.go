package embedding

import (
	"context"
	"errors"
	"fmt"
	"time"

	documentdomain "rag-reasoning-platform/backend/internal/domain/document"
	embeddingdomain "rag-reasoning-platform/backend/internal/domain/embedding"
)

const (
	safeEmbeddingFailureMessage = "embedding generation failed"
	safeEmbeddingRetryMessage   = "embedding service temporarily unavailable"
)

var (
	ErrEmbeddingWorkerDependencies = errors.New(
		"embedding worker dependencies must be provided",
	)
	ErrInvalidEmbeddingBatchSize = errors.New(
		"embedding batch size must be positive",
	)
	ErrInvalidEmbeddingTimeout = errors.New(
		"embedding processing timeout must be positive",
	)
	ErrDocumentHasNoChunks = errors.New(
		"document has no text chunks to embed",
	)
)

// embeddingJobWorkerRepository 组合 Worker 领取和收尾任务所需的最小持久化能力。
//
// Application 只依赖这个接口；PostgreSQL 实现会在下一阶段接入。
type embeddingJobWorkerRepository interface {
	embeddingdomain.JobClaimer
	embeddingdomain.JobFinalizer
}

// Worker 编排一条 embedding_jobs 任务的完整执行过程。
//
// 它不发送 HTTP，也不直接写 SQL，而是分别通过 Embedder、ChunkLister 和任务仓储
// 接口调用基础设施层。这使真实 OpenAI、其他模型和 fake 都可以被组装进同一流程。
type Worker struct {
	jobs              embeddingJobWorkerRepository
	chunks            documentdomain.ChunkLister
	embedder          embeddingdomain.Embedder
	events            JobEventObserver
	batchSize         int
	processingTimeout time.Duration
	retryPolicy       RetryPolicy
	now               func() time.Time
}

// NewWorker 创建生产环境使用的 Embedding Worker。
func NewWorker(
	jobs embeddingJobWorkerRepository,
	chunks documentdomain.ChunkLister,
	embedder embeddingdomain.Embedder,
	events JobEventObserver,
	batchSize int,
	processingTimeout time.Duration,
	retryPolicy RetryPolicy,
) (*Worker, error) {
	return newWorker(
		jobs,
		chunks,
		embedder,
		events,
		batchSize,
		processingTimeout,
		retryPolicy,
		time.Now,
	)
}

// newWorker 允许单元测试注入固定时钟，从而精确核对 nextAttemptAt。
func newWorker(
	jobs embeddingJobWorkerRepository,
	chunks documentdomain.ChunkLister,
	embedder embeddingdomain.Embedder,
	events JobEventObserver,
	batchSize int,
	processingTimeout time.Duration,
	retryPolicy RetryPolicy,
	now func() time.Time,
) (*Worker, error) {
	if jobs == nil || chunks == nil || embedder == nil || events == nil || now == nil {
		return nil, ErrEmbeddingWorkerDependencies
	}
	if batchSize <= 0 {
		return nil, ErrInvalidEmbeddingBatchSize
	}
	if processingTimeout <= 0 {
		return nil, ErrInvalidEmbeddingTimeout
	}
	if retryPolicy.maxAttempts <= 0 {
		return nil, ErrInvalidMaxEmbeddingAttempts
	}

	return &Worker{
		jobs:              jobs,
		chunks:            chunks,
		embedder:          embedder,
		events:            events,
		batchSize:         batchSize,
		processingTimeout: processingTimeout,
		retryPolicy:       retryPolicy,
		now:               now,
	}, nil
}

// RunOnce 尝试领取并执行一条向量任务。
//
// handled=false、err=nil 表示队列为空；handled=true 表示已经领取过任务，
// 即使后续把它安排为重试或标记为失败，本轮也仍然处理过一条任务。
func (w *Worker) RunOnce(ctx context.Context) (handled bool, err error) {
	job, err := w.jobs.ClaimNextEmbeddingJob(ctx)
	if errors.Is(err, embeddingdomain.ErrNoQueuedJob) {
		return false, nil
	}
	if err != nil {
		return false, fmt.Errorf("claim next embedding job: %w", err)
	}

	startedAt := time.Now()
	finalEventType := JobEventUnfinished
	finalStatus := embeddingdomain.JobStatusProcessing
	var completion embeddingdomain.JobCompletion
	var metrics embeddingProcessingMetrics
	var finalizationDuration *time.Duration
	var nextAttemptAt *time.Time
	var processingErr error

	w.events.ObserveEmbeddingJobEvent(ctx, JobEvent{
		Type:         JobEventStarted,
		JobID:        job.ID,
		DocumentID:   job.DocumentID,
		ModelName:    job.ModelName,
		Dimensions:   job.Dimensions,
		AttemptCount: job.AttemptCount,
		Status:       finalStatus,
	})
	defer func() {
		retryCount := max(job.AttemptCount-1, 0)
		w.events.ObserveEmbeddingJobEvent(ctx, JobEvent{
			Type:                 finalEventType,
			JobID:                job.ID,
			DocumentID:           job.DocumentID,
			ModelName:            job.ModelName,
			Dimensions:           job.Dimensions,
			AttemptCount:         job.AttemptCount,
			Status:               finalStatus,
			Duration:             time.Since(startedAt),
			ProviderDuration:     metrics.providerDuration,
			FinalizationDuration: finalizationDuration,
			ProviderCallCount:    metrics.providerCallCount,
			PromptTokens:         completion.PromptTokens,
			TotalTokens:          completion.TotalTokens,
			GeneratedVectorCount: len(completion.Vectors),
			RetryCount:           retryCount,
			Recovered:            finalEventType == JobEventSucceeded && retryCount > 0,
			NextAttemptAt:        nextAttemptAt,
			ErrorCategory:        classifyJobError(err),
			Err:                  err,
		})
	}()

	// timeoutContext 覆盖读取 chunks 和所有远程批次，但不覆盖最后的数据库收尾。
	// 收尾使用父 ctx，避免任务刚完成时恰好触发处理超时而无法更新状态。
	timeoutContext, cancel := context.WithTimeout(ctx, w.processingTimeout)
	completion, metrics, processingErr = w.process(timeoutContext, job)
	cancel()

	// 父 ctx 被取消通常表示程序正在 shutdown。此时不要把正常停机伪装成业务失败；
	// 任务暂时保留 processing，后续由启动恢复机制重新排队。
	if ctx.Err() != nil {
		finalEventType = JobEventInterrupted
		return true, fmt.Errorf(
			"embedding job %d interrupted: %w",
			job.ID,
			ctx.Err(),
		)
	}

	if processingErr != nil {
		finalizationStartedAt := time.Now()
		outcome, failureErr := w.finalizeFailure(ctx, job, processingErr)
		elapsed := time.Since(finalizationStartedAt)
		finalizationDuration = &elapsed
		finalEventType = outcome.eventType
		finalStatus = outcome.status
		nextAttemptAt = outcome.nextAttemptAt
		return true, failureErr
	}

	finalizationStartedAt := time.Now()
	finalizationErr := w.jobs.MarkEmbeddingJobSucceeded(ctx, job.ID, completion)
	elapsed := time.Since(finalizationStartedAt)
	finalizationDuration = &elapsed
	if finalizationErr != nil {
		return true, fmt.Errorf(
			"mark embedding job %d succeeded: %w",
			job.ID,
			finalizationErr,
		)
	}

	finalEventType = JobEventSucceeded
	finalStatus = embeddingdomain.JobStatusSucceeded
	return true, nil
}

type embeddingProcessingMetrics struct {
	providerCallCount int
	providerDuration  time.Duration
}

// process 读取文本块、分批生成向量，并把批次结果重新绑定到 chunk ID。
func (w *Worker) process(
	ctx context.Context,
	job embeddingdomain.Job,
) (
	embeddingdomain.JobCompletion,
	embeddingProcessingMetrics,
	error,
) {
	completion := embeddingdomain.JobCompletion{}
	metrics := embeddingProcessingMetrics{}

	chunks, err := w.chunks.ListByDocumentID(ctx, job.DocumentID)
	if err != nil {
		return completion, metrics, fmt.Errorf(
			"list chunks for embedding document %d: %w",
			job.DocumentID,
			err,
		)
	}
	if len(chunks) == 0 {
		return completion, metrics, ErrDocumentHasNoChunks
	}

	completion.Vectors = make([]embeddingdomain.ChunkVector, 0, len(chunks))

	for start := 0; start < len(chunks); start += w.batchSize {
		end := min(start+w.batchSize, len(chunks))
		batch := chunks[start:end]

		inputs := make([]string, len(batch))
		for index, chunk := range batch {
			inputs[index] = chunk.Content
		}

		providerStartedAt := time.Now()
		result, err := w.embedder.Embed(ctx, embeddingdomain.EmbedRequest{
			Inputs:     inputs,
			Model:      job.ModelName,
			Dimensions: job.Dimensions,
		})
		metrics.providerCallCount++
		metrics.providerDuration += time.Since(providerStartedAt)
		if err != nil {
			return completion, metrics, fmt.Errorf(
				"embed document %d chunk batch [%d:%d]: %w",
				job.DocumentID,
				start,
				end,
				err,
			)
		}

		// 使用量在向量结构校验前累计，因为远程提供方已经完成本次计费调用。
		completion.PromptTokens += result.PromptTokens
		completion.TotalTokens += result.TotalTokens

		if len(result.Vectors) != len(batch) {
			return completion, metrics, fmt.Errorf(
				"%w: received %d vectors for %d chunks",
				embeddingdomain.ErrInvalidEmbeddingResponse,
				len(result.Vectors),
				len(batch),
			)
		}

		for index, values := range result.Vectors {
			if len(values) != job.Dimensions {
				return completion, metrics, fmt.Errorf(
					"%w: chunk %d vector has %d dimensions, want %d",
					embeddingdomain.ErrInvalidEmbeddingResponse,
					batch[index].ID,
					len(values),
					job.Dimensions,
				)
			}
			completion.Vectors = append(
				completion.Vectors,
				embeddingdomain.ChunkVector{
					ChunkID: batch[index].ID,
					Values:  values,
				},
			)
		}
	}

	return completion, metrics, nil
}

type embeddingFailureOutcome struct {
	eventType     JobEventType
	status        embeddingdomain.JobStatus
	nextAttemptAt *time.Time
}

// finalizeFailure 根据错误类型决定永久失败还是延迟重试。
func (w *Worker) finalizeFailure(
	ctx context.Context,
	job embeddingdomain.Job,
	processingErr error,
) (embeddingFailureOutcome, error) {
	unfinished := embeddingFailureOutcome{
		eventType: JobEventUnfinished,
		status:    embeddingdomain.JobStatusProcessing,
	}

	if !isPermanentEmbeddingError(processingErr) {
		if nextAttemptAt, retry := w.retryPolicy.NextAttempt(
			job.AttemptCount,
			w.now(),
		); retry {
			if err := w.jobs.RequeueEmbeddingJob(
				ctx,
				job.ID,
				nextAttemptAt,
				safeEmbeddingRetryMessage,
			); err != nil {
				return unfinished, errors.Join(
					processingErr,
					fmt.Errorf("requeue embedding job %d: %w", job.ID, err),
				)
			}

			return embeddingFailureOutcome{
				eventType:     JobEventRequeued,
				status:        embeddingdomain.JobStatusQueued,
				nextAttemptAt: &nextAttemptAt,
			}, processingErr
		}
	}

	if err := w.jobs.MarkEmbeddingJobFailed(
		ctx,
		job.ID,
		safeEmbeddingFailureMessage,
	); err != nil {
		return unfinished, errors.Join(
			processingErr,
			fmt.Errorf("mark embedding job %d failed: %w", job.ID, err),
		)
	}

	return embeddingFailureOutcome{
		eventType: JobEventFailed,
		status:    embeddingdomain.JobStatusFailed,
	}, processingErr
}

// isPermanentEmbeddingError 只识别“原样重试无法解决”的错误。
// 未知错误默认允许有限次数重试，避免一次偶发网络错误直接终止整条任务。
func isPermanentEmbeddingError(err error) bool {
	return errors.Is(err, embeddingdomain.ErrEmbeddingAuthentication) ||
		errors.Is(err, embeddingdomain.ErrEmbeddingQuotaExceeded) ||
		errors.Is(err, embeddingdomain.ErrEmbeddingRequestRejected) ||
		errors.Is(err, embeddingdomain.ErrInvalidEmbeddingResponse) ||
		errors.Is(err, ErrDocumentHasNoChunks)
}
