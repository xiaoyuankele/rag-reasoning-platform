package embedding

import (
	"errors"
	"math/rand/v2"
	"time"
)

var (
	ErrInvalidMaxEmbeddingAttempts = errors.New(
		"maximum embedding attempts must be positive",
	)
	ErrInvalidEmbeddingRetryDelay = errors.New(
		"embedding retry delays must be positive and ordered",
	)
)

// RetryPolicy 决定临时故障还能否重试，以及最早何时重试。
//
// 它采用指数退避和 full jitter：连续失败时等待上限逐步翻倍，再在
// [0, 上限] 范围内随机选择实际延迟。随机抖动可以避免多个 Worker 同时重试，
// 再次集中冲击远程 API。
type RetryPolicy struct {
	maxAttempts int
	baseDelay   time.Duration
	maxDelay    time.Duration
	jitter      func(time.Duration) time.Duration
}

// NewRetryPolicy 创建生产环境使用的重试策略。
func NewRetryPolicy(
	maxAttempts int,
	baseDelay time.Duration,
	maxDelay time.Duration,
) (RetryPolicy, error) {
	return newRetryPolicy(
		maxAttempts,
		baseDelay,
		maxDelay,
		func(delayLimit time.Duration) time.Duration {
			// Int64N 的上界不包含在结果中，所以加 1 让 maxDelay 本身也可能被选中。
			return time.Duration(rand.Int64N(int64(delayLimit) + 1))
		},
	)
}

// newRetryPolicy 允许测试注入确定性的 jitter，避免依赖随机结果。
func newRetryPolicy(
	maxAttempts int,
	baseDelay time.Duration,
	maxDelay time.Duration,
	jitter func(time.Duration) time.Duration,
) (RetryPolicy, error) {
	if maxAttempts <= 0 {
		return RetryPolicy{}, ErrInvalidMaxEmbeddingAttempts
	}
	if baseDelay <= 0 || maxDelay <= 0 || baseDelay > maxDelay {
		return RetryPolicy{}, ErrInvalidEmbeddingRetryDelay
	}
	if jitter == nil {
		return RetryPolicy{}, errors.New("embedding retry jitter is required")
	}

	return RetryPolicy{
		maxAttempts: maxAttempts,
		baseDelay:   baseDelay,
		maxDelay:    maxDelay,
		jitter:      jitter,
	}, nil
}

// NextAttempt 计算当前失败后是否还允许下一次尝试。
//
// attemptCount 是本次领取后已经累计的尝试次数。达到 maxAttempts 时返回 false。
func (p RetryPolicy) NextAttempt(
	attemptCount int,
	now time.Time,
) (time.Time, bool) {
	if attemptCount >= p.maxAttempts {
		return time.Time{}, false
	}

	delayLimit := p.baseDelay
	// 第 1 次失败使用 baseDelay；此后每失败一次，上限翻倍，直到 maxDelay。
	for attempt := 1; attempt < attemptCount && delayLimit < p.maxDelay; attempt++ {
		if delayLimit > p.maxDelay/2 {
			delayLimit = p.maxDelay
			break
		}
		delayLimit *= 2
	}

	return now.Add(p.jitter(delayLimit)), true
}
