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
		generationConfig.ThinkingEnabled ||
		generationConfig.MaxConcurrency != defaultAnswerMaxConcurrency ||
		generationConfig.MaxConcurrencyPerUser != defaultAnswerMaxConcurrencyPerUser ||
		generationConfig.MaxWaitersGlobal != defaultAnswerMaxWaitersGlobal ||
		generationConfig.MaxWaitersPerUser != defaultAnswerMaxWaitersPerUser ||
		generationConfig.QueueWaitTimeout != defaultAnswerQueueWaitTimeout {
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
	t.Setenv("ANSWER_MAX_CONCURRENCY", "4")
	t.Setenv("ANSWER_MAX_CONCURRENCY_PER_USER", "2")
	t.Setenv("ANSWER_MAX_WAITERS_GLOBAL", "80")
	t.Setenv("ANSWER_MAX_WAITERS_PER_USER", "6")
	t.Setenv("ANSWER_QUEUE_WAIT_TIMEOUT", "5s")

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
		!generationConfig.ThinkingEnabled ||
		generationConfig.MaxConcurrency != 4 ||
		generationConfig.MaxConcurrencyPerUser != 2 ||
		generationConfig.MaxWaitersGlobal != 80 ||
		generationConfig.MaxWaitersPerUser != 6 ||
		generationConfig.QueueWaitTimeout != 5*time.Second {
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
		{name: "zero answer concurrency", environment: "ANSWER_MAX_CONCURRENCY", value: "0"},
		{name: "answer concurrency above maximum", environment: "ANSWER_MAX_CONCURRENCY", value: "129"},
		{name: "zero answer per-user concurrency", environment: "ANSWER_MAX_CONCURRENCY_PER_USER", value: "0"},
		{name: "answer per-user concurrency above maximum", environment: "ANSWER_MAX_CONCURRENCY_PER_USER", value: "17"},
		{name: "zero global answer waiters", environment: "ANSWER_MAX_WAITERS_GLOBAL", value: "0"},
		{name: "global answer waiters above maximum", environment: "ANSWER_MAX_WAITERS_GLOBAL", value: "10001"},
		{name: "zero per-user answer waiters", environment: "ANSWER_MAX_WAITERS_PER_USER", value: "0"},
		{name: "per-user answer waiters above maximum", environment: "ANSWER_MAX_WAITERS_PER_USER", value: "101"},
		{name: "invalid answer wait timeout", environment: "ANSWER_QUEUE_WAIT_TIMEOUT", value: "soon"},
		{name: "zero answer wait timeout", environment: "ANSWER_QUEUE_WAIT_TIMEOUT", value: "0s"},
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

func TestLoadGenerationRejectsPerUserLimitsAboveGlobalLimits(t *testing.T) {
	testCases := []struct {
		name        string
		environment map[string]string
	}{
		{
			name: "per-user concurrency above global concurrency",
			environment: map[string]string{
				"ANSWER_MAX_CONCURRENCY":          "3",
				"ANSWER_MAX_CONCURRENCY_PER_USER": "4",
			},
		},
		{
			name: "per-user waiters above global waiters",
			environment: map[string]string{
				"ANSWER_MAX_WAITERS_GLOBAL":   "4",
				"ANSWER_MAX_WAITERS_PER_USER": "5",
			},
		},
	}

	for _, testCase := range testCases {
		t.Run(testCase.name, func(t *testing.T) {
			clearGenerationEnvironment(t)
			for name, value := range testCase.environment {
				t.Setenv(name, value)
			}

			_, err := LoadGeneration()
			if !errors.Is(err, ErrInvalidAnswerAdmissionLimits) {
				t.Fatalf("LoadGeneration() error = %v, want ErrInvalidAnswerAdmissionLimits", err)
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
		"ANSWER_MAX_CONCURRENCY",
		"ANSWER_MAX_CONCURRENCY_PER_USER",
		"ANSWER_MAX_WAITERS_GLOBAL",
		"ANSWER_MAX_WAITERS_PER_USER",
		"ANSWER_QUEUE_WAIT_TIMEOUT",
	} {
		t.Setenv(name, "")
	}
}
