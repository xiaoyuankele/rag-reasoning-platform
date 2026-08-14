package config

import (
	"errors"
	"testing"
	"time"
)

func TestLoadGenerationUsesSafeDefaults(t *testing.T) {
	clearGenerationEnvironment(t)

	generationConfig, err := LoadGeneration()
	if err != nil {
		t.Fatalf("LoadGeneration() error = %v, want nil", err)
	}
	if generationConfig.Enabled {
		t.Fatal("Enabled = true, want false by default")
	}
	if generationConfig.APIKey != "" {
		t.Fatal("APIKey must be empty when generation is disabled")
	}
	if generationConfig.Endpoint != defaultDashScopeGenerationEndpoint ||
		generationConfig.ModelName != defaultGenerationModel ||
		generationConfig.HTTPTimeout != defaultGenerationHTTPTimeout ||
		generationConfig.MaxOutputTokens != defaultGenerationMaxOutputTokens ||
		generationConfig.Temperature != defaultGenerationTemperature ||
		generationConfig.ThinkingEnabled {
		t.Fatalf("default generation config = %+v", generationConfig)
	}
}

func TestLoadGenerationUsesEnvironment(t *testing.T) {
	clearGenerationEnvironment(t)
	t.Setenv("ANSWER_ENABLED", "true")
	t.Setenv("DASHSCOPE_API_KEY", " test-key ")
	t.Setenv("DASHSCOPE_GENERATION_ENDPOINT", " https://example.com/v1/chat/completions ")
	t.Setenv("GENERATION_MODEL", " test-model ")
	t.Setenv("GENERATION_HTTP_TIMEOUT", "25s")
	t.Setenv("GENERATION_MAX_OUTPUT_TOKENS", "2048")
	t.Setenv("GENERATION_TEMPERATURE", "0.25")
	t.Setenv("GENERATION_THINKING_ENABLED", "true")

	generationConfig, err := LoadGeneration()
	if err != nil {
		t.Fatalf("LoadGeneration() error = %v, want nil", err)
	}
	if !generationConfig.Enabled ||
		generationConfig.APIKey != "test-key" ||
		generationConfig.Endpoint != "https://example.com/v1/chat/completions" ||
		generationConfig.ModelName != "test-model" ||
		generationConfig.HTTPTimeout != 25*time.Second ||
		generationConfig.MaxOutputTokens != 2048 ||
		generationConfig.Temperature != 0.25 ||
		!generationConfig.ThinkingEnabled {
		t.Fatalf("generation config = %+v, want environment values", generationConfig)
	}
}

func TestLoadGenerationRequiresAPIKeyOnlyWhenEnabled(t *testing.T) {
	clearGenerationEnvironment(t)
	t.Setenv("ANSWER_ENABLED", "true")

	_, err := LoadGeneration()
	if !errors.Is(err, ErrGenerationAPIKeyRequired) {
		t.Fatalf("LoadGeneration() error = %v, want ErrGenerationAPIKeyRequired", err)
	}
}

func TestLoadGenerationRejectsInvalidValues(t *testing.T) {
	testCases := []struct {
		name        string
		environment string
		value       string
		wantedError error
	}{
		{name: "invalid enabled", environment: "ANSWER_ENABLED", value: "sometimes"},
		{name: "invalid timeout", environment: "GENERATION_HTTP_TIMEOUT", value: "zero"},
		{name: "zero output", environment: "GENERATION_MAX_OUTPUT_TOKENS", value: "0"},
		{name: "output above maximum", environment: "GENERATION_MAX_OUTPUT_TOKENS", value: "8193"},
		{name: "invalid temperature", environment: "GENERATION_TEMPERATURE", value: "hot"},
		{name: "invalid thinking enabled", environment: "GENERATION_THINKING_ENABLED", value: "sometimes"},
		{name: "negative temperature", environment: "GENERATION_TEMPERATURE", value: "-0.1", wantedError: ErrInvalidGenerationTemperature},
		{name: "temperature above two", environment: "GENERATION_TEMPERATURE", value: "2.1", wantedError: ErrInvalidGenerationTemperature},
	}

	for _, testCase := range testCases {
		t.Run(testCase.name, func(t *testing.T) {
			clearGenerationEnvironment(t)
			t.Setenv(testCase.environment, testCase.value)

			_, err := LoadGeneration()
			if err == nil {
				t.Fatalf("LoadGeneration() error = nil for %s=%q", testCase.environment, testCase.value)
			}
			if testCase.wantedError != nil && !errors.Is(err, testCase.wantedError) {
				t.Fatalf("LoadGeneration() error = %v, want %v", err, testCase.wantedError)
			}
		})
	}
}

func clearGenerationEnvironment(t *testing.T) {
	t.Helper()

	for _, name := range []string{
		"ANSWER_ENABLED",
		"DASHSCOPE_API_KEY",
		"DASHSCOPE_GENERATION_ENDPOINT",
		"GENERATION_MODEL",
		"GENERATION_HTTP_TIMEOUT",
		"GENERATION_MAX_OUTPUT_TOKENS",
		"GENERATION_TEMPERATURE",
		"GENERATION_THINKING_ENABLED",
	} {
		t.Setenv(name, "")
	}
}
