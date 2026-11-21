// Package infrastructure 提供不属于某个具体业务领域的外部设施适配器。
package infrastructure

import (
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"time"

	"github.com/redis/go-redis/v9"
)

var (
	// ErrRedisCacheConfiguration 表示 Redis 地址或操作超时配置无效。
	ErrRedisCacheConfiguration = errors.New("Redis cache configuration is invalid")

	// ErrHMACDigesterSecretRequired 表示摘要器缺少密钥。
	ErrHMACDigesterSecretRequired = errors.New("HMAC digester secret is required")
)

// RedisCacheOptions 是 Redis Cache-Aside 适配器需要的连接参数。
type RedisCacheOptions struct {
	Address          string
	Password         string
	Database         int
	OperationTimeout time.Duration
}

// RedisCache 把 Redis 的字符串 GET/SET 和短租约能力转换为应用层需要的缓存端口。
//
// 它不决定缓存哪些业务数据，也不决定失败时是否回源；这些规则属于 Application。
// RedisCache 自身只负责协议、超时和安全释放锁。
type RedisCache struct {
	client           *redis.Client
	operationTimeout time.Duration
}

// releaseLockScript 只在 Value 仍等于当前持有者 token 时删除锁。
// 这样一个已经超时的旧持有者不会误删后来获得的新锁。
var releaseLockScript = redis.NewScript(`
	if redis.call("GET", KEYS[1]) == ARGV[1] then
		return redis.call("DEL", KEYS[1])
	end
	return 0
`)

// NewRedisCache 创建惰性连接的 Redis 客户端。创建成功不代表 Redis 当前在线；
// 调用方可以使用 Ping 做启动观测，但不能把缓存离线当成业务启动失败。
func NewRedisCache(options RedisCacheOptions) (*RedisCache, error) {
	if options.Address == "" || options.Database < 0 || options.OperationTimeout <= 0 {
		return nil, ErrRedisCacheConfiguration
	}

	client := redis.NewClient(&redis.Options{
		Addr:         options.Address,
		Password:     options.Password,
		DB:           options.Database,
		DialTimeout:  options.OperationTimeout,
		ReadTimeout:  options.OperationTimeout,
		WriteTimeout: options.OperationTimeout,
	})

	return &RedisCache{
		client:           client,
		operationTimeout: options.OperationTimeout,
	}, nil
}

// Ping 检查 Redis 当前是否可用。它只用于观测，不应成为业务正确性的前置条件。
func (c *RedisCache) Ping(ctx context.Context) error {
	operationContext, cancel := c.operationContext(ctx)
	defer cancel()
	return c.client.Ping(operationContext).Err()
}

// Get 读取二进制缓存值。不存在返回 found=false，Redis 故障返回 error。
func (c *RedisCache) Get(
	ctx context.Context,
	key string,
) ([]byte, bool, error) {
	operationContext, cancel := c.operationContext(ctx)
	defer cancel()

	value, err := c.client.Get(operationContext, key).Bytes()
	if errors.Is(err, redis.Nil) {
		return nil, false, nil
	}
	if err != nil {
		return nil, false, err
	}
	return value, true, nil
}

// Set 写入带 TTL 的二进制值。缓存只保存可丢弃副本，因此不使用永久 Key。
func (c *RedisCache) Set(
	ctx context.Context,
	key string,
	value []byte,
	ttl time.Duration,
) error {
	operationContext, cancel := c.operationContext(ctx)
	defer cancel()
	return c.client.Set(operationContext, key, value, ttl).Err()
}

// AcquireLease 使用 SET NX 获取一个自动过期的缓存填充租约。
func (c *RedisCache) AcquireLease(
	ctx context.Context,
	key string,
	token string,
	ttl time.Duration,
) (bool, error) {
	operationContext, cancel := c.operationContext(ctx)
	defer cancel()
	return c.client.SetNX(operationContext, key, token, ttl).Result()
}

// ReleaseLease 通过 Lua 比较 token 后再删除租约，避免误删别人的新租约。
func (c *RedisCache) ReleaseLease(
	ctx context.Context,
	key string,
	token string,
) error {
	operationContext, cancel := c.operationContext(ctx)
	defer cancel()
	return releaseLockScript.Run(
		operationContext,
		c.client,
		[]string{key},
		token,
	).Err()
}

// Close 释放 Redis 客户端持有的连接池。
func (c *RedisCache) Close() error {
	return c.client.Close()
}

func (c *RedisCache) operationContext(
	ctx context.Context,
) (context.Context, context.CancelFunc) {
	return context.WithTimeout(ctx, c.operationTimeout)
}

// HMACSHA256Digester 把已经规范化的问题转换为不可逆摘要。
// Redis Key 和日志只允许使用摘要，不能出现用户问题明文。
type HMACSHA256Digester struct {
	secret []byte
}

// NewHMACSHA256Digester 创建问题摘要器，并复制密钥避免调用方后续修改切片。
func NewHMACSHA256Digester(secret []byte) (*HMACSHA256Digester, error) {
	if len(secret) == 0 {
		return nil, ErrHMACDigesterSecretRequired
	}
	secretCopy := append([]byte(nil), secret...)
	return &HMACSHA256Digester{secret: secretCopy}, nil
}

// Digest 返回 64 个小写十六进制字符的 HMAC-SHA256 摘要。
func (d *HMACSHA256Digester) Digest(value string) string {
	mac := hmac.New(sha256.New, d.secret)
	_, _ = mac.Write([]byte(value))
	return hex.EncodeToString(mac.Sum(nil))
}
