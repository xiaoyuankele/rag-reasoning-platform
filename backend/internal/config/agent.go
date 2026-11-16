package config

// AgentConfig contains only the independently controlled Agent API and Worker
// feature flags. Runtime provider, queue and lease settings belong to B2-C.
type AgentConfig struct {
	Enabled       bool
	WorkerEnabled bool
}

// LoadAgent reads the minimal Agent feature flags. Both default off so loading
// configuration cannot enable Agent routes, workers or remote calls implicitly.
func LoadAgent() (AgentConfig, error) {
	enabled, err := loadOptionalBool("AGENT_ENABLED", false)
	if err != nil {
		return AgentConfig{}, err
	}
	workerEnabled, err := loadOptionalBool("AGENT_WORKER_ENABLED", false)
	if err != nil {
		return AgentConfig{}, err
	}
	return AgentConfig{Enabled: enabled, WorkerEnabled: workerEnabled}, nil
}
