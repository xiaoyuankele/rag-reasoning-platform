package document

import (
	"context"
	"errors"
	"fmt"
	"time"

	documentdomain "rag-reasoning-platform/backend/internal/domain/document"
)

const (
	safeProcessingFailureMessage  = "document processing failed"
	safeProcessingTimeoutMessage  = "document processing timed out"
	safeExpiredProcessingMessage  = "document processing lease expired and was requeued"
	defaultLeaseHeartbeatInterval = 15 * time.Second
)

var ErrInvalidLeaseHeartbeatInterval = errors.New(
	"document worker lease heartbeat interval must be positive",
)

// processingJobWorkerRepository 组合 Worker 领取和收尾任务所需的能力。
type processingJobWorkerRepository interface {
	documentdomain.ProcessingJobClaimer
	documentdomain.ProcessingJobFinalizer
	documentdomain.ProcessingJobLeaseRenewer
	documentdomain.ExpiredProcessingJobRecoverer
}

// ProcessingResult 表示文档处理器产生的统一结果。
//
// 不同输入格式可以有各自的解析方式，但都必须把正文转换成 Chunks，
// 使 Worker 后面的持久化流程不再区分 PDF、Markdown、TXT 或 DOCX。
type ProcessingResult struct {
	DetectedTitle *string
	Chunks        []documentdomain.ChunkInput
	Metrics       *documentdomain.ProcessorStageMetrics
}

// DocumentProcessor 隔离具体的 Go、Python 或其他文档处理实现。
//
// Application 只规定“能够处理一份文档”，不依赖子进程、脚本或 PDF 库。
// 接口需要导出，因为组合根会把多个具体处理器注册到 ProcessorDispatcher。
type DocumentProcessor interface {
	Process(
		ctx context.Context,
		document documentdomain.Document,
	) (ProcessingResult, error)
}

// Worker 编排后台文档解析任务。
type Worker struct {
	jobs              processingJobWorkerRepository
	documents         documentdomain.Finder
	processor         DocumentProcessor
	chunks            documentdomain.LeasedChunkReplacer
	events            ProcessingJobEventObserver
	processingTimeout time.Duration
	heartbeatInterval time.Duration
}

// NewWorker 创建文档解析 Worker。
func NewWorker(
	jobs processingJobWorkerRepository,
	documents documentdomain.Finder,
	processor DocumentProcessor,
	chunks documentdomain.LeasedChunkReplacer,
	events ProcessingJobEventObserver,
	processingTimeout time.Duration,
) *Worker {
	worker, err := NewWorkerWithHeartbeatInterval(
		jobs,
		documents,
		processor,
		chunks,
		events,
		processingTimeout,
		defaultLeaseHeartbeatInterval,
	)
	if err != nil {
		panic(err)
	}
	return worker
}

// NewWorkerWithHeartbeatInterval 创建使用指定租约心跳周期的文档 Worker。
func NewWorkerWithHeartbeatInterval(
	jobs processingJobWorkerRepository,
	documents documentdomain.Finder,
	processor DocumentProcessor,
	chunks documentdomain.LeasedChunkReplacer,
	events ProcessingJobEventObserver,
	processingTimeout time.Duration,
	heartbeatInterval time.Duration,
) (*Worker, error) {
	if events == nil {
		panic("NewWorker requires a non-nil processing job event observer")
	}
	if heartbeatInterval <= 0 {
		return nil, ErrInvalidLeaseHeartbeatInterval
	}

	return &Worker{
		jobs:              jobs,
		documents:         documents,
		processor:         processor,
		chunks:            chunks,
		events:            events,
		processingTimeout: processingTimeout,
		heartbeatInterval: heartbeatInterval,
	}, nil
}

// ClaimNext 尝试领取下一条排队任务。
//
// claimed 为 false 且 err 为 nil，表示队列当前为空，这是 Worker 空闲时
// 的正常结果；claimed 为 true 表示返回的任务已进入 processing。
func (w *Worker) ClaimNext(
	ctx context.Context,
) (
	job documentdomain.ProcessingJob,
	claimed bool,
	err error,
) {
	// 每次领取前先回收真正过期的任务。多实例同时执行时由 PostgreSQL
	// SKIP LOCKED 保证每条任务只被一个恢复事务处理。
	if _, err := w.jobs.RequeueExpiredProcessingJobs(
		ctx,
		safeExpiredProcessingMessage,
	); err != nil {
		return documentdomain.ProcessingJob{}, false, fmt.Errorf(
			"requeue expired processing jobs: %w",
			err,
		)
	}

	foundJob, err := w.jobs.ClaimNextProcessingJob(ctx)
	if errors.Is(err, documentdomain.ErrNoQueuedProcessingJob) {
		return documentdomain.ProcessingJob{}, false, nil
	}
	if err != nil {
		return documentdomain.ProcessingJob{}, false, fmt.Errorf(
			"claim next processing job: %w",
			err,
		)
	}

	return foundJob, true, nil
}

// RunOnce 尝试领取并处理一条任务。
//
// handled 为 false 表示队列为空；handled 为 true 表示本次已经领取任务，
// 无论后续处理成功还是失败。真实处理错误会向上传递，供 Worker 循环记录
// 后端日志；数据库只保存可以安全提供给前端的失败说明。
func (w *Worker) RunOnce(
	ctx context.Context,
) (
	handled bool,
	err error,
) {
	// 第一步：领取一条任务。ClaimNext 已经把“空队列”转换成
	// claimed=false、err=nil。
	job, claimed, err := w.ClaimNext(ctx)
	if err != nil {
		return false, fmt.Errorf(
			"run worker claim: %w",
			err,
		)
	}
	if !claimed {
		return false, nil
	}
	// 从领取成功开始，任务已经进入 processing。观察器先记录 started，
	// 然后 defer 保证每条已领取任务最终都有一条终结事件。
	startedAt := time.Now()
	queueWait := processingJobQueueWait(job, startedAt)
	processorDuration := time.Duration(0)
	var chunkWriteDuration *time.Duration
	var finalizeDuration *time.Duration
	var processorStages *documentdomain.ProcessorStageMetrics
	fileBytes := int64(0)
	chunkCount := 0
	errorCode := documentdomain.ProcessingErrorCodeNone
	finalStatus := documentdomain.ProcessingJobStatusProcessing
	w.events.ObserveProcessingJobEvent(ctx, ProcessingJobEvent{
		Type:         ProcessingJobEventStarted,
		JobID:        job.ID,
		DocumentID:   job.DocumentID,
		AttemptCount: job.AttemptCount,
		Status:       finalStatus,
		QueueWait:    queueWait,
	})
	defer func() {
		eventType := ProcessingJobEventUnfinished
		switch finalStatus {
		case documentdomain.ProcessingJobStatusSucceeded:
			eventType = ProcessingJobEventSucceeded
		case documentdomain.ProcessingJobStatusFailed:
			eventType = ProcessingJobEventFailed
		}

		w.events.ObserveProcessingJobEvent(ctx, ProcessingJobEvent{
			Type:               eventType,
			JobID:              job.ID,
			DocumentID:         job.DocumentID,
			AttemptCount:       job.AttemptCount,
			Status:             finalStatus,
			QueueWait:          queueWait,
			ProcessorDuration:  processorDuration,
			ChunkWriteDuration: chunkWriteDuration,
			FinalizeDuration:   finalizeDuration,
			ProcessorStages:    processorStages,
			TotalDuration:      time.Since(startedAt),
			FileBytes:          fileBytes,
			ChunkCount:         chunkCount,
			ErrorCode:          errorCode,
			Err:                err,
		})
	}()

	// 心跳与真正处理并行运行。续租失败会取消 workContext；旧 Worker 随后
	// 不能再通过 chunks 或终态写入处的 fencing 校验。
	workContext, cancelWork := context.WithCancel(ctx)
	heartbeatContext, stopHeartbeat := context.WithCancel(ctx)
	heartbeatErrors := make(chan error, 1)
	heartbeatDone := make(chan struct{})
	go func() {
		defer close(heartbeatDone)
		ticker := time.NewTicker(w.heartbeatInterval)
		defer ticker.Stop()
		for {
			select {
			case <-heartbeatContext.Done():
				return
			case <-ticker.C:
				if renewErr := w.jobs.RenewProcessingJobLease(
					heartbeatContext,
					job.ID,
					job.LeaseToken,
				); renewErr != nil {
					heartbeatErrors <- renewErr
					cancelWork()
					return
				}
			}
		}
	}()
	defer func() {
		stopHeartbeat()
		cancelWork()
		<-heartbeatDone
	}()
	leaseError := func() error {
		select {
		case heartbeatErr := <-heartbeatErrors:
			return fmt.Errorf("renew processing job lease: %w", heartbeatErr)
		default:
			return nil
		}
	}

	// 从这里开始已经领取过任务，因此后续即使失败，
	// handled 也必须返回 true。
	foundDocument, err := w.documents.GetByID(workContext, job.DocumentID)
	if err != nil {
		if heartbeatErr := leaseError(); heartbeatErr != nil {
			return true, heartbeatErr
		}
		errorCode = documentdomain.ProcessingErrorCodeDocumentLookup
		return true, fmt.Errorf(
			"get claimed processing job document: %w",
			err,
		)
	}
	fileBytes = foundDocument.SizeBytes

	// processContext 只限制文档处理器的执行时间。
	// 父级 ctx 取消时，它也会立刻取消；即使父级仍然有效，
	// 超过 processingTimeout 后也会自动返回 DeadlineExceeded。
	processContext, cancelProcess := context.WithTimeout(
		workContext,
		w.processingTimeout,
	)
	processorStartedAt := time.Now()
	processingResult, processingErr := w.processor.Process(processContext, foundDocument)
	processorDuration = time.Since(processorStartedAt)

	// 处理器已经返回，不再需要定时器，立即释放相关资源。
	cancelProcess()
	if heartbeatErr := leaseError(); heartbeatErr != nil {
		return true, heartbeatErr
	}
	if ctx.Err() != nil {
		// 正常停机不伪装成业务失败；任务会在租约到期后被其他实例重排。
		return true, ctx.Err()
	}

	if processingErr != nil {
		// 真实处理错误返回给 Worker 循环写后端日志。
		wrappedProcessingErr := fmt.Errorf(
			"process document %d: %w",
			foundDocument.ID,
			processingErr,
		)

		failureMessage := safeProcessingFailureMessage
		errorCode = documentdomain.ProcessingErrorCodeProcessor
		if errors.Is(processingErr, context.DeadlineExceeded) {
			failureMessage = safeProcessingTimeoutMessage
			errorCode = documentdomain.ProcessingErrorCodeProcessorTimeout
		}

		// 数据库只保存可安全展示给前端的通用失败说明。
		finalizationStartedAt := time.Now()
		finalizationErr := w.jobs.MarkProcessingJobFailed(
			workContext,
			job.ID,
			job.LeaseToken,
			documentdomain.ProcessingFailure{
				Message: failureMessage,
				Metrics: newProcessingExecutionMetrics(
					processorDuration,
					chunkWriteDuration,
					processorStages,
					fileBytes,
					chunkCount,
					errorCode,
				),
			},
		)
		measuredFinalizationDuration := time.Since(finalizationStartedAt)
		finalizeDuration = &measuredFinalizationDuration
		if finalizationErr != nil {
			wrappedFinalizationErr := fmt.Errorf(
				"mark processing job %d failed: %w",
				job.ID,
				finalizationErr,
			)

			// 处理失败和状态回写失败是两个独立问题，
			// Join 保证后端日志和 errors.Is 都不会丢失任何一个。
			return true, errors.Join(
				wrappedProcessingErr,
				wrappedFinalizationErr,
			)
		}

		finalStatus = documentdomain.ProcessingJobStatusFailed
		return true, wrappedProcessingErr
	}
	processorStages = processingResult.Metrics
	chunkCount = len(processingResult.Chunks)
	// 处理器成功后先保存文本块；只有结果成功入库，任务才能进入 succeeded。
	chunkWriteStartedAt := time.Now()
	replaceErr := w.chunks.ReplaceForProcessingJob(
		workContext,
		job.ID,
		job.LeaseToken,
		foundDocument.ID,
		processingResult.Chunks,
	)
	measuredChunkWriteDuration := time.Since(chunkWriteStartedAt)
	chunkWriteDuration = &measuredChunkWriteDuration
	if replaceErr != nil {
		errorCode = documentdomain.ProcessingErrorCodeChunkWrite
		wrappedReplaceErr := fmt.Errorf(
			"replace document %d chunks: %w",
			foundDocument.ID,
			replaceErr,
		)

		finalizationStartedAt := time.Now()
		markFailedErr := w.jobs.MarkProcessingJobFailed(
			workContext,
			job.ID,
			job.LeaseToken,
			documentdomain.ProcessingFailure{
				Message: safeProcessingFailureMessage,
				Metrics: newProcessingExecutionMetrics(
					processorDuration,
					chunkWriteDuration,
					processorStages,
					fileBytes,
					chunkCount,
					errorCode,
				),
			},
		)
		measuredFinalizationDuration := time.Since(finalizationStartedAt)
		finalizeDuration = &measuredFinalizationDuration
		if markFailedErr != nil {
			wrappedMarkFailedErr := fmt.Errorf(
				"mark processing job %d failed: %w",
				job.ID,
				markFailedErr,
			)
			return true, errors.Join(
				wrappedReplaceErr,
				wrappedMarkFailedErr,
			)
		}

		finalStatus = documentdomain.ProcessingJobStatusFailed
		return true, wrappedReplaceErr
	}

	// 只有处理器真正成功后，才能把任务和文档标记为成功。
	finalizationStartedAt := time.Now()
	if err := w.jobs.MarkProcessingJobSucceeded(
		workContext,
		job.ID,
		job.LeaseToken,
		documentdomain.ProcessingCompletion{
			DetectedTitle: processingResult.DetectedTitle,
			Metrics: newProcessingExecutionMetrics(
				processorDuration,
				chunkWriteDuration,
				processorStages,
				fileBytes,
				chunkCount,
				documentdomain.ProcessingErrorCodeNone,
			),
		},
	); err != nil {
		measuredFinalizationDuration := time.Since(finalizationStartedAt)
		finalizeDuration = &measuredFinalizationDuration
		// 这里不能改写成 failed：文档处理已经成功，
		// 失败的是数据库状态回写，两者的业务事实不同。
		errorCode = documentdomain.ProcessingErrorCodeFinalization
		return true, fmt.Errorf(
			"mark processing job %d succeeded: %w",
			job.ID,
			err,
		)
	}
	measuredFinalizationDuration := time.Since(finalizationStartedAt)
	finalizeDuration = &measuredFinalizationDuration

	finalStatus = documentdomain.ProcessingJobStatusSucceeded
	return true, nil
}

// newProcessingExecutionMetrics 汇总 Worker 自己测量的阶段和处理器返回的
// 可选内部指标，确保成功与失败收尾使用完全相同的字段映射。
func newProcessingExecutionMetrics(
	processorDuration time.Duration,
	chunkWriteDuration *time.Duration,
	processorStages *documentdomain.ProcessorStageMetrics,
	fileBytes int64,
	chunkCount int,
	errorCode documentdomain.ProcessingErrorCode,
) documentdomain.ProcessingExecutionMetrics {
	return documentdomain.ProcessingExecutionMetrics{
		ProcessorDuration:  processorDuration,
		ChunkWriteDuration: chunkWriteDuration,
		ProcessorStages:    processorStages,
		FileBytes:          fileBytes,
		ChunkCount:         chunkCount,
		ErrorCode:          errorCode,
	}
}

// processingJobQueueWait 优先使用数据库领取事务写入的 StartedAt，避免把
// PostgreSQL 查询返回和 Go 调度时间误算进排队耗时。测试替身或旧数据没有
// StartedAt 时，使用 Worker 本地领取完成时间作为保守回退。
func processingJobQueueWait(
	job documentdomain.ProcessingJob,
	claimedAt time.Time,
) time.Duration {
	if job.CreatedAt.IsZero() {
		return 0
	}

	queueEndedAt := claimedAt
	if job.StartedAt != nil {
		queueEndedAt = *job.StartedAt
	}

	wait := queueEndedAt.Sub(job.CreatedAt)
	if wait < 0 {
		return 0
	}

	return wait
}
