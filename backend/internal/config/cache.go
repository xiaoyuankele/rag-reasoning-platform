package config

import (
	"errors"
	"fmt"
	"os"
	"strconv"
	"strings"
	"time"
)

const (
	defaultCacheNamespace          = "rag"
	defaultRedisAddress            = "127.0.0.1:6380"
	defaultRedisDatabase           = 0
	maximumRedisDatabase           = 15
	defaultCacheOperationTimeout   = 250 * time.Millisecond
	defaultQueryVectorCacheTTL     = 12 * time.Hour
	defaultQueryVectorLockTTL      = 30 * time.Second
	defaultQueryVectorWaitTimeout  = 2 * time.Second
	defaultAnswerResultCacheTTL    = 15 * time.Minute
	defaultAnswerResultLockTTL     = 90 * time.Second
	defaultAnswerResultWaitTimeout = 10 * time.Second
)

var (
	// ErrCacheHMACSecretRequired 表示缓存已启用，但缺少保护问题摘要的密钥。
	// 原始问题不能出现在 Redis Key 中，因此不能在缺少密钥时退化为明文 Key。
	ErrCacheHMACSecretRequired = errors.New(
		"cache HMAC secret must be provided when RAG cache is enabled",
	)
	// ErrCacheHMACSecretTooShort 表示密钥不足 32 字节，无法提供合理的 HMAC 强度。
	ErrCacheHMACSecretTooShort = errors.New(
		"cache HMAC secret must contain at least 32 bytes",
	)

	// ErrInvalidCacheTiming 表示填充等待时间不小于锁的自动过期时间。
	// 这种配置会让等待者在锁已经失效后继续等待，增加重复回源的概率。
	ErrInvalidCacheTiming = errors.New(
		"cache fill wait timeout must be shorter than cache lock TTL",
	)
)

// CacheConfig 保存查询向量缓存和问答结果缓存的 Redis 配置。
//
// Redis 只保存可丢弃的加速副本。PostgreSQL 仍是用户、文档、权限和
// corpus revision 的正式事实来源，因此 Redis 故障不能阻止业务继续执行。
type CacheConfig struct {
	Enabled                 bool
	Namespace               string
	RedisAddress            string
	RedisPassword           string
	RedisDatabase           int
	HMACSecret              string
	OperationTimeout        time.Duration
	QueryVectorTTL          time.Duration
	QueryVectorLockTTL      time.Duration
	QueryVectorWaitTimeout  time.Duration
	AnswerResultTTL         time.Duration
	AnswerResultLockTTL     time.Duration
	AnswerResultWaitTimeout time.Duration
}

// LoadCache 从环境变量读取 Redis RAG 缓存配置。
func LoadCache() (CacheConfig, error) {
	enabled, err := loadOptionalBool("RAG_CACHE_ENABLED", false)
	if err != nil {
		return CacheConfig{}, err
	}

	namespace := strings.TrimSpace(os.Getenv("CACHE_NAMESPACE"))
	if namespace == "" {
		namespace = defaultCacheNamespace
	}

	redisAddress := strings.TrimSpace(os.Getenv("REDIS_ADDRESS"))
	if redisAddress == "" {
		redisAddress = defaultRedisAddress
	}

	redisDatabase, err := loadRedisDatabase()
	if err != nil {
		return CacheConfig{}, err
	}

	hmacSecret := strings.TrimSpace(os.Getenv("CACHE_HMAC_SECRET"))
	if enabled && hmacSecret == "" {
		return CacheConfig{}, ErrCacheHMACSecretRequired
	}
	if enabled && len([]byte(hmacSecret)) < 32 {
		return CacheConfig{}, ErrCacheHMACSecretTooShort
	}

	operationTimeout, err := loadPositiveDuration(
		"CACHE_OPERATION_TIMEOUT",
		defaultCacheOperationTimeout,
	)
	if err != nil {
		return CacheConfig{}, fmt.Errorf("load cache operation timeout: %w", err)
	}
	queryVectorTTL, err := loadPositiveDuration(
		"QUERY_VECTOR_CACHE_TTL",
		defaultQueryVectorCacheTTL,
	)
	if err != nil {
		return CacheConfig{}, fmt.Errorf("load query vector cache TTL: %w", err)
	}
	queryVectorLockTTL, err := loadPositiveDuration(
		"QUERY_VECTOR_CACHE_LOCK_TTL",
		defaultQueryVectorLockTTL,
	)
	if err != nil {
		return CacheConfig{}, fmt.Errorf("load query vector cache lock TTL: %w", err)
	}
	queryVectorWaitTimeout, err := loadPositiveDuration(
		"QUERY_VECTOR_CACHE_WAIT_TIMEOUT",
		defaultQueryVectorWaitTimeout,
	)
	if err != nil {
		return CacheConfig{}, fmt.Errorf("load query vector cache wait timeout: %w", err)
	}
	answerResultTTL, err := loadPositiveDuration(
		"ANSWER_RESULT_CACHE_TTL",
		defaultAnswerResultCacheTTL,
	)
	if err != nil {
		return CacheConfig{}, fmt.Errorf("load answer result cache TTL: %w", err)
	}
	answerResultLockTTL, err := loadPositiveDuration(
		"ANSWER_RESULT_CACHE_LOCK_TTL",
		defaultAnswerResultLockTTL,
	)
	if err != nil {
		return CacheConfig{}, fmt.Errorf("load answer result cache lock TTL: %w", err)
	}
	answerResultWaitTimeout, err := loadPositiveDuration(
		"ANSWER_RESULT_CACHE_WAIT_TIMEOUT",
		defaultAnswerResultWaitTimeout,
	)
	if err != nil {
		return CacheConfig{}, fmt.Errorf("load answer result cache wait timeout: %w", err)
	}

	if queryVectorWaitTimeout >= queryVectorLockTTL ||
		answerResultWaitTimeout >= answerResultLockTTL {
		return CacheConfig{}, ErrInvalidCacheTiming
	}

	return CacheConfig{
		Enabled:                 enabled,
		Namespace:               namespace,
		RedisAddress:            redisAddress,
		RedisPassword:           os.Getenv("REDIS_PASSWORD"),
		RedisDatabase:           redisDatabase,
		HMACSecret:              hmacSecret,
		OperationTimeout:        operationTimeout,
		QueryVectorTTL:          queryVectorTTL,
		QueryVectorLockTTL:      queryVectorLockTTL,
		QueryVectorWaitTimeout:  queryVectorWaitTimeout,
		AnswerResultTTL:         answerResultTTL,
		AnswerResultLockTTL:     answerResultLockTTL,
		AnswerResultWaitTimeout: answerResultWaitTimeout,
	}, nil
}

func loadRedisDatabase() (int, error) {
	rawValue := strings.TrimSpace(os.Getenv("REDIS_DATABASE"))
	if rawValue == "" {
		return defaultRedisDatabase, nil
	}

	database, err := strconv.Atoi(rawValue)
	if err != nil {
		return 0, fmt.Errorf("REDIS_DATABASE must be an integer: %w", err)
	}
	if database < 0 || database > maximumRedisDatabase {
		return 0, fmt.Errorf(
			"REDIS_DATABASE must be between 0 and %d",
			maximumRedisDatabase,
		)
	}
	return database, nil
}
