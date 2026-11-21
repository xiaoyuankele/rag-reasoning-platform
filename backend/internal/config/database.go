package config

import (
	"fmt"
	"net"
	"net/url"
	"os"
	"strconv"
)

const (
	// 默认值与根目录的 .env.example 保持一致。
	defaultDBHost    = "localhost"
	defaultDBPort    = 5433
	defaultDBName    = "rag_platform"
	defaultDBUser    = "rag_user"
	defaultDBSSLMode = "disable"

	// defaultDBMaxConnections 是单个 Go 后端实例可以同时占用的数据库连接上限。
	// 压测环境的 PostgreSQL 独占 4 核 8GB，第一轮从 10 开始，再依据连接等待、
	// SQL 延迟和数据库 CPU 决定是否调整，不能直接按在线用户数配置连接数。
	defaultDBMaxConnections = 10
	maximumDBMaxConnections = 100
)

// DatabaseConfig 保存 Go 后端连接 PostgreSQL 所需的配置。
type DatabaseConfig struct {
	Host     string
	Port     int
	Name     string
	User     string
	Password string
	SSLMode  string

	// MaxConnections 限制当前后端实例的 PostgreSQL 连接池大小。
	// 多实例部署时，数据库看到的最大连接数约为所有实例配置之和。
	MaxConnections int
}

// LoadDatabase 从操作系统环境变量中读取并校验数据库配置。
// 当前函数不会自动读取 .env 文件。
func LoadDatabase() (DatabaseConfig, error) {
	// DB_PORT 是字符串，需要转换成整数。
	portValue := os.Getenv("DB_PORT")
	if portValue == "" {
		portValue = strconv.Itoa(defaultDBPort)
	}

	port, err := strconv.Atoi(portValue)
	if err != nil {
		return DatabaseConfig{}, fmt.Errorf("DB_PORT must be an integer: %w", err)
	}

	if port < minPort || port > maxPort {
		return DatabaseConfig{}, fmt.Errorf("DB_PORT must be between %d and %d", minPort, maxPort)
	}

	maxConnections, err := loadPositiveBoundedInt(
		"DB_MAX_CONNECTIONS",
		defaultDBMaxConnections,
		maximumDBMaxConnections,
	)
	if err != nil {
		return DatabaseConfig{}, fmt.Errorf(
			"load database max connections: %w",
			err,
		)
	}

	// 密码没有安全默认值，因此必须显式提供。
	password := os.Getenv("DB_PASSWORD")
	if password == "" {
		return DatabaseConfig{}, fmt.Errorf("DB_PASSWORD must be provided")
	}

	return DatabaseConfig{
		Host:           environmentOrDefault("DB_HOST", defaultDBHost),
		Port:           port,
		Name:           environmentOrDefault("DB_NAME", defaultDBName),
		User:           environmentOrDefault("DB_USER", defaultDBUser),
		Password:       password,
		SSLMode:        environmentOrDefault("DB_SSLMODE", defaultDBSSLMode),
		MaxConnections: maxConnections,
	}, nil
}

// ConnectionString 生成 PostgreSQL 驱动可以使用的连接地址。
// 返回值包含密码，不应写入日志或返回给前端。
func (c DatabaseConfig) ConnectionString() string {
	// url.UserPassword 会安全编码用户名和密码中的特殊字符。
	userInfo := url.UserPassword(c.User, c.Password)

	// JoinHostPort 会正确组合主机和端口。
	// 它也能正确处理包含冒号的 IPv6 地址。
	host := net.JoinHostPort(c.Host, strconv.Itoa(c.Port))

	// 使用 url.URL 结构体组装地址，避免手工字符串拼接产生转义错误。
	connectionURL := &url.URL{
		Scheme: "postgres",
		User:   userInfo,
		Host:   host,
		Path:   c.Name,
	}

	// Query 返回查询参数集合。
	// Set 添加或覆盖 sslmode 参数。
	query := connectionURL.Query()
	// pool_max_conns 是 pgxpool.ParseConfig 支持的连接池参数。
	// 把经过校验的值放进连接字符串，可以让服务器、维护命令和集成测试
	// 继续复用同一个 database.Open 入口，而不把 config 包传入基础设施层。
	query.Set("pool_max_conns", strconv.Itoa(c.MaxConnections))
	query.Set("sslmode", c.SSLMode)

	// Encode 会对查询参数排序并进行安全编码。
	connectionURL.RawQuery = query.Encode()

	return connectionURL.String()
}

// environmentOrDefault 在环境变量为空时返回默认值。
func environmentOrDefault(name string, defaultValue string) string {
	value := os.Getenv(name)

	if value == "" {
		return defaultValue
	}

	return value
}
