package infrastructure

import (
	"errors"
	"testing"
	"time"
)

func TestNewRedisCapacityStoreRejectsInvalidConfiguration(t *testing.T) {
	tests := []RedisCapacityOptions{
		{OperationTimeout: time.Second},
		{Address: "127.0.0.1:6379", Database: -1, OperationTimeout: time.Second},
		{Address: "127.0.0.1:6379"},
	}
	for _, options := range tests {
		if _, err := NewRedisCapacityStore(options); !errors.Is(err, ErrRedisCapacityConfiguration) {
			t.Fatalf("NewRedisCapacityStore(%+v) error = %v", options, err)
		}
	}
}

func TestValidateCapacityRequest(t *testing.T) {
	tests := []struct {
		keys   []string
		limits []int
		ttl    time.Duration
	}{
		{limits: []int{1}, ttl: time.Second},
		{keys: []string{"one"}, ttl: time.Second},
		{keys: []string{"one"}, limits: []int{1}},
		{keys: []string{""}, limits: []int{1}, ttl: time.Second},
		{keys: []string{"one"}, limits: []int{0}, ttl: time.Second},
		{keys: []string{"one", "one"}, limits: []int{1, 1}, ttl: time.Second},
	}
	for _, test := range tests {
		if err := validateCapacityRequest(test.keys, test.limits, test.ttl); !errors.Is(err, ErrRedisCapacityRequest) {
			t.Fatalf("validateCapacityRequest(%v, %v, %v) error = %v", test.keys, test.limits, test.ttl, err)
		}
	}

	if err := validateCapacityRequest(
		[]string{"global", "owner"},
		[]int{10, 2},
		time.Minute,
	); err != nil {
		t.Fatalf("valid request error = %v", err)
	}
}
