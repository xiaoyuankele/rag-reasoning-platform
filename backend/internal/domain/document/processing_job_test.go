package document

import "testing"

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
