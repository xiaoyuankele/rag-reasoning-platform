// Package config 负责读取和校验应用程序配置。
package config

import (
	"fmt"
	"os"
	"strconv"
)

const (
	// 未设置 APP_PORT 时，服务默认使用 8080 端口。
	defaultPort = 8080

	// TCP 端口的有效范围是 1 到 65535。
	minPort = 1
	maxPort = 65535
)

// Config 保存应用程序运行时需要的配置。
// Port 首字母大写，因此其他包也可以读取该字段。
type Config struct {
	Port int
}

// Load 从操作系统环境变量中读取并校验配置。
// 第一个返回值是配置，第二个返回值是可能发生的错误。
func Load() (Config, error) {
	// os.Getenv 读取 APP_PORT；变量不存在时返回空字符串。
	portValue := os.Getenv("APP_PORT")

	if portValue == "" {
		return Config{
			Port: defaultPort,
		}, nil
	}

	// 环境变量是字符串，因此需要把端口转换成整数。
	port, err := strconv.Atoi(portValue)
	if err != nil {
		// %w 包装原始错误，在补充业务说明的同时保留错误链。
		return Config{}, fmt.Errorf(
			"APP_PORT must be an integer: %w",
			err,
		)
	}

	// 拒绝超出 TCP 有效范围的端口。
	if port < minPort || port > maxPort {
		return Config{}, fmt.Errorf(
			"APP_PORT must be between %d and %d",
			minPort,
			maxPort,
		)
	}

	return Config{
		Port: port,
	}, nil
}

// ServerAddress 把整数端口转换成 Gin Run 方法需要的监听地址。
// 例如 Port 为 9090 时，返回 ":9090"。
func (c Config) ServerAddress() string {
	return fmt.Sprintf(":%d", c.Port)
}
