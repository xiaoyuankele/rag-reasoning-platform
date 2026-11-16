package observability

import (
	"context"
	"log/slog"
	"time"
)

type AgentWorkerRunner interface {
	RunOnce(context.Context) (bool, error)
	RecoverExpiredRuns(context.Context) (int64, error)
}

type AgentWorkerLogger struct {
	next   AgentWorkerRunner
	logger *slog.Logger
}

func NewAgentWorkerLogger(next AgentWorkerRunner, logger *slog.Logger) *AgentWorkerLogger {
	if next == nil {
		panic("NewAgentWorkerLogger requires a non-nil Worker")
	}
	if logger == nil {
		panic("NewAgentWorkerLogger requires a non-nil logger")
	}
	return &AgentWorkerLogger{next: next, logger: logger}
}

func (l *AgentWorkerLogger) RunOnce(ctx context.Context) (bool, error) {
	startedAt := time.Now()
	handled, err := l.next.RunOnce(ctx)
	attributes := []slog.Attr{
		slog.String("event", "agent_worker_iteration"),
		slog.Bool("handled", handled),
		slog.Int64("duration_ms", time.Since(startedAt).Milliseconds()),
	}
	if err != nil {
		attributes = append(attributes, slog.String("error_category", safeAgentErrorCategory(err)))
	}
	l.logger.LogAttrs(ctx, safeAgentLogLevel(err), "Agent Worker iteration", attributes...)
	return handled, err
}

func (l *AgentWorkerLogger) RecoverExpiredRuns(ctx context.Context) (int64, error) {
	startedAt := time.Now()
	recovered, err := l.next.RecoverExpiredRuns(ctx)
	attributes := []slog.Attr{
		slog.String("event", "agent_worker_startup_recovery"),
		slog.Int64("recovered_count", recovered),
		slog.Int64("duration_ms", time.Since(startedAt).Milliseconds()),
	}
	if err != nil {
		attributes = append(attributes, slog.String("error_category", safeAgentErrorCategory(err)))
	}
	l.logger.LogAttrs(ctx, safeAgentLogLevel(err), "Agent Worker startup recovery", attributes...)
	return recovered, err
}
