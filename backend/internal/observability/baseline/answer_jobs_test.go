package baseline

import (
	"strings"
	"testing"
	"time"
)

func TestSummarizeAggregatesAnswerJobAttemptsAndQueuePressure(t *testing.T) {
	input := strings.Join([]string{
		`{"event":"answer_job_succeeded","answer_job_id":1,"status":"succeeded","attempt_count":2,"retry_count":1,"recovered":true,"queue_wait_ms":100,"execution_duration_ms":800,"total_ms":5000,"queued_count":8,"ready_queued_count":6,"processing_count":10,"max_owner_processing_count":2,"oldest_ready_wait_ms":4000}`,
		`{"event":"answer_job_requeued","answer_job_id":2,"status":"queued","attempt_count":1,"retry_count":0,"recovered":false,"queue_wait_ms":200,"execution_duration_ms":600,"total_ms":1000,"error_category":"generation_unavailable"}`,
		`{"event":"answer_job_failed","answer_job_id":3,"status":"failed","attempt_count":3,"retry_count":2,"recovered":false,"queue_wait_ms":300,"execution_duration_ms":1000,"total_ms":6000,"queued_count":12,"ready_queued_count":7,"processing_count":9,"max_owner_processing_count":2,"oldest_ready_wait_ms":9000,"error_category":"generation_quota"}`,
	}, "\n")

	report, err := Summarize(strings.NewReader(input), time.Time{})
	if err != nil {
		t.Fatalf("Summarize() error = %v, want nil", err)
	}
	summary := report.AnswerJobs
	if summary.Events["answer_job_succeeded"] != 1 ||
		summary.Events["answer_job_requeued"] != 1 ||
		summary.Events["answer_job_failed"] != 1 ||
		summary.Statuses["succeeded"] != 1 ||
		summary.Statuses["queued"] != 1 ||
		summary.Statuses["failed"] != 1 ||
		summary.ErrorCategories["generation_unavailable"] != 1 ||
		summary.ErrorCategories["generation_quota"] != 1 ||
		summary.RetriedJobCount != 2 ||
		summary.RecoveredJobCount != 1 ||
		summary.RetryExhaustedCount != 1 ||
		summary.RecoveryRate != 0.5 ||
		summary.QueueSnapshotCount != 2 ||
		summary.QueueSnapshotMissingCount != 1 ||
		summary.MaxObservedQueued != 12 ||
		summary.MaxObservedReadyQueued != 7 ||
		summary.MaxObservedProcessing != 10 ||
		summary.MaxObservedOwnerProcessing != 2 ||
		summary.MaxObservedOldestReadyWaitMS != 9000 {
		t.Fatalf("answer job summary = %+v, want attempts, recovery and queue maxima", summary)
	}
	assertDurationSummary(t, summary.QueueWaitDuration, 3, 600, 200, 100, 200, 300, 300)
	assertDurationSummary(t, summary.ExecutionDuration, 3, 2400, 800, 600, 800, 1000, 1000)
	assertDurationSummary(t, summary.TotalDuration, 2, 11000, 5500, 5000, 5000, 6000, 6000)
}

func TestSummarizeRejectsPartialAnswerJobQueueSnapshot(t *testing.T) {
	input := `{"event":"answer_job_failed","answer_job_id":1,"status":"failed","attempt_count":1,"retry_count":0,"recovered":false,"queue_wait_ms":10,"execution_duration_ms":20,"total_ms":30,"queued_count":2}`

	_, err := Summarize(strings.NewReader(input), time.Time{})
	if err == nil || !strings.Contains(err.Error(), "queue snapshot fields") {
		t.Fatalf("Summarize() error = %v, want partial queue snapshot error", err)
	}
}
