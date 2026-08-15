package main

import (
	"bytes"
	"encoding/json"
	"log/slog"
	"strings"
	"testing"

	"rag-reasoning-platform/backend/internal/config"
)

func TestNewApplicationLoggerUsesJSONAndFiltersDebug(t *testing.T) {
	var output bytes.Buffer
	logger := newApplicationLogger(&output, config.LoggingConfig{
		Level:  slog.LevelInfo,
		Format: config.LogFormatJSON,
	})

	logger.Debug("hidden debug message", "event", "debug_event")
	logger.Info("visible info message", "event", "info_event")

	lines := strings.Split(strings.TrimSpace(output.String()), "\n")
	if len(lines) != 1 {
		t.Fatalf("log line count = %d, want 1; output = %q", len(lines), output.String())
	}
	var entry map[string]any
	if err := json.Unmarshal([]byte(lines[0]), &entry); err != nil {
		t.Fatalf("decode JSON log: %v; output = %q", err, output.String())
	}
	if entry["level"] != "INFO" || entry["event"] != "info_event" {
		t.Fatalf("JSON entry = %+v, want INFO info_event", entry)
	}
}

func TestNewApplicationLoggerUsesTextAndAllowsDebug(t *testing.T) {
	var output bytes.Buffer
	logger := newApplicationLogger(&output, config.LoggingConfig{
		Level:  slog.LevelDebug,
		Format: config.LogFormatText,
	})

	logger.Debug("visible debug message", "event", "debug_event")

	logLine := output.String()
	if !strings.Contains(logLine, "level=DEBUG") ||
		!strings.Contains(logLine, "event=debug_event") {
		t.Fatalf("text log = %q, want DEBUG event fields", logLine)
	}
}
