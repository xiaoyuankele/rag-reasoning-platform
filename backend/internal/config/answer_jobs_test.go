package config

import (
	"errors"
	"testing"
	"time"
)

func TestLoadAnswerJobsUsesSafeDefaults(t *testing.T) {
	clearAnswerJobsEnvironment(t)

	config, err := LoadAnswerJobs()
	if err != nil {
		t.Fatalf("LoadAnswerJobs() error = %v", err)
	}
	if config.Enabled ||
		config.WorkerConcurrency != 10 ||
		config.PollInterval != time.Second ||
		config.ProcessingTimeout != 90*time.Second ||
		config.MaxQueuedPerUser != 5 ||
		config.MaxQueuedGlobal != 500 ||
		config.OwnerInFlightLimit != 1 ||
		config.OwnerBorrowedInFlightLimit != 2 ||
		config.StarvationThreshold != 30*time.Second ||
		config.MaxAttempts != 3 ||
		config.RetryBaseDelay != 2*time.Second ||
		config.RetryMaxDelay != 30*time.Second ||
		config.Retention != 7*24*time.Hour ||
		config.CleanupInterval != time.Hour ||
		config.CleanupBatchSize != 500 ||
		config.WorkerID == "" ||
		config.JobLeaseDuration != time.Minute ||
		config.JobHeartbeatInterval != 15*time.Second {
		t.Fatalf("default answer jobs config = %+v", config)
	}
}

func TestLoadAnswerJobsUsesEnvironment(t *testing.T) {
	clearAnswerJobsEnvironment(t)
	t.Setenv("ANSWER_JOBS_ENABLED", "true")
	t.Setenv("ANSWER_JOB_WORKER_CONCURRENCY", "8")
	t.Setenv("ANSWER_JOB_POLL_INTERVAL", "250ms")
	t.Setenv("ANSWER_JOB_PROCESSING_TIMEOUT", "2m")
	t.Setenv("ANSWER_JOB_MAX_QUEUED_PER_USER", "10")
	t.Setenv("ANSWER_JOB_MAX_QUEUED_GLOBAL", "1000")
	t.Setenv("ANSWER_JOB_OWNER_IN_FLIGHT_LIMIT", "2")
	t.Setenv("ANSWER_JOB_OWNER_BORROWED_LIMIT", "4")
	t.Setenv("ANSWER_JOB_STARVATION_THRESHOLD", "45s")
	t.Setenv("ANSWER_JOB_MAX_ATTEMPTS", "5")
	t.Setenv("ANSWER_JOB_RETRY_BASE_DELAY", "3s")
	t.Setenv("ANSWER_JOB_RETRY_MAX_DELAY", "1m")
	t.Setenv("ANSWER_JOB_RETENTION", "336h")
	t.Setenv("ANSWER_JOB_CLEANUP_INTERVAL", "30m")
	t.Setenv("ANSWER_JOB_CLEANUP_BATCH_SIZE", "250")
	t.Setenv("ANSWER_JOB_WORKER_ID", "answer-worker-test")
	t.Setenv("ANSWER_JOB_LEASE_DURATION", "2m")
	t.Setenv("ANSWER_JOB_HEARTBEAT_INTERVAL", "20s")

	config, err := LoadAnswerJobs()
	if err != nil {
		t.Fatalf("LoadAnswerJobs() error = %v", err)
	}
	if !config.Enabled ||
		config.WorkerConcurrency != 8 ||
		config.PollInterval != 250*time.Millisecond ||
		config.ProcessingTimeout != 2*time.Minute ||
		config.MaxQueuedPerUser != 10 ||
		config.MaxQueuedGlobal != 1000 ||
		config.OwnerInFlightLimit != 2 ||
		config.OwnerBorrowedInFlightLimit != 4 ||
		config.StarvationThreshold != 45*time.Second ||
		config.MaxAttempts != 5 ||
		config.RetryBaseDelay != 3*time.Second ||
		config.RetryMaxDelay != time.Minute ||
		config.Retention != 14*24*time.Hour ||
		config.CleanupInterval != 30*time.Minute ||
		config.CleanupBatchSize != 250 ||
		config.WorkerID != "answer-worker-test" ||
		config.JobLeaseDuration != 2*time.Minute ||
		config.JobHeartbeatInterval != 20*time.Second {
		t.Fatalf("answer jobs config = %+v", config)
	}
}

func TestLoadAnswerJobsRejectsCrossFieldErrors(t *testing.T) {
	testCases := []struct {
		name        string
		environment map[string]string
		want        error
	}{
		{
			name: "owner queue above global queue",
			environment: map[string]string{
				"ANSWER_JOB_MAX_QUEUED_PER_USER": "6",
				"ANSWER_JOB_MAX_QUEUED_GLOBAL":   "5",
			},
			want: ErrInvalidAnswerJobsCapacity,
		},
		{
			name: "base owner execution above borrowed execution",
			environment: map[string]string{
				"ANSWER_JOB_WORKER_CONCURRENCY":    "4",
				"ANSWER_JOB_OWNER_IN_FLIGHT_LIMIT": "3",
				"ANSWER_JOB_OWNER_BORROWED_LIMIT":  "2",
			},
			want: ErrInvalidAnswerJobsCapacity,
		},
		{
			name: "borrowed execution above workers",
			environment: map[string]string{
				"ANSWER_JOB_WORKER_CONCURRENCY":   "2",
				"ANSWER_JOB_OWNER_BORROWED_LIMIT": "3",
			},
			want: ErrInvalidAnswerJobsCapacity,
		},
		{
			name: "retry base above maximum",
			environment: map[string]string{
				"ANSWER_JOB_RETRY_BASE_DELAY": "31s",
				"ANSWER_JOB_RETRY_MAX_DELAY":  "30s",
			},
			want: ErrInvalidAnswerJobsRetry,
		},
		{
			name: "heartbeat not shorter than lease",
			environment: map[string]string{
				"ANSWER_JOB_LEASE_DURATION":     "15s",
				"ANSWER_JOB_HEARTBEAT_INTERVAL": "15s",
			},
			want: ErrInvalidAnswerJobsLeaseTiming,
		},
	}

	for _, testCase := range testCases {
		t.Run(testCase.name, func(t *testing.T) {
			clearAnswerJobsEnvironment(t)
			for name, value := range testCase.environment {
				t.Setenv(name, value)
			}
			_, err := LoadAnswerJobs()
			if !errors.Is(err, testCase.want) {
				t.Fatalf("LoadAnswerJobs() error = %v, want %v", err, testCase.want)
			}
		})
	}
}

func clearAnswerJobsEnvironment(t *testing.T) {
	t.Helper()
	for _, name := range []string{
		"ANSWER_JOBS_ENABLED",
		"ANSWER_JOB_WORKER_CONCURRENCY",
		"ANSWER_JOB_POLL_INTERVAL",
		"ANSWER_JOB_PROCESSING_TIMEOUT",
		"ANSWER_JOB_MAX_QUEUED_PER_USER",
		"ANSWER_JOB_MAX_QUEUED_GLOBAL",
		"ANSWER_JOB_OWNER_IN_FLIGHT_LIMIT",
		"ANSWER_JOB_OWNER_BORROWED_LIMIT",
		"ANSWER_JOB_STARVATION_THRESHOLD",
		"ANSWER_JOB_MAX_ATTEMPTS",
		"ANSWER_JOB_RETRY_BASE_DELAY",
		"ANSWER_JOB_RETRY_MAX_DELAY",
		"ANSWER_JOB_RETENTION",
		"ANSWER_JOB_CLEANUP_INTERVAL",
		"ANSWER_JOB_CLEANUP_BATCH_SIZE",
		"ANSWER_JOB_WORKER_ID",
		"ANSWER_JOB_LEASE_DURATION",
		"ANSWER_JOB_HEARTBEAT_INTERVAL",
	} {
		t.Setenv(name, "")
	}
}
