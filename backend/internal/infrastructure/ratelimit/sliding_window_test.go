package ratelimit

import (
	"errors"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

func TestSlidingWindowLimiterEnforcesClientAndGlobalBudgets(t *testing.T) {
	limiter, err := NewSlidingWindowLimiter(time.Minute, 2, 3)
	if err != nil {
		t.Fatalf("NewSlidingWindowLimiter() error = %v, want nil", err)
	}
	now := time.Date(2026, time.August, 16, 9, 0, 0, 0, time.UTC)

	if _, allowed := limiter.Allow("client-a", now); !allowed {
		t.Fatal("first client-a request was rejected")
	}
	if _, allowed := limiter.Allow("client-a", now.Add(time.Second)); !allowed {
		t.Fatal("second client-a request was rejected")
	}
	retryAt, allowed := limiter.Allow("client-a", now.Add(2*time.Second))
	if allowed || !retryAt.Equal(now.Add(time.Minute)) {
		t.Fatalf("third client-a request allowed=%v retryAt=%s", allowed, retryAt)
	}

	if _, allowed := limiter.Allow("client-b", now.Add(2*time.Second)); !allowed {
		t.Fatal("first client-b request was rejected before global budget was full")
	}
	globalRetryAt, allowed := limiter.Allow("client-c", now.Add(3*time.Second))
	if allowed || !globalRetryAt.Equal(now.Add(time.Minute)) {
		t.Fatalf("global overflow allowed=%v retryAt=%s", allowed, globalRetryAt)
	}

	// 到达最早请求的窗口边界后，该名额应被释放。
	if _, allowed := limiter.Allow("client-c", now.Add(time.Minute)); !allowed {
		t.Fatal("request was rejected after the oldest global attempt expired")
	}
}

func TestSlidingWindowLimiterIsAtomicUnderConcurrency(t *testing.T) {
	limiter, err := NewSlidingWindowLimiter(time.Minute, 10, 10)
	if err != nil {
		t.Fatalf("NewSlidingWindowLimiter() error = %v, want nil", err)
	}
	now := time.Date(2026, time.August, 16, 9, 0, 0, 0, time.UTC)

	var allowedCount atomic.Int32
	var waitGroup sync.WaitGroup
	for index := 0; index < 100; index++ {
		waitGroup.Add(1)
		go func() {
			defer waitGroup.Done()
			if _, allowed := limiter.Allow("same-client", now); allowed {
				allowedCount.Add(1)
			}
		}()
	}
	waitGroup.Wait()

	if allowedCount.Load() != 10 {
		t.Fatalf("allowed request count = %d, want 10", allowedCount.Load())
	}
}

func TestNewSlidingWindowLimiterRejectsInvalidConfiguration(t *testing.T) {
	for _, test := range []struct {
		name      string
		window    time.Duration
		perClient int
		global    int
	}{
		{name: "zero window", window: 0, perClient: 1, global: 1},
		{name: "zero client", window: time.Minute, perClient: 0, global: 1},
		{name: "zero global", window: time.Minute, perClient: 1, global: 0},
		{name: "global below client", window: time.Minute, perClient: 2, global: 1},
	} {
		t.Run(test.name, func(t *testing.T) {
			_, err := NewSlidingWindowLimiter(
				test.window,
				test.perClient,
				test.global,
			)
			if !errors.Is(err, ErrInvalidConfiguration) {
				t.Fatalf("error = %v, want ErrInvalidConfiguration", err)
			}
		})
	}
}
