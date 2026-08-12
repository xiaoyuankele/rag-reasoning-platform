package embedding

import "testing"

func TestJobStatusIsValid(t *testing.T) {
	testCases := []struct {
		name   string
		status JobStatus
		valid  bool
	}{
		{name: "queued", status: JobStatusQueued, valid: true},
		{name: "processing", status: JobStatusProcessing, valid: true},
		{name: "succeeded", status: JobStatusSucceeded, valid: true},
		{name: "failed", status: JobStatusFailed, valid: true},
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
