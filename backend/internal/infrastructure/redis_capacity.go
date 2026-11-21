package infrastructure

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
	"time"

	"github.com/redis/go-redis/v9"
)

var (
	// ErrRedisCapacityConfiguration 表示 Redis 地址、数据库或操作超时非法。
	ErrRedisCapacityConfiguration = errors.New(
		"Redis capacity coordination configuration is invalid",
	)
	// ErrRedisCapacityRequest 表示容量维度、上限或租约 TTL 非法。
	ErrRedisCapacityRequest = errors.New(
		"Redis capacity coordination request is invalid",
	)
	// ErrRedisCapacityResponse 表示 Lua 脚本返回了无法识别的数据。
	ErrRedisCapacityResponse = errors.New(
		"Redis capacity coordination response is invalid",
	)
)

// RedisCapacityOptions 是独立容量协调 Redis 的连接参数。
type RedisCapacityOptions struct {
	Address          string
	Password         string
	Database         int
	OperationTimeout time.Duration
}

// RedisCapacityStore 使用带 TTL 的 Redis 有序集合实现跨进程执行槽位。
//
// 每次租约会同时写入全部容量维度，例如“Embedding 全局 + Worker 分类”或
// “Answer 全局 + 当前 Owner”。Lua 保证检查和写入是一个原子操作。
type RedisCapacityStore struct {
	client           *redis.Client
	operationTimeout time.Duration
}

// acquireCapacityScript 使用 Redis TIME，避免不同主机的系统时钟偏差。
// 过期租约会在计数前清理；只有所有维度都有空位时才写入同一个 token。
var acquireCapacityScript = redis.NewScript(`
	local redis_time = redis.call("TIME")
	local now_ms = redis_time[1] * 1000 + math.floor(redis_time[2] / 1000)
	local ttl_ms = tonumber(ARGV[1])
	local token = ARGV[2]
	local expires_at = now_ms + ttl_ms
	local counts = {}

	for index, key in ipairs(KEYS) do
		redis.call("ZREMRANGEBYSCORE", key, "-inf", now_ms)
		local count = redis.call("ZCARD", key)
		counts[index] = count
	end

	for index, count in ipairs(counts) do
		if count >= tonumber(ARGV[index + 2]) then
			local result = {0}
			for _, value in ipairs(counts) do
				table.insert(result, value)
			end
			return result
		end
	end

	local result = {1}
	for index, key in ipairs(KEYS) do
		redis.call("ZADD", key, expires_at, token)
		redis.call("PEXPIRE", key, ttl_ms + 1000)
		table.insert(result, counts[index] + 1)
	end
	return result
`)

// releaseCapacityScript 只删除当前 token，不会影响其他进程持有的槽位。
var releaseCapacityScript = redis.NewScript(`
	local removed = 0
	for _, key in ipairs(KEYS) do
		removed = removed + redis.call("ZREM", key, ARGV[1])
	end
	return removed
`)

// NewRedisCapacityStore 创建惰性 Redis 容量协调客户端。
func NewRedisCapacityStore(
	options RedisCapacityOptions,
) (*RedisCapacityStore, error) {
	if options.Address == "" ||
		options.Database < 0 ||
		options.OperationTimeout <= 0 {
		return nil, ErrRedisCapacityConfiguration
	}

	client := redis.NewClient(&redis.Options{
		Addr:         options.Address,
		Password:     options.Password,
		DB:           options.Database,
		DialTimeout:  options.OperationTimeout,
		ReadTimeout:  options.OperationTimeout,
		WriteTimeout: options.OperationTimeout,
	})
	return &RedisCapacityStore{
		client:           client,
		operationTimeout: options.OperationTimeout,
	}, nil
}

// Ping 用于启用协调能力时的启动期 fail-fast 检查。
func (s *RedisCapacityStore) Ping(ctx context.Context) error {
	operationContext, cancel := s.operationContext(ctx)
	defer cancel()
	return s.client.Ping(operationContext).Err()
}

// AcquireCapacity 原子申请全部容量维度。
//
// acquired=false 只是容量已满，不是 Redis 故障。token 仅在申请成功时返回；
// counts 与 keys 顺序一致，表示脚本决策时各维度的占用量。
func (s *RedisCapacityStore) AcquireCapacity(
	ctx context.Context,
	keys []string,
	limits []int,
	ttl time.Duration,
) (token string, counts []int, acquired bool, err error) {
	if err := validateCapacityRequest(keys, limits, ttl); err != nil {
		return "", nil, false, err
	}

	token, err = newCapacityToken()
	if err != nil {
		return "", nil, false, fmt.Errorf("create capacity token: %w", err)
	}

	arguments := make([]any, 0, len(limits)+2)
	arguments = append(arguments, ttl.Milliseconds(), token)
	for _, limit := range limits {
		arguments = append(arguments, limit)
	}

	operationContext, cancel := s.operationContext(ctx)
	defer cancel()
	rawResult, err := acquireCapacityScript.Run(
		operationContext,
		s.client,
		keys,
		arguments...,
	).Slice()
	if err != nil {
		return "", nil, false, err
	}
	if len(rawResult) < 1 {
		return "", nil, false, ErrRedisCapacityResponse
	}

	admittedValue, ok := rawResult[0].(int64)
	if !ok || (admittedValue != 0 && admittedValue != 1) {
		return "", nil, false, ErrRedisCapacityResponse
	}
	counts = make([]int, len(rawResult)-1)
	for index, value := range rawResult[1:] {
		count, ok := value.(int64)
		if !ok || count < 0 {
			return "", nil, false, ErrRedisCapacityResponse
		}
		counts[index] = int(count)
	}
	if admittedValue == 0 {
		return "", counts, false, nil
	}
	if len(counts) != len(keys) {
		return "", nil, false, ErrRedisCapacityResponse
	}
	return token, counts, true, nil
}

// ReleaseCapacity 归还一次成功申请的全部容量维度。
// 进程异常退出时，租约也会在 TTL 后自动消失。
func (s *RedisCapacityStore) ReleaseCapacity(
	ctx context.Context,
	keys []string,
	token string,
) error {
	if len(keys) == 0 || token == "" {
		return ErrRedisCapacityRequest
	}
	operationContext, cancel := s.operationContext(ctx)
	defer cancel()
	return releaseCapacityScript.Run(
		operationContext,
		s.client,
		keys,
		token,
	).Err()
}

// Close 释放 Redis 连接池。
func (s *RedisCapacityStore) Close() error {
	return s.client.Close()
}

func (s *RedisCapacityStore) operationContext(
	ctx context.Context,
) (context.Context, context.CancelFunc) {
	return context.WithTimeout(ctx, s.operationTimeout)
}

func validateCapacityRequest(
	keys []string,
	limits []int,
	ttl time.Duration,
) error {
	if len(keys) == 0 || len(keys) != len(limits) || ttl <= 0 {
		return ErrRedisCapacityRequest
	}
	seen := make(map[string]struct{}, len(keys))
	for index, key := range keys {
		if key == "" || limits[index] <= 0 {
			return ErrRedisCapacityRequest
		}
		if _, exists := seen[key]; exists {
			return ErrRedisCapacityRequest
		}
		seen[key] = struct{}{}
	}
	return nil
}

func newCapacityToken() (string, error) {
	randomBytes := make([]byte, 16)
	if _, err := rand.Read(randomBytes); err != nil {
		return "", err
	}
	return hex.EncodeToString(randomBytes), nil
}
