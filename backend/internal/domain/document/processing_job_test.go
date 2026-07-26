package document

import "testing"

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
