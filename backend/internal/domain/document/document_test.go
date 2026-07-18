package document

import "testing"

// TestStatusIsValid 验证系统支持和拒绝的文档状态。
func TestStatusIsValid(t *testing.T) {
	testCases := []struct {
		name     string
		status   Status
		expected bool
	}{
		{
			name:     "uploaded is valid",
			status:   StatusUploaded,
			expected: true,
		},
		{
			name:     "processing is valid",
			status:   StatusProcessing,
			expected: true,
		},
		{
			name:     "ready is valid",
			status:   StatusReady,
			expected: true,
		},
		{
			name:     "failed is valid",
			status:   StatusFailed,
			expected: true,
		},
		{
			name:     "empty status is invalid",
			status:   Status(""),
			expected: false,
		},
		{
			name:     "unknown status is invalid",
			status:   Status("unknown"),
			expected: false,
		},
	}

	for _, testCase := range testCases {
		t.Run(testCase.name, func(t *testing.T) {
			// 在 if 前置语句中计算实际结果。
			// actual 的作用域仅限于当前 if/else。
			if actual := testCase.status.IsValid(); actual != testCase.expected {
				t.Fatalf(
					"expected IsValid to return %t for status %q, got %t",
					testCase.expected,
					testCase.status,
					actual,
				)
			}
		})
	}
}

// TestStatusCanTransitionTo 验证允许和禁止的文档状态流转。
func TestStatusCanTransitionTo(t *testing.T) {
	testCases := []struct {
		name    string
		from    Status
		to      Status
		allowed bool
	}{
		{
			name:    "uploaded can start processing",
			from:    StatusUploaded,
			to:      StatusProcessing,
			allowed: true,
		},
		{
			name:    "processing can become ready",
			from:    StatusProcessing,
			to:      StatusReady,
			allowed: true,
		},
		{
			name:    "processing can fail",
			from:    StatusProcessing,
			to:      StatusFailed,
			allowed: true,
		},
		{
			name:    "failed can retry processing",
			from:    StatusFailed,
			to:      StatusProcessing,
			allowed: true,
		},
		{
			name:    "uploaded cannot skip directly to ready",
			from:    StatusUploaded,
			to:      StatusReady,
			allowed: false,
		},
		{
			name:    "uploaded cannot fail before processing",
			from:    StatusUploaded,
			to:      StatusFailed,
			allowed: false,
		},
		{
			name:    "processing cannot return to uploaded",
			from:    StatusProcessing,
			to:      StatusUploaded,
			allowed: false,
		},
		{
			name:    "ready is a terminal status",
			from:    StatusReady,
			to:      StatusProcessing,
			allowed: false,
		},
		{
			name:    "same status is not a transition",
			from:    StatusProcessing,
			to:      StatusProcessing,
			allowed: false,
		},
		{
			name:    "invalid source is rejected",
			from:    Status("unknown"),
			to:      StatusProcessing,
			allowed: false,
		},
		{
			name:    "invalid target is rejected",
			from:    StatusUploaded,
			to:      Status("unknown"),
			allowed: false,
		},
	}

	for _, testCase := range testCases {
		t.Run(testCase.name, func(t *testing.T) {
			actual := testCase.from.CanTransitionTo(testCase.to)

			if actual != testCase.allowed {
				t.Fatalf(
					"expected transition %q -> %q allowed=%t, got %t",
					testCase.from,
					testCase.to,
					testCase.allowed,
					actual,
				)
			}
		})
	}
}
