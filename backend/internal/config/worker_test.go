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
}

func TestLoadWorkerUsesEnvironmentValues(t *testing.T) {
	t.Setenv("WORKER_POLL_INTERVAL", "500ms")
	t.Setenv("WORKER_PROCESSING_TIMEOUT", "30s")
	t.Setenv("DOCUMENT_WORKER_CONCURRENCY", "2")
	t.Setenv("PROCESSING_MAX_ACTIVE_JOBS_PER_USER", "7")
	t.Setenv("PROCESSING_MAX_ACTIVE_JOBS_GLOBAL", "80")

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
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			// 先建立一个全部合法的测试环境。
			t.Setenv("WORKER_POLL_INTERVAL", "2s")
			t.Setenv("WORKER_PROCESSING_TIMEOUT", "5m")
			t.Setenv("DOCUMENT_WORKER_CONCURRENCY", "1")
			t.Setenv("PROCESSING_MAX_ACTIVE_JOBS_PER_USER", "5")
			t.Setenv("PROCESSING_MAX_ACTIVE_JOBS_GLOBAL", "40")

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

func TestLoadWorkerRejectsGlobalLimitBelowPerUserLimit(t *testing.T) {
	t.Setenv("WORKER_POLL_INTERVAL", "2s")
	t.Setenv("WORKER_PROCESSING_TIMEOUT", "5m")
	t.Setenv("DOCUMENT_WORKER_CONCURRENCY", "1")
	t.Setenv("PROCESSING_MAX_ACTIVE_JOBS_PER_USER", "6")
	t.Setenv("PROCESSING_MAX_ACTIVE_JOBS_GLOBAL", "5")

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
