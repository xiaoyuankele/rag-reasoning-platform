// Package ratelimit 提供单实例进程内限流能力。
package ratelimit

import (
	"errors"
	"sync"
	"time"
)

var (
	// ErrInvalidConfiguration 表示时间窗口或请求上限不是正数，
	// 或全局上限小于单个客户端上限。
	ErrInvalidConfiguration = errors.New("invalid sliding-window rate-limit configuration")
)

// SlidingWindowLimiter 同时限制单个客户端和整个进程内的请求数量。
//
// 它只适用于当前单实例个人版。未来部署多个后端实例时，应通过相同调用
// 契约替换成 Redis 等共享限流实现，而不是让 Handler 感知存储变化。
type SlidingWindowLimiter struct {
	mutex          sync.Mutex
	window         time.Duration
	perClientLimit int
	globalLimit    int
	globalAttempts []time.Time
	clientAttempts map[string][]time.Time
}

// NewSlidingWindowLimiter 创建一个同时具有单客户端与全局预算的滑动窗口限流器。
func NewSlidingWindowLimiter(
	window time.Duration,
	perClientLimit int,
	globalLimit int,
) (*SlidingWindowLimiter, error) {
	if window <= 0 || perClientLimit <= 0 || globalLimit <= 0 ||
		globalLimit < perClientLimit {
		return nil, ErrInvalidConfiguration
	}

	return &SlidingWindowLimiter{
		window:         window,
		perClientLimit: perClientLimit,
		globalLimit:    globalLimit,
		globalAttempts: make([]time.Time, 0, globalLimit),
		clientAttempts: make(map[string][]time.Time),
	}, nil
}

// Allow 判断客户端当前是否还能占用一次请求名额。
//
// 允许时会同时记录客户端名额和全局名额；拒绝时返回最早可能重新尝试的
// 时间。整个检查与记录过程由同一把锁保护，并发请求不会突破配置上限。
func (l *SlidingWindowLimiter) Allow(
	clientKey string,
	now time.Time,
) (time.Time, bool) {
	l.mutex.Lock()
	defer l.mutex.Unlock()

	windowStart := now.Add(-l.window)
	l.globalAttempts = retainAttemptsAfter(
		l.globalAttempts,
		windowStart,
	)
	for key, attempts := range l.clientAttempts {
		activeAttempts := retainAttemptsAfter(attempts, windowStart)
		if len(activeAttempts) == 0 {
			delete(l.clientAttempts, key)
			continue
		}
		l.clientAttempts[key] = activeAttempts
	}

	clientAttempts := l.clientAttempts[clientKey]
	var retryAt time.Time
	if len(clientAttempts) >= l.perClientLimit {
		retryAt = clientAttempts[0].Add(l.window)
	}
	if len(l.globalAttempts) >= l.globalLimit {
		globalRetryAt := l.globalAttempts[0].Add(l.window)
		if globalRetryAt.After(retryAt) {
			retryAt = globalRetryAt
		}
	}
	if !retryAt.IsZero() {
		return retryAt, false
	}

	l.globalAttempts = append(l.globalAttempts, now)
	l.clientAttempts[clientKey] = append(clientAttempts, now)
	return time.Time{}, true
}

func retainAttemptsAfter(
	attempts []time.Time,
	cutoff time.Time,
) []time.Time {
	firstActiveIndex := 0
	for firstActiveIndex < len(attempts) &&
		!attempts[firstActiveIndex].After(cutoff) {
		firstActiveIndex++
	}

	if firstActiveIndex == len(attempts) {
		return attempts[:0]
	}

	return attempts[firstActiveIndex:]
}
