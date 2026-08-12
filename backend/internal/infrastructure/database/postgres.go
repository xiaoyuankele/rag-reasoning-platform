// Package database 负责创建和管理数据库连接。
package database

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
	pgxvector "github.com/pgvector/pgvector-go/pgx"
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
	poolConfig.AfterConnect = registerVectorTypesWhenAvailable

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

// registerVectorTypesWhenAvailable 在每条新建 pgx 连接上注册 pgvector 编解码器。
//
// 首次启动时连接池先于数据库迁移创建，此时 vector 扩展可能还不存在，因此这里先查询
// 扩展类型是否可用。迁移完成后 RefreshVectorTypes 会重建现有连接；之后的新连接都会在
// AfterConnect 中自动注册，不需要 Repository 自己处理数据库类型。
func registerVectorTypesWhenAvailable(
	ctx context.Context,
	connection *pgx.Conn,
) error {
	var vectorAvailable bool
	if err := connection.QueryRow(
		ctx,
		"SELECT to_regtype('public.vector') IS NOT NULL",
	).Scan(&vectorAvailable); err != nil {
		return fmt.Errorf("check pgvector type availability: %w", err)
	}
	if !vectorAvailable {
		return nil
	}

	if err := pgxvector.RegisterTypes(ctx, connection); err != nil {
		return fmt.Errorf("register pgvector types: %w", err)
	}

	return nil
}

// RefreshVectorTypes 在创建 vector 扩展的迁移完成后刷新连接池。
//
// Reset 只关闭空闲连接，不会中断正在使用的连接。服务启动阶段尚未开始接受 HTTP 请求，
// 因此随后 Acquire 得到的新连接会通过 AfterConnect 注册 vector 类型。
func RefreshVectorTypes(
	ctx context.Context,
	pool *pgxpool.Pool,
) error {
	if pool == nil {
		return errors.New("PostgreSQL connection pool must be provided")
	}

	pool.Reset()
	connection, err := pool.Acquire(ctx)
	if err != nil {
		return fmt.Errorf("acquire refreshed PostgreSQL connection: %w", err)
	}
	defer connection.Release()

	if _, registered := connection.Conn().TypeMap().TypeForName("vector"); !registered {
		return errors.New("pgvector type was not registered after migrations")
	}

	return nil
}
