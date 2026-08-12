package embedding

import (
	"errors"
	"testing"
	"time"
)

func TestRetryPolicyUsesExponentialDelayAndMaximumCap(t *testing.T) {
	policy, err := newRetryPolicy(
		5,
		2*time.Second,
		10*time.Second,
		func(delayLimit time.Duration) time.Duration {
			// 测试固定返回上限，这样可以直接观察指数增长，不受随机数影响。
			return delayLimit
		},
	)
	if err != nil {
		t.Fatalf("newRetryPolicy() error = %v, want nil", err)
	}

	now := time.Date(2026, time.August, 11, 9, 0, 0, 0, time.UTC)
	tests := []struct {
		name         string
		attemptCount int
		wantDelay    time.Duration
		wantRetry    bool
	}{
		{name: "first failure", attemptCount: 1, wantDelay: 2 * time.Second, wantRetry: true},
		{name: "second failure", attemptCount: 2, wantDelay: 4 * time.Second, wantRetry: true},
		{name: "third failure", attemptCount: 3, wantDelay: 8 * time.Second, wantRetry: true},
		{name: "delay reaches cap", attemptCount: 4, wantDelay: 10 * time.Second, wantRetry: true},
		{name: "attempt limit reached", attemptCount: 5, wantRetry: false},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			nextAttemptAt, retry := policy.NextAttempt(test.attemptCount, now)
			if retry != test.wantRetry {
				t.Fatalf("NextAttempt() retry = %v, want %v", retry, test.wantRetry)
			}
			if !test.wantRetry {
				if !nextAttemptAt.IsZero() {
					t.Fatalf("NextAttempt() time = %v, want zero value", nextAttemptAt)
				}
				return
			}

			wantTime := now.Add(test.wantDelay)
			if !nextAttemptAt.Equal(wantTime) {
				t.Fatalf("NextAttempt() time = %v, want %v", nextAttemptAt, wantTime)
			}
		})
	}
}

func TestNewRetryPolicyRejectsInvalidConfiguration(t *testing.T) {
	tests := []struct {
		name        string
		maxAttempts int
		baseDelay   time.Duration
		maxDelay    time.Duration
		wantErr     error
	}{
		{
			name:        "zero attempts",
			maxAttempts: 0,
			baseDelay:   time.Second,
			maxDelay:    time.Minute,
			wantErr:     ErrInvalidMaxEmbeddingAttempts,
		},
		{
			name:        "base delay greater than maximum",
			maxAttempts: 3,
			baseDelay:   2 * time.Minute,
			maxDelay:    time.Minute,
			wantErr:     ErrInvalidEmbeddingRetryDelay,
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			_, err := NewRetryPolicy(test.maxAttempts, test.baseDelay, test.maxDelay)
			if !errors.Is(err, test.wantErr) {
				t.Fatalf("NewRetryPolicy() error = %v, want %v", err, test.wantErr)
			}
		})
	}
}
