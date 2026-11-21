package config

import (
	"fmt"
	"os"
	"strconv"
	"strings"
	"time"
)

const (
	defaultCapacityNamespace        = "rag-capacity"
	defaultCapacityRedisAddress     = "127.0.0.1:6381"
	defaultCapacityRedisDatabase    = 0
	defaultCapacityOperationTimeout = 250 * time.Millisecond
	defaultCapacityLeaseTTL         = 3 * time.Minute
	defaultCapacityRetryInterval    = 25 * time.Millisecond
)

// CapacityCoordinationConfig 保存跨进程远程模型容量协调配置。
//
// 它使用独立 Redis，而不是可淘汰的 RAG 缓存 Redis。Redis 中只保存带 TTL
// 的短期执行租约；任务状态、用户权限和最终结果仍以 PostgreSQL 为准。
type CapacityCoordinationConfig struct {
	Enabled          bool
	Namespace        string
	RedisAddress     string
	RedisPassword    string
	RedisDatabase    int
	OperationTimeout time.Duration
	LeaseTTL         time.Duration
	RetryInterval    time.Duration
}

// LoadCapacityCoordination 从环境变量加载跨进程容量协调配置。
func LoadCapacityCoordination() (CapacityCoordinationConfig, error) {
	enabled, err := loadOptionalBool("CAPACITY_COORDINATION_ENABLED", false)
	if err != nil {
		return CapacityCoordinationConfig{}, err
	}

	namespace := strings.TrimSpace(os.Getenv("CAPACITY_NAMESPACE"))
	if namespace == "" {
		namespace = defaultCapacityNamespace
	}

	redisAddress := strings.TrimSpace(os.Getenv("CAPACITY_REDIS_ADDRESS"))
	if redisAddress == "" {
		redisAddress = defaultCapacityRedisAddress
	}

	redisDatabase, err := loadCapacityRedisDatabase()
	if err != nil {
		return CapacityCoordinationConfig{}, err
	}

	operationTimeout, err := loadPositiveDuration(
		"CAPACITY_OPERATION_TIMEOUT",
		defaultCapacityOperationTimeout,
	)
	if err != nil {
		return CapacityCoordinationConfig{}, fmt.Errorf(
			"load capacity operation timeout: %w",
			err,
		)
	}

	leaseTTL, err := loadPositiveDuration(
		"CAPACITY_LEASE_TTL",
		defaultCapacityLeaseTTL,
	)
	if err != nil {
		return CapacityCoordinationConfig{}, fmt.Errorf(
			"load capacity lease TTL: %w",
			err,
		)
	}

	retryInterval, err := loadPositiveDuration(
		"CAPACITY_RETRY_INTERVAL",
		defaultCapacityRetryInterval,
	)
	if err != nil {
		return CapacityCoordinationConfig{}, fmt.Errorf(
			"load capacity retry interval: %w",
			err,
		)
	}

	return CapacityCoordinationConfig{
		Enabled:          enabled,
		Namespace:        namespace,
		RedisAddress:     redisAddress,
		RedisPassword:    os.Getenv("CAPACITY_REDIS_PASSWORD"),
		RedisDatabase:    redisDatabase,
		OperationTimeout: operationTimeout,
		LeaseTTL:         leaseTTL,
		RetryInterval:    retryInterval,
	}, nil
}

func loadCapacityRedisDatabase() (int, error) {
	rawValue := strings.TrimSpace(os.Getenv("CAPACITY_REDIS_DATABASE"))
	if rawValue == "" {
		return defaultCapacityRedisDatabase, nil
	}

	database, err := strconv.Atoi(rawValue)
	if err != nil {
		return 0, fmt.Errorf(
			"CAPACITY_REDIS_DATABASE must be an integer: %w",
			err,
		)
	}
	if database < 0 || database > maximumRedisDatabase {
		return 0, fmt.Errorf(
			"CAPACITY_REDIS_DATABASE must be between 0 and %d",
			maximumRedisDatabase,
		)
	}
	return database, nil
}
