package baseline

import (
	"errors"
	"fmt"

	answerapplication "rag-reasoning-platform/backend/internal/application/answer"
)

// AnswerJobSummary 汇总异步问答任务每次执行尝试的等待、执行、恢复和队列压力。
// QueueWaitDuration/ExecutionDuration 统计每次结束的尝试；TotalDuration 只统计
// succeeded/failed 终态，避免同一任务在 requeued 事件中被重复计算端到端耗时。
type AnswerJobSummary struct {
	Events                       map[string]int  `json:"events"`
	Statuses                     map[string]int  `json:"statuses"`
	ErrorCategories              map[string]int  `json:"error_categories"`
	RetriedJobCount              int             `json:"retried_job_count"`
	RecoveredJobCount            int             `json:"recovered_job_count"`
	RetryExhaustedCount          int             `json:"retry_exhausted_count"`
	RecoveryRate                 float64         `json:"recovery_rate"`
	QueueSnapshotCount           int             `json:"queue_snapshot_count"`
	QueueSnapshotMissingCount    int             `json:"queue_snapshot_missing_count"`
	MaxObservedQueued            int64           `json:"max_observed_queued"`
	MaxObservedReadyQueued       int64           `json:"max_observed_ready_queued"`
	MaxObservedProcessing        int64           `json:"max_observed_processing"`
	MaxObservedOwnerProcessing   int64           `json:"max_observed_owner_processing"`
	MaxObservedOldestReadyWaitMS int64           `json:"max_observed_oldest_ready_wait_ms"`
	QueueWaitDuration            DurationSummary `json:"queue_wait_duration"`
	ExecutionDuration            DurationSummary `json:"execution_duration"`
	TotalDuration                DurationSummary `json:"total_duration"`
}

type answerJobAccumulator struct {
	summary            AnswerJobSummary
	queueWaitDurations durationAccumulator
	executionDurations durationAccumulator
	totalDurations     durationAccumulator
}

func newAnswerJobAccumulator() answerJobAccumulator {
	return answerJobAccumulator{summary: AnswerJobSummary{
		Events:          make(map[string]int),
		Statuses:        make(map[string]int),
		ErrorCategories: make(map[string]int),
	}}
}

func (a *answerJobAccumulator) aggregate(entry logEntry) error {
	if entry.AnswerJobID == nil || *entry.AnswerJobID <= 0 {
		return errors.New("answer job event requires positive answer_job_id")
	}
	if entry.AttemptCount == nil || *entry.AttemptCount <= 0 {
		return errors.New("answer job event requires positive attempt_count")
	}
	if entry.RetryCount == nil || *entry.RetryCount < 0 {
		return errors.New("answer job event requires non-negative retry_count")
	}
	if *entry.RetryCount != max(*entry.AttemptCount-1, 0) {
		return errors.New("answer job retry_count is inconsistent with attempt_count")
	}
	if entry.QueueWaitMS == nil || *entry.QueueWaitMS < 0 {
		return errors.New("answer job event requires non-negative queue_wait_ms")
	}
	if entry.ExecutionDurationMS == nil || *entry.ExecutionDurationMS < 0 {
		return errors.New("answer job event requires non-negative execution_duration_ms")
	}
	if entry.TotalMS == nil || *entry.TotalMS < 0 {
		return errors.New("answer job event requires non-negative total_ms")
	}
	if *entry.TotalMS < *entry.ExecutionDurationMS {
		return errors.New("answer job total_ms must cover execution_duration_ms")
	}
	if entry.Recovered == nil {
		return errors.New("answer job event requires recovered")
	}
	expectedRecovered := entry.Event == string(answerapplication.JobEventSucceeded) &&
		*entry.RetryCount > 0
	if *entry.Recovered != expectedRecovered {
		return errors.New("answer job recovered flag is inconsistent with event and retry_count")
	}
	if expectedStatus, ok := expectedAnswerJobStatus(entry.Event); !ok {
		return fmt.Errorf("unsupported answer job event %q", entry.Event)
	} else if entry.Status != expectedStatus {
		return fmt.Errorf(
			"answer job event %q requires status %q",
			entry.Event,
			expectedStatus,
		)
	}
	if err := a.aggregateQueueSnapshot(entry); err != nil {
		return err
	}

	a.summary.Events[entry.Event]++
	a.summary.Statuses[entry.Status]++
	addNonBlankMapValue(a.summary.ErrorCategories, entry.ErrorCategory)
	a.queueWaitDurations.add(*entry.QueueWaitMS)
	a.executionDurations.add(*entry.ExecutionDurationMS)
	if isTerminalAnswerJobEvent(entry.Event) {
		a.totalDurations.add(*entry.TotalMS)
		if *entry.RetryCount > 0 {
			a.summary.RetriedJobCount++
			if entry.Event == string(answerapplication.JobEventSucceeded) {
				a.summary.RecoveredJobCount++
			} else {
				a.summary.RetryExhaustedCount++
			}
		}
	}
	return nil
}

func (a *answerJobAccumulator) aggregateQueueSnapshot(entry logEntry) error {
	present := 0
	if entry.QueuedCount != nil {
		present++
	}
	if entry.ReadyQueuedCount != nil {
		present++
	}
	if entry.ProcessingCount != nil {
		present++
	}
	if entry.MaxOwnerProcessingCount != nil {
		present++
	}
	if entry.OldestReadyWaitMS != nil {
		present++
	}
	if present == 0 {
		a.summary.QueueSnapshotMissingCount++
		return nil
	}
	if present != 5 {
		return errors.New("answer job queue snapshot fields must be provided together")
	}
	if *entry.QueuedCount < 0 || *entry.ReadyQueuedCount < 0 ||
		*entry.ReadyQueuedCount > *entry.QueuedCount ||
		*entry.ProcessingCount < 0 || *entry.MaxOwnerProcessingCount < 0 ||
		*entry.MaxOwnerProcessingCount > *entry.ProcessingCount ||
		*entry.OldestReadyWaitMS < 0 {
		return errors.New("answer job queue snapshot is invalid")
	}

	a.summary.QueueSnapshotCount++
	a.summary.MaxObservedQueued = max(a.summary.MaxObservedQueued, *entry.QueuedCount)
	a.summary.MaxObservedReadyQueued = max(
		a.summary.MaxObservedReadyQueued,
		*entry.ReadyQueuedCount,
	)
	a.summary.MaxObservedProcessing = max(
		a.summary.MaxObservedProcessing,
		*entry.ProcessingCount,
	)
	a.summary.MaxObservedOwnerProcessing = max(
		a.summary.MaxObservedOwnerProcessing,
		*entry.MaxOwnerProcessingCount,
	)
	a.summary.MaxObservedOldestReadyWaitMS = max(
		a.summary.MaxObservedOldestReadyWaitMS,
		*entry.OldestReadyWaitMS,
	)
	return nil
}

func (a answerJobAccumulator) result() AnswerJobSummary {
	summary := a.summary
	summary.QueueWaitDuration = a.queueWaitDurations.summary()
	summary.ExecutionDuration = a.executionDurations.summary()
	summary.TotalDuration = a.totalDurations.summary()
	if summary.RetriedJobCount > 0 {
		summary.RecoveryRate =
			float64(summary.RecoveredJobCount) / float64(summary.RetriedJobCount)
	}
	return summary
}

func expectedAnswerJobStatus(event string) (string, bool) {
	switch event {
	case string(answerapplication.JobEventSucceeded):
		return string(answerapplication.JobStatusSucceeded), true
	case string(answerapplication.JobEventRequeued):
		return string(answerapplication.JobStatusQueued), true
	case string(answerapplication.JobEventFailed):
		return string(answerapplication.JobStatusFailed), true
	case string(answerapplication.JobEventInterrupted),
		string(answerapplication.JobEventUnfinished):
		return string(answerapplication.JobStatusProcessing), true
	default:
		return "", false
	}
}

func isTerminalAnswerJobEvent(event string) bool {
	return event == string(answerapplication.JobEventSucceeded) ||
		event == string(answerapplication.JobEventFailed)
}
