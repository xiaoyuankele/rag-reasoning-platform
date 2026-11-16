package config

import (
	"strings"
	"testing"
)

func TestLoadAgentDefaultsBothFeaturesOff(t *testing.T) {
	clearAgentEnvironment(t)

	config, err := LoadAgent()
	if err != nil {
		t.Fatalf("LoadAgent() error = %v", err)
	}
	if config.Enabled || config.WorkerEnabled {
		t.Fatalf("default Agent config = %+v", config)
	}
}

func TestLoadAgentFlagsAreIndependent(t *testing.T) {
	for _, test := range []struct {
		name          string
		enabled       string
		workerEnabled string
		want          AgentConfig
	}{
		{name: "both disabled", enabled: "false", workerEnabled: "false", want: AgentConfig{}},
		{name: "API only", enabled: " true ", workerEnabled: "false", want: AgentConfig{Enabled: true}},
		{name: "Worker only", enabled: "false", workerEnabled: " TRUE ", want: AgentConfig{WorkerEnabled: true}},
		{name: "both enabled", enabled: "true", workerEnabled: "true", want: AgentConfig{Enabled: true, WorkerEnabled: true}},
	} {
		t.Run(test.name, func(t *testing.T) {
			clearAgentEnvironment(t)
			t.Setenv("AGENT_ENABLED", test.enabled)
			t.Setenv("AGENT_WORKER_ENABLED", test.workerEnabled)
			t.Setenv("OPENAI_API_KEY", "")
			t.Setenv("DASHSCOPE_API_KEY", "")

			got, err := LoadAgent()
			if err != nil {
				t.Fatalf("LoadAgent() error = %v", err)
			}
			if got != test.want {
				t.Fatalf("LoadAgent() = %+v, want %+v", got, test.want)
			}
		})
	}
}

func TestLoadAgentRejectsInvalidBooleanFailClosed(t *testing.T) {
	for _, environment := range []string{"AGENT_ENABLED", "AGENT_WORKER_ENABLED"} {
		t.Run(environment, func(t *testing.T) {
			clearAgentEnvironment(t)
			t.Setenv("AGENT_ENABLED", "true")
			t.Setenv("AGENT_WORKER_ENABLED", "true")
			t.Setenv(environment, "enabled")

			got, err := LoadAgent()
			if err == nil || !strings.Contains(err.Error(), environment) {
				t.Fatalf("LoadAgent() = %+v, %v", got, err)
			}
			if got != (AgentConfig{}) {
				t.Fatalf("invalid config did not fail closed: %+v", got)
			}
		})
	}
}

func clearAgentEnvironment(t *testing.T) {
	t.Helper()
	t.Setenv("AGENT_ENABLED", "")
	t.Setenv("AGENT_WORKER_ENABLED", "")
}
