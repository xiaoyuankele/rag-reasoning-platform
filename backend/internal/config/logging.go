package config

import (
	"fmt"
	"log/slog"
	"os"
	"strings"
)

const (
	defaultLogLevel  = slog.LevelInfo
	defaultLogFormat = LogFormatJSON
)

// LogFormat 是后端支持的稳定日志输出格式。
type LogFormat string

const (
	// LogFormatJSON 适合文件、容器与 observability-report 自动分析。
	LogFormatJSON LogFormat = "json"

	// LogFormatText 适合开发者在终端直接阅读。
	LogFormatText LogFormat = "text"
)

// LoggingConfig 保存应用启动时使用的日志级别和输出格式。
type LoggingConfig struct {
	Level  slog.Level
	Format LogFormat
}

// LoadLogging 从环境变量读取并校验日志配置。
//
// LOG_LEVEL 支持 debug、info、warn、error；LOG_FORMAT 支持 json、text。
// 输入会去除首尾空白并转成小写，方便 Windows PowerShell 配置。
func LoadLogging() (LoggingConfig, error) {
	level, err := parseLogLevel(os.Getenv("LOG_LEVEL"))
	if err != nil {
		return LoggingConfig{}, err
	}

	format, err := parseLogFormat(os.Getenv("LOG_FORMAT"))
	if err != nil {
		return LoggingConfig{}, err
	}

	return LoggingConfig{
		Level:  level,
		Format: format,
	}, nil
}

func parseLogLevel(rawValue string) (slog.Level, error) {
	value := strings.ToLower(strings.TrimSpace(rawValue))
	if value == "" {
		return defaultLogLevel, nil
	}

	switch value {
	case "debug":
		return slog.LevelDebug, nil
	case "info":
		return slog.LevelInfo, nil
	case "warn":
		return slog.LevelWarn, nil
	case "error":
		return slog.LevelError, nil
	default:
		return 0, fmt.Errorf(
			"LOG_LEVEL must be one of debug, info, warn, error: %q",
			rawValue,
		)
	}
}

func parseLogFormat(rawValue string) (LogFormat, error) {
	value := strings.ToLower(strings.TrimSpace(rawValue))
	if value == "" {
		return defaultLogFormat, nil
	}

	switch LogFormat(value) {
	case LogFormatJSON:
		return LogFormatJSON, nil
	case LogFormatText:
		return LogFormatText, nil
	default:
		return "", fmt.Errorf(
			"LOG_FORMAT must be one of json, text: %q",
			rawValue,
		)
	}
}
