package config

import (
	"testing"
	"time"
)

func TestLoadWorkerUsesDefaultPollInterval(t *testing.T) {
	t.Setenv("WORKER_POLL_INTERVAL", "")

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
}

func TestLoadWorkerUsesEnvironmentPollInterval(t *testing.T) {
	t.Setenv("WORKER_POLL_INTERVAL", "500ms")

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
}

func TestLoadWorkerRejectsInvalidPollInterval(t *testing.T) {
	tests := []struct {
		name  string
		value string
	}{
		{name: "invalid duration", value: "soon"},
		{name: "zero duration", value: "0s"},
		{name: "negative duration", value: "-1s"},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			t.Setenv("WORKER_POLL_INTERVAL", test.value)

			workerConfig, err := LoadWorker()
			if err == nil {
				t.Fatalf("LoadWorker() error = nil for %q", test.value)
			}
			if workerConfig != (WorkerConfig{}) {
				t.Fatalf(
					"LoadWorker() config = %+v, want zero value",
					workerConfig,
				)
			}
		})
	}
}
