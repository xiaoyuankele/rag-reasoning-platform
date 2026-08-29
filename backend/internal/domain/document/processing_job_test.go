package document

import (
	"testing"
	"time"
)

func TestProcessingJobAdmissionLimitsIsValid(t *testing.T) {
	testCases := []struct {
		name   string
		limits ProcessingJobAdmissionLimits
		want   bool
	}{
		{
			name: "valid",
			limits: ProcessingJobAdmissionLimits{
				MaxActiveJobsPerOwner: 5,
				MaxActiveJobsGlobal:   40,
			},
			want: true,
		},
		{
			name: "equal limits",
			limits: ProcessingJobAdmissionLimits{
				MaxActiveJobsPerOwner: 5,
				MaxActiveJobsGlobal:   5,
			},
			want: true,
		},
		{
			name: "missing owner limit",
			limits: ProcessingJobAdmissionLimits{
				MaxActiveJobsGlobal: 40,
			},
			want: false,
		},
		{
			name: "global below owner",
			limits: ProcessingJobAdmissionLimits{
				MaxActiveJobsPerOwner: 6,
				MaxActiveJobsGlobal:   5,
			},
			want: false,
		},
	}

	for _, testCase := range testCases {
		t.Run(testCase.name, func(t *testing.T) {
			if got := testCase.limits.IsValid(); got != testCase.want {
				t.Fatalf("IsValid() = %t, want %t", got, testCase.want)
			}
		})
	}
}

func TestProcessingJobSchedulingPolicyIsValid(t *testing.T) {
	testCases := []struct {
		name   string
		policy ProcessingJobSchedulingPolicy
		want   bool
	}{
		{
			name: "fair base with borrowed capacity",
			policy: ProcessingJobSchedulingPolicy{
				MaxInFlightPerOwner:         1,
				MaxBorrowedInFlightPerOwner: 2,
				StarvationThreshold:         2 * time.Minute,
			},
			want: true,
		},
		{
			name: "borrowing disabled",
			policy: ProcessingJobSchedulingPolicy{
				MaxInFlightPerOwner:         1,
				MaxBorrowedInFlightPerOwner: 1,
				StarvationThreshold:         time.Minute,
			},
			want: true,
		},
		{
			name: "missing base limit",
			policy: ProcessingJobSchedulingPolicy{
				MaxBorrowedInFlightPerOwner: 2,
				StarvationThreshold:         time.Minute,
			},
			want: false,
		},
		{
			name: "borrowed below base",
			policy: ProcessingJobSchedulingPolicy{
				MaxInFlightPerOwner:         2,
				MaxBorrowedInFlightPerOwner: 1,
				StarvationThreshold:         time.Minute,
			},
			want: false,
		},
		{
			name: "missing starvation threshold",
			policy: ProcessingJobSchedulingPolicy{
				MaxInFlightPerOwner:         1,
				MaxBorrowedInFlightPerOwner: 2,
			},
			want: false,
		},
	}

	for _, testCase := range testCases {
		t.Run(testCase.name, func(t *testing.T) {
			if got := testCase.policy.IsValid(); got != testCase.want {
				t.Fatalf("IsValid() = %t, want %t", got, testCase.want)
			}
		})
	}
}

func TestProcessingJobLeasePolicyIsValid(t *testing.T) {
	testCases := []struct {
		name   string
		policy ProcessingJobLeasePolicy
		want   bool
	}{
		{
			name: "valid",
			policy: ProcessingJobLeasePolicy{
				WorkerID:      "document-worker-a",
				LeaseDuration: time.Minute,
			},
			want: true,
		},
		{
			name: "blank worker ID",
			policy: ProcessingJobLeasePolicy{
				WorkerID:      "  ",
				LeaseDuration: time.Minute,
			},
			want: false,
		},
		{
			name: "missing lease duration",
			policy: ProcessingJobLeasePolicy{
				WorkerID: "document-worker-a",
			},
			want: false,
		},
	}

	for _, testCase := range testCases {
		t.Run(testCase.name, func(t *testing.T) {
			if got := testCase.policy.IsValid(); got != testCase.want {
				t.Fatalf("IsValid() = %t, want %t", got, testCase.want)
			}
		})
	}
}

func TestProcessingJobStatusIsValid(t *testing.T) {
	testCases := []struct {
		name   string
		status ProcessingJobStatus
		valid  bool
	}{
		{
			name:   "queued",
			status: ProcessingJobStatusQueued,
			valid:  true,
		},
		{
			name:   "processing",
			status: ProcessingJobStatusProcessing,
			valid:  true,
		},
		{
			name:   "succeeded",
			status: ProcessingJobStatusSucceeded,
			valid:  true,
		},
		{
			name:   "failed",
			status: ProcessingJobStatusFailed,
			valid:  true,
		},
		{
			name:   "canceled",
			status: ProcessingJobStatusCanceled,
			valid:  true,
		},
		{
			name:   "unknown",
			status: ProcessingJobStatus("unknown"),
			valid:  false,
		},
	}

	for _, testCase := range testCases {
		t.Run(testCase.name, func(t *testing.T) {
			if actual := testCase.status.IsValid(); actual != testCase.valid {
				t.Fatalf(
					"IsValid() = %t, want %t",
					actual,
					testCase.valid,
				)
			}
		})
	}
}
