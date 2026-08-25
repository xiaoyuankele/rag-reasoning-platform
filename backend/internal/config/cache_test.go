package config

import (
	"errors"
	"testing"
	"time"
)

func TestLoadCacheUsesSafeDefaults(t *testing.T) {
	clearCacheEnvironment(t)

	cacheConfig, err := LoadCache()
	if err != nil {
		t.Fatalf("LoadCache() error = %v, want nil", err)
	}
	if cacheConfig.Enabled ||
		cacheConfig.Namespace != defaultCacheNamespace ||
		cacheConfig.RedisAddress != defaultRedisAddress ||
		cacheConfig.RedisPassword != "" ||
		cacheConfig.RedisDatabase != defaultRedisDatabase ||
		cacheConfig.HMACSecret != "" ||
		cacheConfig.OperationTimeout != defaultCacheOperationTimeout ||
		cacheConfig.QueryVectorTTL != defaultQueryVectorCacheTTL ||
		cacheConfig.QueryVectorLockTTL != defaultQueryVectorLockTTL ||
		cacheConfig.QueryVectorWaitTimeout != defaultQueryVectorWaitTimeout ||
		cacheConfig.AnswerResultTTL != defaultAnswerResultCacheTTL ||
		cacheConfig.AnswerResultLockTTL != defaultAnswerResultLockTTL ||
		cacheConfig.AnswerResultWaitTimeout != defaultAnswerResultWaitTimeout {
		t.Fatalf("default cache config = %+v", cacheConfig)
	}
}

func TestLoadCacheUsesEnvironment(t *testing.T) {
	clearCacheEnvironment(t)
	t.Setenv("RAG_CACHE_ENABLED", "true")
	t.Setenv("CACHE_NAMESPACE", " test-rag ")
	t.Setenv("REDIS_ADDRESS", " redis.example:6380 ")
	t.Setenv("REDIS_PASSWORD", " redis-password ")
	t.Setenv("REDIS_DATABASE", "3")
	t.Setenv("CACHE_HMAC_SECRET", " 0123456789abcdef0123456789abcdef ")
	t.Setenv("CACHE_OPERATION_TIMEOUT", "400ms")
	t.Setenv("QUERY_VECTOR_CACHE_TTL", "6h")
	t.Setenv("QUERY_VECTOR_CACHE_LOCK_TTL", "20s")
	t.Setenv("QUERY_VECTOR_CACHE_WAIT_TIMEOUT", "1s")
	t.Setenv("ANSWER_RESULT_CACHE_TTL", "10m")
	t.Setenv("ANSWER_RESULT_CACHE_LOCK_TTL", "60s")
	t.Setenv("ANSWER_RESULT_CACHE_WAIT_TIMEOUT", "8s")

	cacheConfig, err := LoadCache()
	if err != nil {
		t.Fatalf("LoadCache() error = %v, want nil", err)
	}
	if !cacheConfig.Enabled ||
		cacheConfig.Namespace != "test-rag" ||
		cacheConfig.RedisAddress != "redis.example:6380" ||
		cacheConfig.RedisPassword != " redis-password " ||
		cacheConfig.RedisDatabase != 3 ||
		cacheConfig.HMACSecret != "0123456789abcdef0123456789abcdef" ||
		cacheConfig.OperationTimeout != 400*time.Millisecond ||
		cacheConfig.QueryVectorTTL != 6*time.Hour ||
		cacheConfig.QueryVectorLockTTL != 20*time.Second ||
		cacheConfig.QueryVectorWaitTimeout != time.Second ||
		cacheConfig.AnswerResultTTL != 10*time.Minute ||
		cacheConfig.AnswerResultLockTTL != time.Minute ||
		cacheConfig.AnswerResultWaitTimeout != 8*time.Second {
		t.Fatalf("cache config = %+v, want environment values", cacheConfig)
	}
}

func TestLoadCacheRejectsShortHMACSecretWhenEnabled(t *testing.T) {
	clearCacheEnvironment(t)
	t.Setenv("RAG_CACHE_ENABLED", "true")
	t.Setenv("CACHE_HMAC_SECRET", "too-short")

	_, err := LoadCache()
	if !errors.Is(err, ErrCacheHMACSecretTooShort) {
		t.Fatalf("LoadCache() error = %v, want ErrCacheHMACSecretTooShort", err)
	}
}

func TestLoadCacheRequiresHMACSecretOnlyWhenEnabled(t *testing.T) {
	clearCacheEnvironment(t)
	t.Setenv("RAG_CACHE_ENABLED", "true")

	_, err := LoadCache()
	if !errors.Is(err, ErrCacheHMACSecretRequired) {
		t.Fatalf("LoadCache() error = %v, want ErrCacheHMACSecretRequired", err)
	}
}

func TestLoadCacheRejectsInvalidValues(t *testing.T) {
	testCases := []struct {
		name        string
		environment string
		value       string
	}{
		{name: "invalid enabled", environment: "RAG_CACHE_ENABLED", value: "sometimes"},
		{name: "non-numeric Redis database", environment: "REDIS_DATABASE", value: "cache"},
		{name: "negative Redis database", environment: "REDIS_DATABASE", value: "-1"},
		{name: "Redis database above maximum", environment: "REDIS_DATABASE", value: "16"},
		{name: "invalid operation timeout", environment: "CACHE_OPERATION_TIMEOUT", value: "soon"},
		{name: "zero query TTL", environment: "QUERY_VECTOR_CACHE_TTL", value: "0s"},
		{name: "zero answer TTL", environment: "ANSWER_RESULT_CACHE_TTL", value: "0s"},
	}

	for _, testCase := range testCases {
		t.Run(testCase.name, func(t *testing.T) {
			clearCacheEnvironment(t)
			t.Setenv(testCase.environment, testCase.value)

			if _, err := LoadCache(); err == nil {
				t.Fatalf("LoadCache() error = nil for %s=%q", testCase.environment, testCase.value)
			}
		})
	}
}

func TestLoadCacheRejectsWaitTimeoutAtOrAboveLockTTL(t *testing.T) {
	testCases := []struct {
		name        string
		environment map[string]string
	}{
		{
			name: "query vector wait equals lock TTL",
			environment: map[string]string{
				"QUERY_VECTOR_CACHE_LOCK_TTL":     "2s",
				"QUERY_VECTOR_CACHE_WAIT_TIMEOUT": "2s",
			},
		},
		{
			name: "answer wait exceeds lock TTL",
			environment: map[string]string{
				"ANSWER_RESULT_CACHE_LOCK_TTL":     "5s",
				"ANSWER_RESULT_CACHE_WAIT_TIMEOUT": "6s",
			},
		},
	}

	for _, testCase := range testCases {
		t.Run(testCase.name, func(t *testing.T) {
			clearCacheEnvironment(t)
			for name, value := range testCase.environment {
				t.Setenv(name, value)
			}

			_, err := LoadCache()
			if !errors.Is(err, ErrInvalidCacheTiming) {
				t.Fatalf("LoadCache() error = %v, want ErrInvalidCacheTiming", err)
			}
		})
	}
}

func clearCacheEnvironment(t *testing.T) {
	t.Helper()
	for _, name := range []string{
		"RAG_CACHE_ENABLED",
		"CACHE_NAMESPACE",
		"REDIS_ADDRESS",
		"REDIS_PASSWORD",
		"REDIS_DATABASE",
		"CACHE_HMAC_SECRET",
		"CACHE_OPERATION_TIMEOUT",
		"QUERY_VECTOR_CACHE_TTL",
		"QUERY_VECTOR_CACHE_LOCK_TTL",
		"QUERY_VECTOR_CACHE_WAIT_TIMEOUT",
		"ANSWER_RESULT_CACHE_TTL",
		"ANSWER_RESULT_CACHE_LOCK_TTL",
		"ANSWER_RESULT_CACHE_WAIT_TIMEOUT",
	} {
		t.Setenv(name, "")
	}
}
