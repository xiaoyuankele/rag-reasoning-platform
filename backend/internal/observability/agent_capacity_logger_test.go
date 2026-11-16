package observability

import (
	"bytes"
	"context"
	"encoding/json"
	"log/slog"
	"strings"
	"sync"
	"testing"
	"time"

	agentapplication "rag-reasoning-platform/backend/internal/application/agent"
)

type lockedAgentLogBuffer struct {
	mu     sync.Mutex
	buffer bytes.Buffer
}

func (b *lockedAgentLogBuffer) Write(value []byte) (int, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.buffer.Write(value)
}

func (b *lockedAgentLogBuffer) String() string {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.buffer.String()
}

func TestAgentModelCapacityLoggerWritesOnlySafeAnonymousFields(t *testing.T) {
	var output lockedAgentLogBuffer
	logger := NewAgentModelCapacityLogger(slog.New(slog.NewJSONHandler(&output, nil)))
	ctx := context.WithValue(t.Context(), struct{}{}, "owner-999 prompt-secret api-key-secret")
	logger.ObserveModelCapacityEvent(ctx, agentapplication.ModelCapacityEvent{
		Type:         agentapplication.ModelCapacityEventReleased,
		Outcome:      agentapplication.ModelCapacityOutcomeReleaseError,
		WaitDuration: 25 * time.Millisecond, ExecutionDuration: 1500 * time.Millisecond,
		InFlight: 8, MaxConcurrency: 10, OwnerInFlight: 1, OwnerMaxConcurrency: 2,
		ErrorCategory: agentapplication.ModelSafeErrorCoordination,
	})

	line := strings.TrimSpace(output.String())
	var entry map[string]any
	if err := json.Unmarshal([]byte(line), &entry); err != nil {
		t.Fatalf("decode capacity log: %v; %s", err, line)
	}
	assertStringLogField(t, entry, "level", "WARN")
	assertStringLogField(t, entry, "event", string(agentapplication.ModelCapacityEventReleased))
	assertStringLogField(t, entry, "outcome", string(agentapplication.ModelCapacityOutcomeReleaseError))
	assertStringLogField(t, entry, "error_category", string(agentapplication.ModelSafeErrorCoordination))
	assertNumericLogField(t, entry, "wait_duration_ms", 25)
	assertNumericLogField(t, entry, "execution_duration_ms", 1500)
	for _, forbidden := range []string{
		"owner-999", "prompt-secret", "api-key-secret", "owner_user_id", "query", "answer",
		"arguments", "result", "evidence", "provider_body", `"error":`,
	} {
		if strings.Contains(line, forbidden) {
			t.Fatalf("capacity log leaked %q: %s", forbidden, line)
		}
	}
}

func TestAgentModelCapacityLoggerIsConcurrentSafe(t *testing.T) {
	var output lockedAgentLogBuffer
	logger := NewAgentModelCapacityLogger(slog.New(slog.NewJSONHandler(&output, nil)))
	var workers sync.WaitGroup
	for range 32 {
		workers.Add(1)
		go func() {
			defer workers.Done()
			logger.ObserveModelCapacityEvent(t.Context(), agentapplication.ModelCapacityEvent{
				Type:           agentapplication.ModelCapacityEventAdmitted,
				Outcome:        agentapplication.ModelCapacityOutcomeSucceeded,
				MaxConcurrency: 10, OwnerMaxConcurrency: 2,
			})
		}()
	}
	workers.Wait()
	lines := strings.Split(strings.TrimSpace(output.String()), "\n")
	if len(lines) != 32 {
		t.Fatalf("capacity log lines = %d, want 32", len(lines))
	}
	for _, line := range lines {
		var entry map[string]any
		if err := json.Unmarshal([]byte(line), &entry); err != nil {
			t.Fatalf("invalid concurrent log: %v; %s", err, line)
		}
	}
}

func TestAgentModelCapacityLoggerSanitizesUnknownEnumValues(t *testing.T) {
	var output lockedAgentLogBuffer
	logger := NewAgentModelCapacityLogger(slog.New(slog.NewJSONHandler(&output, nil)))
	logger.ObserveModelCapacityEvent(t.Context(), agentapplication.ModelCapacityEvent{
		Type: "owner-secret", Outcome: "prompt-secret", ErrorCategory: "api-key-secret",
	})
	line := output.String()
	if strings.Contains(line, "owner-secret") || strings.Contains(line, "prompt-secret") ||
		strings.Contains(line, "api-key-secret") || !strings.Contains(line, `"event":"agent_model_capacity_invalid"`) ||
		!strings.Contains(line, `"outcome":"invalid"`) || !strings.Contains(line, `"error_category":"internal"`) {
		t.Fatalf("sanitized capacity log = %s", line)
	}
}
