package embedding

import "testing"

func TestJobAdmissionLimitsIsValid(t *testing.T) {
	testCases := []struct {
		name   string
		limits JobAdmissionLimits
		want   bool
	}{
		{name: "valid", limits: JobAdmissionLimits{MaxActiveJobsPerOwner: 10, MaxActiveJobsGlobal: 100}, want: true},
		{name: "equal limits", limits: JobAdmissionLimits{MaxActiveJobsPerOwner: 10, MaxActiveJobsGlobal: 10}, want: true},
		{name: "missing owner limit", limits: JobAdmissionLimits{MaxActiveJobsGlobal: 10}, want: false},
		{name: "global below owner", limits: JobAdmissionLimits{MaxActiveJobsPerOwner: 11, MaxActiveJobsGlobal: 10}, want: false},
	}

	for _, testCase := range testCases {
		t.Run(testCase.name, func(t *testing.T) {
			if actual := testCase.limits.IsValid(); actual != testCase.want {
				t.Fatalf("IsValid() = %v, want %v", actual, testCase.want)
			}
		})
	}
}

func TestJobStatusIsValid(t *testing.T) {
	testCases := []struct {
		name   string
		status JobStatus
		valid  bool
	}{
		{name: "waiting document", status: JobStatusWaitingDocument, valid: true},
		{name: "queued", status: JobStatusQueued, valid: true},
		{name: "processing", status: JobStatusProcessing, valid: true},
		{name: "succeeded", status: JobStatusSucceeded, valid: true},
		{name: "failed", status: JobStatusFailed, valid: true},
		{name: "canceled", status: JobStatusCanceled, valid: true},
		{name: "empty", status: JobStatus(""), valid: false},
		{name: "unknown", status: JobStatus("unknown"), valid: false},
	}

	for _, testCase := range testCases {
		t.Run(testCase.name, func(t *testing.T) {
			if actual := testCase.status.IsValid(); actual != testCase.valid {
				t.Fatalf(
					"IsValid() = %t, want %t for status %q",
					actual,
					testCase.valid,
					testCase.status,
				)
			}
		})
	}
}
