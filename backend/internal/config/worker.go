package config

import (
	"fmt"
	"os"
	"strings"
	"time"
)

const defaultWorkerPollInterval = 2 * time.Second

// WorkerConfig 保存后台 Worker 的运行配置。
type WorkerConfig struct {
	PollInterval time.Duration
}

// LoadWorker 从环境变量读取并校验 Worker 配置。
func LoadWorker() (WorkerConfig, error) {
	value := strings.TrimSpace(
		os.Getenv("WORKER_POLL_INTERVAL"),
	)

	// 1. value 为空时，使用 defaultWorkerPollInterval。
	if value == "" {
		return WorkerConfig{
			PollInterval: defaultWorkerPollInterval,
		}, nil
	}

	pollInterval, err := time.ParseDuration(value)
	if err != nil {
		return WorkerConfig{}, fmt.Errorf(
			"WORKER_POLL_INTERVAL must be a valid duration: %w",
			err,
		)
	}

	// 零或负间隔会让定时器立即触发，导致 Worker 高频轮询数据库。
	if pollInterval <= 0 {
		return WorkerConfig{}, fmt.Errorf(
			"WORKER_POLL_INTERVAL must be greater than zero",
		)
	}

	return WorkerConfig{PollInterval: pollInterval}, nil
}
