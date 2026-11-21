package observability

import (
	"context"
	"log/slog"

	agentapplication "rag-reasoning-platform/backend/internal/application/agent"
)

type AgentModelCapacityLogger struct {
	logger *slog.Logger
}

var _ agentapplication.ModelCapacityEventObserver = (*AgentModelCapacityLogger)(nil)

func NewAgentModelCapacityLogger(logger *slog.Logger) *AgentModelCapacityLogger {
	if logger == nil {
		panic("NewAgentModelCapacityLogger requires a non-nil logger")
	}
	return &AgentModelCapacityLogger{logger: logger}
}

func (l *AgentModelCapacityLogger) ObserveModelCapacityEvent(
	ctx context.Context,
	event agentapplication.ModelCapacityEvent,
) {
	attributes := []slog.Attr{
		slog.String("event", safeAgentCapacityEventType(event.Type)),
		slog.String("outcome", safeAgentCapacityOutcome(event.Outcome)),
		slog.Int64("wait_duration_ms", event.WaitDuration.Milliseconds()),
		slog.Int64("execution_duration_ms", event.ExecutionDuration.Milliseconds()),
		slog.Int("in_flight", event.InFlight),
		slog.Int("max_concurrency", event.MaxConcurrency),
		slog.Int("owner_in_flight", event.OwnerInFlight),
		slog.Int("owner_max_concurrency", event.OwnerMaxConcurrency),
	}
	if event.ErrorCategory != agentapplication.ModelSafeErrorNone {
		attributes = append(attributes, slog.String("error_category", safeAgentModelErrorCategory(event.ErrorCategory)))
	}
	level := slog.LevelInfo
	if event.Type == agentapplication.ModelCapacityEventRejected ||
		event.Outcome == agentapplication.ModelCapacityOutcomeReleaseError {
		level = slog.LevelWarn
	}
	l.logger.LogAttrs(ctx, level, "Agent model capacity event", attributes...)
}

func safeAgentCapacityEventType(value agentapplication.ModelCapacityEventType) string {
	switch value {
	case agentapplication.ModelCapacityEventAdmitted,
		agentapplication.ModelCapacityEventRejected,
		agentapplication.ModelCapacityEventReleased:
		return string(value)
	default:
		return "agent_model_capacity_invalid"
	}
}

func safeAgentCapacityOutcome(value agentapplication.ModelCapacityOutcome) string {
	switch value {
	case agentapplication.ModelCapacityOutcomeSucceeded,
		agentapplication.ModelCapacityOutcomeCapacity,
		agentapplication.ModelCapacityOutcomeCoordination,
		agentapplication.ModelCapacityOutcomeCanceled,
		agentapplication.ModelCapacityOutcomeInvalidRequest,
		agentapplication.ModelCapacityOutcomeDownstreamError,
		agentapplication.ModelCapacityOutcomeReleaseError:
		return string(value)
	default:
		return "invalid"
	}
}

func safeAgentModelErrorCategory(value agentapplication.ModelSafeErrorCategory) string {
	switch value {
	case agentapplication.ModelSafeErrorTemporary,
		agentapplication.ModelSafeErrorRejected,
		agentapplication.ModelSafeErrorInvalid,
		agentapplication.ModelSafeErrorCanceled,
		agentapplication.ModelSafeErrorCoordination,
		agentapplication.ModelSafeErrorInternal:
		return string(value)
	default:
		return "internal"
	}
}
