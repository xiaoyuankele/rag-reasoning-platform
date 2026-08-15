package main

import (
	"io"
	"log/slog"

	"rag-reasoning-platform/backend/internal/config"
)

// newApplicationLogger 根据已校验配置创建应用 Logger。
//
// Logger 仍然由 main 组合根创建并注入其他层；业务代码不读取环境变量，
// 也不需要知道当前使用 JSONHandler 还是 TextHandler。
func newApplicationLogger(
	output io.Writer,
	loggingConfig config.LoggingConfig,
) *slog.Logger {
	options := &slog.HandlerOptions{Level: loggingConfig.Level}

	var handler slog.Handler
	if loggingConfig.Format == config.LogFormatText {
		handler = slog.NewTextHandler(output, options)
	} else {
		handler = slog.NewJSONHandler(output, options)
	}
	return slog.New(handler)
}
