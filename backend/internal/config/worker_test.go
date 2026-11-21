package config

import (
	"errors"
	"strings"
	"testing"
	"time"
)

func TestLoadWorkerUsesDefaults(t *testing.T) {
	t.Setenv("WORKER_POLL_INTERVAL", "")
	t.Setenv("WORKER_PROCESSING_TIMEOUT", "")
	t.Setenv("DOCUMENT_WORKER_CONCURRENCY", "")
	t.Setenv("PROCESSING_MAX_ACTIVE_JOBS_PER_USER", "")
	t.Setenv("PROCESSING_MAX_ACTIVE_JOBS_GLOBAL", "")
	t.Setenv("PROCESSING_MAX_IN_FLIGHT_PER_OWNER", "")
	t.Setenv("PROCESSING_MAX_BORROWED_IN_FLIGHT_PER_OWNER", "")
	t.Setenv("PROCESSING_STARVATION_THRESHOLD", "")
	t.Setenv("DOCUMENT_WORKER_ID", "")
	t.Setenv("DOCUMENT_JOB_LEASE_DURATION", "")
	t.Setenv("DOCUMENT_JOB_HEARTBEAT_INTERVAL", "")

	workerConfig, err := LoadWorker()
	if err != nil {
		t.Fatalf("LoadWorker() error = %v, want nil", err)
	}
	if workerConfig.PollInterval != defaultWorkerPollInterval {
		t.Fatalf(
			"PollInterval = %v, want %v",
			workerConfig.PollInterval,
			defaultWorkerPollInterval,
		)
	}
	if workerConfig.ProcessingTimeout != defaultWorkerProcessingTimeout {
		t.Fatalf(
			"ProcessingTimeout = %v, want %v",
			workerConfig.ProcessingTimeout,
			defaultWorkerProcessingTimeout,
		)
	}
	if workerConfig.DocumentConcurrency != defaultDocumentWorkerConcurrency {
		t.Fatalf(
			"DocumentConcurrency = %d, want %d",
			workerConfig.DocumentConcurrency,
			defaultDocumentWorkerConcurrency,
		)
	}
	if workerConfig.ActiveJobsPerUserLimit != defaultProcessingActiveOwnerLimit {
		t.Fatalf(
			"ActiveJobsPerUserLimit = %d, want %d",
			workerConfig.ActiveJobsPerUserLimit,
			defaultProcessingActiveOwnerLimit,
		)
	}
	if workerConfig.ActiveJobsGlobalLimit != defaultProcessingActiveGlobalLimit {
		t.Fatalf(
			"ActiveJobsGlobalLimit = %d, want %d",
			workerConfig.ActiveJobsGlobalLimit,
			defaultProcessingActiveGlobalLimit,
		)
	}
	if workerConfig.OwnerInFlightLimit != defaultProcessingOwnerInFlightLimit {
		t.Fatalf(
			"OwnerInFlightLimit = %d, want %d",
			workerConfig.OwnerInFlightLimit,
			defaultProcessingOwnerInFlightLimit,
		)
	}
	if workerConfig.OwnerBorrowedLimit != defaultProcessingOwnerBorrowedLimit {
		t.Fatalf(
			"OwnerBorrowedLimit = %d, want %d",
			workerConfig.OwnerBorrowedLimit,
			defaultProcessingOwnerBorrowedLimit,
		)
	}
	if workerConfig.StarvationThreshold != defaultProcessingStarvationThreshold {
		t.Fatalf(
			"StarvationThreshold = %v, want %v",
			workerConfig.StarvationThreshold,
			defaultProcessingStarvationThreshold,
		)
	}
	if workerConfig.DocumentWorkerID == "" {
		t.Fatal("DocumentWorkerID must receive a generated default")
	}
	if workerConfig.JobLeaseDuration != defaultDocumentJobLeaseDuration {
		t.Fatalf(
			"JobLeaseDuration = %v, want %v",
			workerConfig.JobLeaseDuration,
			defaultDocumentJobLeaseDuration,
		)
	}
	if workerConfig.JobHeartbeatInterval != defaultDocumentJobHeartbeatInterval {
		t.Fatalf(
			"JobHeartbeatInterval = %v, want %v",
			workerConfig.JobHeartbeatInterval,
			defaultDocumentJobHeartbeatInterval,
		)
	}
}

func TestLoadWorkerUsesEnvironmentValues(t *testing.T) {
	t.Setenv("WORKER_POLL_INTERVAL", "500ms")
	t.Setenv("WORKER_PROCESSING_TIMEOUT", "30s")
	t.Setenv("DOCUMENT_WORKER_CONCURRENCY", "2")
	t.Setenv("PROCESSING_MAX_ACTIVE_JOBS_PER_USER", "7")
	t.Setenv("PROCESSING_MAX_ACTIVE_JOBS_GLOBAL", "80")
	t.Setenv("PROCESSING_MAX_IN_FLIGHT_PER_OWNER", "2")
	t.Setenv("PROCESSING_MAX_BORROWED_IN_FLIGHT_PER_OWNER", "3")
	t.Setenv("PROCESSING_STARVATION_THRESHOLD", "90s")
	t.Setenv("DOCUMENT_WORKER_ID", "worker-a")
	t.Setenv("DOCUMENT_JOB_LEASE_DURATION", "45s")
	t.Setenv("DOCUMENT_JOB_HEARTBEAT_INTERVAL", "10s")

	workerConfig, err := LoadWorker()
	if err != nil {
		t.Fatalf("LoadWorker() error = %v, want nil", err)
	}
	if workerConfig.PollInterval != 500*time.Millisecond {
		t.Fatalf(
			"PollInterval = %v, want %v",
			workerConfig.PollInterval,
			500*time.Millisecond,
		)
	}

	if workerConfig.ProcessingTimeout != 30*time.Second {
		t.Fatalf(
			"ProcessingTimeout = %v, want %v",
			workerConfig.ProcessingTimeout,
			30*time.Second,
		)
	}
	if workerConfig.DocumentConcurrency != 2 {
		t.Fatalf(
			"DocumentConcurrency = %d, want 2",
			workerConfig.DocumentConcurrency,
		)
	}
	if workerConfig.ActiveJobsPerUserLimit != 7 {
		t.Fatalf(
			"ActiveJobsPerUserLimit = %d, want 7",
			workerConfig.ActiveJobsPerUserLimit,
		)
	}
	if workerConfig.ActiveJobsGlobalLimit != 80 {
		t.Fatalf(
			"ActiveJobsGlobalLimit = %d, want 80",
			workerConfig.ActiveJobsGlobalLimit,
		)
	}
	if workerConfig.OwnerInFlightLimit != 2 {
		t.Fatalf(
			"OwnerInFlightLimit = %d, want 2",
			workerConfig.OwnerInFlightLimit,
		)
	}
	if workerConfig.OwnerBorrowedLimit != 3 {
		t.Fatalf(
			"OwnerBorrowedLimit = %d, want 3",
			workerConfig.OwnerBorrowedLimit,
		)
	}
	if workerConfig.StarvationThreshold != 90*time.Second {
		t.Fatalf(
			"StarvationThreshold = %v, want %v",
			workerConfig.StarvationThreshold,
			90*time.Second,
		)
	}
	if workerConfig.DocumentWorkerID != "worker-a" {
		t.Fatalf("DocumentWorkerID = %q, want worker-a", workerConfig.DocumentWorkerID)
	}
	if workerConfig.JobLeaseDuration != 45*time.Second {
		t.Fatalf("JobLeaseDuration = %v, want 45s", workerConfig.JobLeaseDuration)
	}
	if workerConfig.JobHeartbeatInterval != 10*time.Second {
		t.Fatalf("JobHeartbeatInterval = %v, want 10s", workerConfig.JobHeartbeatInterval)
	}
}

func TestLoadWorkerRejectsInvalidValues(t *testing.T) {
	tests := []struct {
		name            string
		environmentName string
		value           string
	}{
		{
			name:            "invalid poll interval",
			environmentName: "WORKER_POLL_INTERVAL",
			value:           "soon",
		},
		{
			name:            "zero poll interval",
			environmentName: "WORKER_POLL_INTERVAL",
			value:           "0s",
		},
		{
			name:            "negative poll interval",
			environmentName: "WORKER_POLL_INTERVAL",
			value:           "-1s",
		},
		{
			name:            "invalid processing timeout",
			environmentName: "WORKER_PROCESSING_TIMEOUT",
			value:           "soon",
		},
		{
			name:            "zero processing timeout",
			environmentName: "WORKER_PROCESSING_TIMEOUT",
			value:           "0s",
		},
		{
			name:            "negative processing timeout",
			environmentName: "WORKER_PROCESSING_TIMEOUT",
			value:           "-1s",
		},
		{
			name:            "non-numeric document concurrency",
			environmentName: "DOCUMENT_WORKER_CONCURRENCY",
			value:           "two",
		},
		{
			name:            "zero document concurrency",
			environmentName: "DOCUMENT_WORKER_CONCURRENCY",
			value:           "0",
		},
		{
			name:            "document concurrency above maximum",
			environmentName: "DOCUMENT_WORKER_CONCURRENCY",
			value:           "5",
		},
		{
			name:            "non-numeric per-user active job limit",
			environmentName: "PROCESSING_MAX_ACTIVE_JOBS_PER_USER",
			value:           "five",
		},
		{
			name:            "zero per-user active job limit",
			environmentName: "PROCESSING_MAX_ACTIVE_JOBS_PER_USER",
			value:           "0",
		},
		{
			name:            "per-user active job limit above maximum",
			environmentName: "PROCESSING_MAX_ACTIVE_JOBS_PER_USER",
			value:           "10001",
		},
		{
			name:            "non-numeric global active job limit",
			environmentName: "PROCESSING_MAX_ACTIVE_JOBS_GLOBAL",
			value:           "forty",
		},
		{
			name:            "zero global active job limit",
			environmentName: "PROCESSING_MAX_ACTIVE_JOBS_GLOBAL",
			value:           "0",
		},
		{
			name:            "global active job limit above maximum",
			environmentName: "PROCESSING_MAX_ACTIVE_JOBS_GLOBAL",
			value:           "10001",
		},
		{
			name:            "non-numeric owner in-flight limit",
			environmentName: "PROCESSING_MAX_IN_FLIGHT_PER_OWNER",
			value:           "one",
		},
		{
			name:            "zero owner in-flight limit",
			environmentName: "PROCESSING_MAX_IN_FLIGHT_PER_OWNER",
			value:           "0",
		},
		{
			name:            "owner in-flight limit above maximum",
			environmentName: "PROCESSING_MAX_IN_FLIGHT_PER_OWNER",
			value:           "65",
		},
		{
			name:            "non-numeric borrowed owner limit",
			environmentName: "PROCESSING_MAX_BORROWED_IN_FLIGHT_PER_OWNER",
			value:           "two",
		},
		{
			name:            "zero borrowed owner limit",
			environmentName: "PROCESSING_MAX_BORROWED_IN_FLIGHT_PER_OWNER",
			value:           "0",
		},
		{
			name:            "borrowed owner limit above maximum",
			environmentName: "PROCESSING_MAX_BORROWED_IN_FLIGHT_PER_OWNER",
			value:           "65",
		},
		{
			name:            "invalid starvation threshold",
			environmentName: "PROCESSING_STARVATION_THRESHOLD",
			value:           "later",
		},
		{
			name:            "zero starvation threshold",
			environmentName: "PROCESSING_STARVATION_THRESHOLD",
			value:           "0s",
		},
		{
			name:            "invalid lease duration",
			environmentName: "DOCUMENT_JOB_LEASE_DURATION",
			value:           "later",
		},
		{
			name:            "zero lease duration",
			environmentName: "DOCUMENT_JOB_LEASE_DURATION",
			value:           "0s",
		},
		{
			name:            "invalid heartbeat interval",
			environmentName: "DOCUMENT_JOB_HEARTBEAT_INTERVAL",
			value:           "often",
		},
		{
			name:            "zero heartbeat interval",
			environmentName: "DOCUMENT_JOB_HEARTBEAT_INTERVAL",
			value:           "0s",
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			// 先建立一个全部合法的测试环境。
			t.Setenv("WORKER_POLL_INTERVAL", "2s")
			t.Setenv("WORKER_PROCESSING_TIMEOUT", "5m")
			t.Setenv("DOCUMENT_WORKER_CONCURRENCY", "1")
			t.Setenv("PROCESSING_MAX_ACTIVE_JOBS_PER_USER", "5")
			t.Setenv("PROCESSING_MAX_ACTIVE_JOBS_GLOBAL", "40")
			t.Setenv("PROCESSING_MAX_IN_FLIGHT_PER_OWNER", "1")
			t.Setenv("PROCESSING_MAX_BORROWED_IN_FLIGHT_PER_OWNER", "2")
			t.Setenv("PROCESSING_STARVATION_THRESHOLD", "2m")

			// 然后只破坏本测试关注的环境变量。
			t.Setenv(test.environmentName, test.value)

			workerConfig, err := LoadWorker()
			if err == nil {
				t.Fatalf(
					"LoadWorker() error = nil for %s=%q",
					test.environmentName,
					test.value,
				)
			}

			if workerConfig != (WorkerConfig{}) {
				t.Fatalf(
					"LoadWorker() config = %+v, want zero value",
					workerConfig,
				)
			}

			if !strings.Contains(err.Error(), test.environmentName) {
				t.Fatalf(
					"LoadWorker() error = %q, want environment name %q",
					err,
					test.environmentName,
				)
			}
		})
	}
}

func TestLoadWorkerRejectsHeartbeatNotShorterThanLease(t *testing.T) {
	t.Setenv("DOCUMENT_JOB_LEASE_DURATION", "30s")
	t.Setenv("DOCUMENT_JOB_HEARTBEAT_INTERVAL", "30s")

	workerConfig, err := LoadWorker()
	if !errors.Is(err, ErrInvalidDocumentJobLeaseTiming) {
		t.Fatalf(
			"LoadWorker() error = %v, want ErrInvalidDocumentJobLeaseTiming",
			err,
		)
	}
	if workerConfig != (WorkerConfig{}) {
		t.Fatalf("LoadWorker() config = %+v, want zero value", workerConfig)
	}
}

func TestLoadWorkerRejectsGlobalLimitBelowPerUserLimit(t *testing.T) {
	t.Setenv("WORKER_POLL_INTERVAL", "2s")
	t.Setenv("WORKER_PROCESSING_TIMEOUT", "5m")
	t.Setenv("DOCUMENT_WORKER_CONCURRENCY", "1")
	t.Setenv("PROCESSING_MAX_ACTIVE_JOBS_PER_USER", "6")
	t.Setenv("PROCESSING_MAX_ACTIVE_JOBS_GLOBAL", "5")
	t.Setenv("PROCESSING_MAX_IN_FLIGHT_PER_OWNER", "1")
	t.Setenv("PROCESSING_MAX_BORROWED_IN_FLIGHT_PER_OWNER", "2")
	t.Setenv("PROCESSING_STARVATION_THRESHOLD", "2m")

	workerConfig, err := LoadWorker()
	if !errors.Is(err, ErrInvalidProcessingActiveJobLimits) {
		t.Fatalf(
			"LoadWorker() error = %v, want ErrInvalidProcessingActiveJobLimits",
			err,
		)
	}
	if workerConfig != (WorkerConfig{}) {
		t.Fatalf("LoadWorker() config = %+v, want zero value", workerConfig)
	}
}

func TestLoadWorkerRejectsBorrowedOwnerLimitBelowBaseLimit(t *testing.T) {
	t.Setenv("WORKER_POLL_INTERVAL", "2s")
	t.Setenv("WORKER_PROCESSING_TIMEOUT", "5m")
	t.Setenv("DOCUMENT_WORKER_CONCURRENCY", "1")
	t.Setenv("PROCESSING_MAX_ACTIVE_JOBS_PER_USER", "5")
	t.Setenv("PROCESSING_MAX_ACTIVE_JOBS_GLOBAL", "40")
	t.Setenv("PROCESSING_MAX_IN_FLIGHT_PER_OWNER", "3")
	t.Setenv("PROCESSING_MAX_BORROWED_IN_FLIGHT_PER_OWNER", "2")
	t.Setenv("PROCESSING_STARVATION_THRESHOLD", "2m")

	workerConfig, err := LoadWorker()
	if !errors.Is(err, ErrInvalidProcessingOwnerSchedulingLimits) {
		t.Fatalf(
			"LoadWorker() error = %v, want ErrInvalidProcessingOwnerSchedulingLimits",
			err,
		)
	}
	if workerConfig != (WorkerConfig{}) {
		t.Fatalf("LoadWorker() config = %+v, want zero value", workerConfig)
	}
}
