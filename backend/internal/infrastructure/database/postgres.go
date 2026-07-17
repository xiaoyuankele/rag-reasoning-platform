// Package database 负责创建和管理数据库连接。
package database

import (
	"context"
	"fmt"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
)

const (
	// pingTimeout 限制启动时数据库健康检查的最长等待时间。
	pingTimeout = 5 * time.Second

	// maxConnections 限制连接池最多创建 5 条数据库连接。
	maxConnections int32 = 5

	// maxConnectionIdleTime 表示连接空闲超过 5 分钟后可以被关闭。
	maxConnectionIdleTime = 5 * time.Minute
)

// Open 创建连接池，并通过 Ping 验证 PostgreSQL 是否真正可用。
//
// connectionString 包含数据库密码，调用者不得把它写入日志。
func Open(
	ctx context.Context,
	connectionString string,
) (*pgxpool.Pool, error) {
	// ParseConfig 解析连接字符串，并生成可进一步调整的连接池配置。
	poolConfig, err := pgxpool.ParseConfig(connectionString)
	if err != nil {
		return nil, fmt.Errorf("parse PostgreSQL configuration: %w", err)
	}

	// 限制连接数量，减少本机数据库和 Go 服务的资源占用。
	poolConfig.MaxConns = maxConnections
	poolConfig.MinConns = 0
	poolConfig.MaxConnIdleTime = maxConnectionIdleTime

	// NewWithConfig 创建并管理并发安全的连接池。
	// 创建连接池并不代表数据库一定已经能够访问，因此后面还要 Ping。
	pool, err := pgxpool.NewWithConfig(ctx, poolConfig)

	if err != nil {
		return nil, fmt.Errorf(
			"create PostgreSQL connection pool: %w",
			err,
		)
	}

	// 为 Ping 创建最多持续 5 秒的子 context。
	pingContext, cancel := context.WithTimeout(ctx, pingTimeout)

	// defer 表示 Open 函数结束前调用 cancel，释放定时器资源。
	defer cancel()

	// Ping 会真实获取连接并向 PostgreSQL 发送检查请求。
	if err := pool.Ping(pingContext); err != nil {
		// 如果检查失败，必须关闭已经创建的连接池，避免资源泄漏。
		pool.Close()

		return nil, fmt.Errorf("ping PostgreSQL: %w", err)
	}

	return pool, nil
}
