package config

import (
	"errors"
	"fmt"
	"os"
	"strings"
	"time"
)

const (
	defaultWorkerPollInterval            = 2 * time.Second
	defaultWorkerProcessingTimeout       = 5 * time.Minute
	defaultDocumentWorkerConcurrency     = 1
	maximumDocumentWorkerConcurrency     = 4
	defaultProcessingActiveOwnerLimit    = 5
	defaultProcessingActiveGlobalLimit   = 40
	maximumProcessingActiveJobLimit      = 10000
	defaultProcessingOwnerInFlightLimit  = 1
	defaultProcessingOwnerBorrowedLimit  = 2
	maximumProcessingOwnerInFlightLimit  = 64
	defaultProcessingStarvationThreshold = 2 * time.Minute
)

// ErrInvalidProcessingActiveJobLimits 表示全局容量小于单用户容量。
var ErrInvalidProcessingActiveJobLimits = errors.New(
	"processing global active job limit must not be smaller than the per-user limit",
)

// ErrInvalidProcessingOwnerSchedulingLimits 表示借用上限小于公平基础上限。
var ErrInvalidProcessingOwnerSchedulingLimits = errors.New(
	"processing borrowed owner in-flight limit must not be smaller than the base owner limit",
)

// WorkerConfig 保存后台 Worker 的运行配置。
type WorkerConfig struct {
	PollInterval           time.Duration
	ProcessingTimeout      time.Duration
	DocumentConcurrency    int
	ActiveJobsPerUserLimit int
	ActiveJobsGlobalLimit  int
	OwnerInFlightLimit     int
	OwnerBorrowedLimit     int
	StarvationThreshold    time.Duration
}

// LoadWorker 从环境变量读取并校验 Worker 配置。
func LoadWorker() (WorkerConfig, error) {
	pollInterval, err := loadPositiveDuration(
		"WORKER_POLL_INTERVAL",
		defaultWorkerPollInterval,
	)
	if err != nil {
		return WorkerConfig{}, fmt.Errorf(
			"load worker poll interval: %w",
			err,
		)
	}

	processingTimeout, err := loadPositiveDuration(
		"WORKER_PROCESSING_TIMEOUT",
		defaultWorkerProcessingTimeout,
	)
	if err != nil {
		return WorkerConfig{}, fmt.Errorf(
			"load worker processing timeout: %w",
			err,
		)
	}

	documentConcurrency, err := loadPositiveBoundedInt(
		"DOCUMENT_WORKER_CONCURRENCY",
		defaultDocumentWorkerConcurrency,
		maximumDocumentWorkerConcurrency,
	)
	if err != nil {
		return WorkerConfig{}, fmt.Errorf(
			"load document worker concurrency: %w",
			err,
		)
	}

	activeJobsPerUserLimit, err := loadPositiveBoundedInt(
		"PROCESSING_MAX_ACTIVE_JOBS_PER_USER",
		defaultProcessingActiveOwnerLimit,
		maximumProcessingActiveJobLimit,
	)
	if err != nil {
		return WorkerConfig{}, fmt.Errorf(
			"load processing per-user active job limit: %w",
			err,
		)
	}

	activeJobsGlobalLimit, err := loadPositiveBoundedInt(
		"PROCESSING_MAX_ACTIVE_JOBS_GLOBAL",
		defaultProcessingActiveGlobalLimit,
		maximumProcessingActiveJobLimit,
	)
	if err != nil {
		return WorkerConfig{}, fmt.Errorf(
			"load processing global active job limit: %w",
			err,
		)
	}
	if activeJobsGlobalLimit < activeJobsPerUserLimit {
		return WorkerConfig{}, ErrInvalidProcessingActiveJobLimits
	}

	ownerInFlightLimit, err := loadPositiveBoundedInt(
		"PROCESSING_MAX_IN_FLIGHT_PER_OWNER",
		defaultProcessingOwnerInFlightLimit,
		maximumProcessingOwnerInFlightLimit,
	)
	if err != nil {
		return WorkerConfig{}, fmt.Errorf(
			"load processing owner in-flight limit: %w",
			err,
		)
	}

	ownerBorrowedLimit, err := loadPositiveBoundedInt(
		"PROCESSING_MAX_BORROWED_IN_FLIGHT_PER_OWNER",
		defaultProcessingOwnerBorrowedLimit,
		maximumProcessingOwnerInFlightLimit,
	)
	if err != nil {
		return WorkerConfig{}, fmt.Errorf(
			"load processing borrowed owner in-flight limit: %w",
			err,
		)
	}
	if ownerBorrowedLimit < ownerInFlightLimit {
		return WorkerConfig{}, ErrInvalidProcessingOwnerSchedulingLimits
	}

	starvationThreshold, err := loadPositiveDuration(
		"PROCESSING_STARVATION_THRESHOLD",
		defaultProcessingStarvationThreshold,
	)
	if err != nil {
		return WorkerConfig{}, fmt.Errorf(
			"load processing starvation threshold: %w",
			err,
		)
	}

	return WorkerConfig{
		PollInterval:           pollInterval,
		ProcessingTimeout:      processingTimeout,
		DocumentConcurrency:    documentConcurrency,
		ActiveJobsPerUserLimit: activeJobsPerUserLimit,
		ActiveJobsGlobalLimit:  activeJobsGlobalLimit,
		OwnerInFlightLimit:     ownerInFlightLimit,
		OwnerBorrowedLimit:     ownerBorrowedLimit,
		StarvationThreshold:    starvationThreshold,
	}, nil
}

// loadPositiveDuration 读取一个必须为正数的 Go duration 环境变量。
func loadPositiveDuration(
	environmentName string,
	defaultValue time.Duration,
) (time.Duration, error) {
	value := strings.TrimSpace(
		os.Getenv(environmentName),
	)

	if value == "" {
		return defaultValue, nil
	}

	duration, err := time.ParseDuration(value)
	if err != nil {
		return 0, fmt.Errorf(
			"%s must be a valid duration: %w",
			environmentName,
			err,
		)
	}

	if duration <= 0 {
		return 0, fmt.Errorf(
			"%s must be greater than zero",
			environmentName,
		)
	}

	return duration, nil
}
