package config

import (
	"testing"
	"time"
)

func TestLoadCapacityCoordinationUsesSafeDefaults(t *testing.T) {
	clearCapacityCoordinationEnvironment(t)

	actual, err := LoadCapacityCoordination()
	if err != nil {
		t.Fatalf("LoadCapacityCoordination() error = %v", err)
	}
	if actual.Enabled ||
		actual.Namespace != defaultCapacityNamespace ||
		actual.RedisAddress != defaultCapacityRedisAddress ||
		actual.RedisPassword != "" ||
		actual.RedisDatabase != defaultCapacityRedisDatabase ||
		actual.OperationTimeout != defaultCapacityOperationTimeout ||
		actual.LeaseTTL != defaultCapacityLeaseTTL ||
		actual.RetryInterval != defaultCapacityRetryInterval {
		t.Fatalf("default capacity coordination config = %+v", actual)
	}
}

func TestLoadCapacityCoordinationUsesEnvironment(t *testing.T) {
	clearCapacityCoordinationEnvironment(t)
	t.Setenv("CAPACITY_COORDINATION_ENABLED", "true")
	t.Setenv("CAPACITY_NAMESPACE", " shared-provider ")
	t.Setenv("CAPACITY_REDIS_ADDRESS", " redis-capacity.example:6379 ")
	t.Setenv("CAPACITY_REDIS_PASSWORD", " capacity-password ")
	t.Setenv("CAPACITY_REDIS_DATABASE", "2")
	t.Setenv("CAPACITY_OPERATION_TIMEOUT", "500ms")
	t.Setenv("CAPACITY_LEASE_TTL", "3m")
	t.Setenv("CAPACITY_RETRY_INTERVAL", "40ms")

	actual, err := LoadCapacityCoordination()
	if err != nil {
		t.Fatalf("LoadCapacityCoordination() error = %v", err)
	}
	if !actual.Enabled ||
		actual.Namespace != "shared-provider" ||
		actual.RedisAddress != "redis-capacity.example:6379" ||
		actual.RedisPassword != " capacity-password " ||
		actual.RedisDatabase != 2 ||
		actual.OperationTimeout != 500*time.Millisecond ||
		actual.LeaseTTL != 3*time.Minute ||
		actual.RetryInterval != 40*time.Millisecond {
		t.Fatalf("capacity coordination config = %+v", actual)
	}
}

func TestLoadCapacityCoordinationRejectsInvalidValues(t *testing.T) {
	tests := []struct {
		name  string
		key   string
		value string
	}{
		{name: "invalid enabled", key: "CAPACITY_COORDINATION_ENABLED", value: "sometimes"},
		{name: "invalid database", key: "CAPACITY_REDIS_DATABASE", value: "coordination"},
		{name: "database below range", key: "CAPACITY_REDIS_DATABASE", value: "-1"},
		{name: "database above range", key: "CAPACITY_REDIS_DATABASE", value: "16"},
		{name: "zero operation timeout", key: "CAPACITY_OPERATION_TIMEOUT", value: "0s"},
		{name: "zero lease TTL", key: "CAPACITY_LEASE_TTL", value: "0s"},
		{name: "zero retry interval", key: "CAPACITY_RETRY_INTERVAL", value: "0s"},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			clearCapacityCoordinationEnvironment(t)
			t.Setenv(test.key, test.value)
			if _, err := LoadCapacityCoordination(); err == nil {
				t.Fatalf("LoadCapacityCoordination() error = nil for %s=%q", test.key, test.value)
			}
		})
	}
}

func clearCapacityCoordinationEnvironment(t *testing.T) {
	t.Helper()
	for _, name := range []string{
		"CAPACITY_COORDINATION_ENABLED",
		"CAPACITY_NAMESPACE",
		"CAPACITY_REDIS_ADDRESS",
		"CAPACITY_REDIS_PASSWORD",
		"CAPACITY_REDIS_DATABASE",
		"CAPACITY_OPERATION_TIMEOUT",
		"CAPACITY_LEASE_TTL",
		"CAPACITY_RETRY_INTERVAL",
	} {
		t.Setenv(name, "")
	}
}
