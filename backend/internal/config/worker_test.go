package config

import (
	"strings"
	"testing"
	"time"
)

func TestLoadWorkerUsesDefaults(t *testing.T) {
	t.Setenv("WORKER_POLL_INTERVAL", "")
	t.Setenv("WORKER_PROCESSING_TIMEOUT", "")

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
}

func TestLoadWorkerUsesEnvironmentValues(t *testing.T) {
	t.Setenv("WORKER_POLL_INTERVAL", "500ms")
	t.Setenv("WORKER_PROCESSING_TIMEOUT", "30s")

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
}

func TestLoadWorkerRejectsInvalidDurations(t *testing.T) {
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
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			// 先建立一个全部合法的测试环境。
			t.Setenv("WORKER_POLL_INTERVAL", "2s")
			t.Setenv("WORKER_PROCESSING_TIMEOUT", "5m")

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
