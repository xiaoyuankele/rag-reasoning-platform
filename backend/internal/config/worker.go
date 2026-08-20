package config

import (
	"fmt"
	"os"
	"strings"
	"time"
)

const (
	defaultWorkerPollInterval        = 2 * time.Second
	defaultWorkerProcessingTimeout   = 5 * time.Minute
	defaultDocumentWorkerConcurrency = 1
	maximumDocumentWorkerConcurrency = 4
)

// WorkerConfig 保存后台 Worker 的运行配置。
type WorkerConfig struct {
	PollInterval        time.Duration
	ProcessingTimeout   time.Duration
	DocumentConcurrency int
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

	return WorkerConfig{
		PollInterval:        pollInterval,
		ProcessingTimeout:   processingTimeout,
		DocumentConcurrency: documentConcurrency,
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
