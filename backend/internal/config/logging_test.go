package config

import (
	"log/slog"
	"testing"
)

func TestLoadLoggingUsesDefaults(t *testing.T) {
	t.Setenv("LOG_LEVEL", "")
	t.Setenv("LOG_FORMAT", "")

	loggingConfig, err := LoadLogging()
	if err != nil {
		t.Fatalf("LoadLogging() error = %v, want nil", err)
	}
	if loggingConfig.Level != slog.LevelInfo ||
		loggingConfig.Format != LogFormatJSON {
		t.Fatalf("LoadLogging() = %+v, want info/json defaults", loggingConfig)
	}
}

func TestLoadLoggingNormalizesEnvironmentValues(t *testing.T) {
	t.Setenv("LOG_LEVEL", " DEBUG ")
	t.Setenv("LOG_FORMAT", " TEXT ")

	loggingConfig, err := LoadLogging()
	if err != nil {
		t.Fatalf("LoadLogging() error = %v, want nil", err)
	}
	if loggingConfig.Level != slog.LevelDebug ||
		loggingConfig.Format != LogFormatText {
		t.Fatalf("LoadLogging() = %+v, want debug/text", loggingConfig)
	}
}

func TestLoadLoggingRejectsInvalidLevel(t *testing.T) {
	t.Setenv("LOG_LEVEL", "verbose")
	t.Setenv("LOG_FORMAT", "json")

	_, err := LoadLogging()
	if err == nil {
		t.Fatal("LoadLogging() error = nil, want invalid level error")
	}
}

func TestLoadLoggingRejectsInvalidFormat(t *testing.T) {
	t.Setenv("LOG_LEVEL", "info")
	t.Setenv("LOG_FORMAT", "yaml")

	_, err := LoadLogging()
	if err == nil {
		t.Fatal("LoadLogging() error = nil, want invalid format error")
	}
}
