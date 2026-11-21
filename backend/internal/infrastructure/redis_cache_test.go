package infrastructure

import (
	"context"
	"errors"
	"os"
	"testing"
	"time"
)

func TestHMACSHA256DigesterIsStableAndSecretScoped(t *testing.T) {
	digesterA, err := NewHMACSHA256Digester([]byte("secret-a"))
	if err != nil {
		t.Fatalf("NewHMACSHA256Digester() error = %v, want nil", err)
	}
	digesterB, err := NewHMACSHA256Digester([]byte("secret-b"))
	if err != nil {
		t.Fatalf("NewHMACSHA256Digester() error = %v, want nil", err)
	}

	first := digesterA.Digest("规范化问题")
	second := digesterA.Digest("规范化问题")
	if first != second {
		t.Fatalf("same value digest = %q and %q, want equal", first, second)
	}
	if first == digesterB.Digest("规范化问题") {
		t.Fatal("different secrets produced the same digest")
	}
	if len(first) != 64 {
		t.Fatalf("digest length = %d, want 64", len(first))
	}
}

func TestNewHMACSHA256DigesterRequiresSecret(t *testing.T) {
	digester, err := NewHMACSHA256Digester(nil)
	if digester != nil || !errors.Is(err, ErrHMACDigesterSecretRequired) {
		t.Fatalf("NewHMACSHA256Digester(nil) = (%v, %v)", digester, err)
	}
}

func TestRedisCacheRoundTripAndLease(t *testing.T) {
	if os.Getenv("RUN_REDIS_TESTS") != "1" {
		t.Skip("set RUN_REDIS_TESTS=1 to run Redis integration tests")
	}

	address := os.Getenv("REDIS_TEST_ADDRESS")
	if address == "" {
		address = "127.0.0.1:6380"
	}
	cache, err := NewRedisCache(RedisCacheOptions{
		Address:          address,
		OperationTimeout: time.Second,
	})
	if err != nil {
		t.Fatalf("NewRedisCache() error = %v, want nil", err)
	}
	t.Cleanup(func() {
		if closeErr := cache.Close(); closeErr != nil {
			t.Errorf("Close() error = %v", closeErr)
		}
	})

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	if err := cache.Ping(ctx); err != nil {
		t.Fatalf("Ping() error = %v", err)
	}

	key := "rag:test:redis-cache:round-trip"
	leaseKey := key + ":lock"
	t.Cleanup(func() {
		cleanupContext, cleanupCancel := context.WithTimeout(
			context.Background(),
			time.Second,
		)
		defer cleanupCancel()
		if cleanupErr := cache.client.Del(
			cleanupContext,
			key,
			leaseKey,
		).Err(); cleanupErr != nil {
			t.Errorf("clean up Redis integration keys: %v", cleanupErr)
		}
	})
	if err := cache.Set(ctx, key, []byte{0, 1, 2, 255}, time.Minute); err != nil {
		t.Fatalf("Set() error = %v", err)
	}
	value, found, err := cache.Get(ctx, key)
	if err != nil || !found || len(value) != 4 || value[3] != 255 {
		t.Fatalf("Get() = (%v, %v, %v), want binary cache hit", value, found, err)
	}

	acquired, err := cache.AcquireLease(ctx, leaseKey, "owner-a", time.Minute)
	if err != nil || !acquired {
		t.Fatalf("first AcquireLease() = (%v, %v), want true, nil", acquired, err)
	}
	acquired, err = cache.AcquireLease(ctx, leaseKey, "owner-b", time.Minute)
	if err != nil || acquired {
		t.Fatalf("second AcquireLease() = (%v, %v), want false, nil", acquired, err)
	}
	if err := cache.ReleaseLease(ctx, leaseKey, "owner-b"); err != nil {
		t.Fatalf("ReleaseLease(wrong owner) error = %v", err)
	}
	acquired, err = cache.AcquireLease(ctx, leaseKey, "owner-b", time.Minute)
	if err != nil || acquired {
		t.Fatalf("AcquireLease() after wrong release = (%v, %v), want false, nil", acquired, err)
	}
	if err := cache.ReleaseLease(ctx, leaseKey, "owner-a"); err != nil {
		t.Fatalf("ReleaseLease(owner) error = %v", err)
	}
	acquired, err = cache.AcquireLease(ctx, leaseKey, "owner-b", time.Minute)
	if err != nil || !acquired {
		t.Fatalf("AcquireLease() after release = (%v, %v), want true, nil", acquired, err)
	}
}
