package config

import (
	"errors"
	"fmt"
	"time"
)

const (
	defaultAnswerJobsWorkerConcurrency = 10
	maximumAnswerJobsWorkerConcurrency = 32
	defaultAnswerJobsPollInterval      = time.Second
	defaultAnswerJobsProcessingTimeout = 90 * time.Second
	defaultAnswerJobsMaxQueuedPerUser  = 5
	maximumAnswerJobsMaxQueuedPerUser  = 100
	defaultAnswerJobsMaxQueuedGlobal   = 500
	maximumAnswerJobsMaxQueuedGlobal   = 10000
	defaultAnswerJobsOwnerInFlight     = 1
	maximumAnswerJobsOwnerInFlight     = 16
	defaultAnswerJobsOwnerBorrowed     = 2
	maximumAnswerJobsOwnerBorrowed     = 32
	defaultAnswerJobsStarvation        = 30 * time.Second
	defaultAnswerJobsMaxAttempts       = 3
	maximumAnswerJobsMaxAttempts       = 10
	defaultAnswerJobsRetryBaseDelay    = 2 * time.Second
	defaultAnswerJobsRetryMaxDelay     = 30 * time.Second
	defaultAnswerJobsRetention         = 7 * 24 * time.Hour
	defaultAnswerJobsCleanupInterval   = time.Hour
	defaultAnswerJobsCleanupBatchSize  = 500
	maximumAnswerJobsCleanupBatchSize  = 10000
)

var (
	// ErrInvalidAnswerJobsCapacity 表示用户队列或执行上限超过全局能力。
	ErrInvalidAnswerJobsCapacity = errors.New(
		"answer job owner capacity must not exceed global worker or queue capacity",
	)

	// ErrInvalidAnswerJobsRetry 表示重试基础间隔大于最大退避间隔。
	ErrInvalidAnswerJobsRetry = errors.New(
		"answer job retry base delay must not exceed retry max delay",
	)
)

// AnswerJobsConfig 保存持久化问答队列、Worker 和重试策略。
// Enabled 默认关闭，防止开发环境启动后自动产生远程模型费用。
type AnswerJobsConfig struct {
	Enabled                    bool
	WorkerConcurrency          int
	PollInterval               time.Duration
	ProcessingTimeout          time.Duration
	MaxQueuedPerUser           int
	MaxQueuedGlobal            int
	OwnerInFlightLimit         int
	OwnerBorrowedInFlightLimit int
	StarvationThreshold        time.Duration
	MaxAttempts                int
	RetryBaseDelay             time.Duration
	RetryMaxDelay              time.Duration
	Retention                  time.Duration
	CleanupInterval            time.Duration
	CleanupBatchSize           int
}

// LoadAnswerJobs 从环境变量读取异步问答配置。
func LoadAnswerJobs() (AnswerJobsConfig, error) {
	enabled, err := loadOptionalBool("ANSWER_JOBS_ENABLED", false)
	if err != nil {
		return AnswerJobsConfig{}, err
	}

	workerConcurrency, err := loadPositiveBoundedInt(
		"ANSWER_JOB_WORKER_CONCURRENCY",
		defaultAnswerJobsWorkerConcurrency,
		maximumAnswerJobsWorkerConcurrency,
	)
	if err != nil {
		return AnswerJobsConfig{}, fmt.Errorf("load answer job worker concurrency: %w", err)
	}
	pollInterval, err := loadPositiveDuration(
		"ANSWER_JOB_POLL_INTERVAL",
		defaultAnswerJobsPollInterval,
	)
	if err != nil {
		return AnswerJobsConfig{}, fmt.Errorf("load answer job poll interval: %w", err)
	}
	processingTimeout, err := loadPositiveDuration(
		"ANSWER_JOB_PROCESSING_TIMEOUT",
		defaultAnswerJobsProcessingTimeout,
	)
	if err != nil {
		return AnswerJobsConfig{}, fmt.Errorf("load answer job processing timeout: %w", err)
	}
	maxQueuedPerUser, err := loadPositiveBoundedInt(
		"ANSWER_JOB_MAX_QUEUED_PER_USER",
		defaultAnswerJobsMaxQueuedPerUser,
		maximumAnswerJobsMaxQueuedPerUser,
	)
	if err != nil {
		return AnswerJobsConfig{}, fmt.Errorf("load answer job max queued per user: %w", err)
	}
	maxQueuedGlobal, err := loadPositiveBoundedInt(
		"ANSWER_JOB_MAX_QUEUED_GLOBAL",
		defaultAnswerJobsMaxQueuedGlobal,
		maximumAnswerJobsMaxQueuedGlobal,
	)
	if err != nil {
		return AnswerJobsConfig{}, fmt.Errorf("load answer job max queued global: %w", err)
	}
	ownerInFlight, err := loadPositiveBoundedInt(
		"ANSWER_JOB_OWNER_IN_FLIGHT_LIMIT",
		defaultAnswerJobsOwnerInFlight,
		maximumAnswerJobsOwnerInFlight,
	)
	if err != nil {
		return AnswerJobsConfig{}, fmt.Errorf("load answer job owner in-flight limit: %w", err)
	}
	ownerBorrowed, err := loadPositiveBoundedInt(
		"ANSWER_JOB_OWNER_BORROWED_LIMIT",
		defaultAnswerJobsOwnerBorrowed,
		maximumAnswerJobsOwnerBorrowed,
	)
	if err != nil {
		return AnswerJobsConfig{}, fmt.Errorf("load answer job owner borrowed limit: %w", err)
	}
	starvationThreshold, err := loadPositiveDuration(
		"ANSWER_JOB_STARVATION_THRESHOLD",
		defaultAnswerJobsStarvation,
	)
	if err != nil {
		return AnswerJobsConfig{}, fmt.Errorf("load answer job starvation threshold: %w", err)
	}
	maxAttempts, err := loadPositiveBoundedInt(
		"ANSWER_JOB_MAX_ATTEMPTS",
		defaultAnswerJobsMaxAttempts,
		maximumAnswerJobsMaxAttempts,
	)
	if err != nil {
		return AnswerJobsConfig{}, fmt.Errorf("load answer job max attempts: %w", err)
	}
	retryBaseDelay, err := loadPositiveDuration(
		"ANSWER_JOB_RETRY_BASE_DELAY",
		defaultAnswerJobsRetryBaseDelay,
	)
	if err != nil {
		return AnswerJobsConfig{}, fmt.Errorf("load answer job retry base delay: %w", err)
	}
	retryMaxDelay, err := loadPositiveDuration(
		"ANSWER_JOB_RETRY_MAX_DELAY",
		defaultAnswerJobsRetryMaxDelay,
	)
	if err != nil {
		return AnswerJobsConfig{}, fmt.Errorf("load answer job retry max delay: %w", err)
	}
	retention, err := loadPositiveDuration(
		"ANSWER_JOB_RETENTION",
		defaultAnswerJobsRetention,
	)
	if err != nil {
		return AnswerJobsConfig{}, fmt.Errorf("load answer job retention: %w", err)
	}
	cleanupInterval, err := loadPositiveDuration(
		"ANSWER_JOB_CLEANUP_INTERVAL",
		defaultAnswerJobsCleanupInterval,
	)
	if err != nil {
		return AnswerJobsConfig{}, fmt.Errorf("load answer job cleanup interval: %w", err)
	}
	cleanupBatchSize, err := loadPositiveBoundedInt(
		"ANSWER_JOB_CLEANUP_BATCH_SIZE",
		defaultAnswerJobsCleanupBatchSize,
		maximumAnswerJobsCleanupBatchSize,
	)
	if err != nil {
		return AnswerJobsConfig{}, fmt.Errorf("load answer job cleanup batch size: %w", err)
	}

	if maxQueuedPerUser > maxQueuedGlobal ||
		ownerInFlight > ownerBorrowed ||
		ownerBorrowed > workerConcurrency {
		return AnswerJobsConfig{}, ErrInvalidAnswerJobsCapacity
	}
	if retryBaseDelay > retryMaxDelay {
		return AnswerJobsConfig{}, ErrInvalidAnswerJobsRetry
	}

	return AnswerJobsConfig{
		Enabled:                    enabled,
		WorkerConcurrency:          workerConcurrency,
		PollInterval:               pollInterval,
		ProcessingTimeout:          processingTimeout,
		MaxQueuedPerUser:           maxQueuedPerUser,
		MaxQueuedGlobal:            maxQueuedGlobal,
		OwnerInFlightLimit:         ownerInFlight,
		OwnerBorrowedInFlightLimit: ownerBorrowed,
		StarvationThreshold:        starvationThreshold,
		MaxAttempts:                maxAttempts,
		RetryBaseDelay:             retryBaseDelay,
		RetryMaxDelay:              retryMaxDelay,
		Retention:                  retention,
		CleanupInterval:            cleanupInterval,
		CleanupBatchSize:           cleanupBatchSize,
	}, nil
}
