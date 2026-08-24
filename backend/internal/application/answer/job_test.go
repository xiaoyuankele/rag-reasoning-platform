package answer

import (
	"testing"
	"time"
)

func TestAnswerJobStatusAndPolicies(t *testing.T) {
	for _, status := range []JobStatus{
		JobStatusQueued,
		JobStatusProcessing,
		JobStatusSucceeded,
		JobStatusFailed,
		JobStatusCanceled,
	} {
		if !status.IsValid() {
			t.Fatalf("status %q should be valid", status)
		}
	}
	if JobStatus("unknown").IsValid() {
		t.Fatal("unknown status should be invalid")
	}
	if !(JobAdmissionLimits{MaxQueuedJobsPerOwner: 5, MaxQueuedJobsGlobal: 500}).IsValid() {
		t.Fatal("expected admission limits to be valid")
	}
	if (JobAdmissionLimits{MaxQueuedJobsPerOwner: 6, MaxQueuedJobsGlobal: 5}).IsValid() {
		t.Fatal("owner queue above global queue should be invalid")
	}
	if !(JobSchedulingPolicy{MaxInFlightPerOwner: 1, MaxBorrowedInFlightPerOwner: 2, StarvationThreshold: time.Second}).IsValid() {
		t.Fatal("expected scheduling policy to be valid")
	}
	if (JobSchedulingPolicy{MaxInFlightPerOwner: 2, MaxBorrowedInFlightPerOwner: 1, StarvationThreshold: time.Second}).IsValid() {
		t.Fatal("borrowed limit below base limit should be invalid")
	}
}
