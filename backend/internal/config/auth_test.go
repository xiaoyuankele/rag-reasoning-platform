package config

import (
	"testing"
	"time"
)

func TestLoadAuthUsesDefaults(t *testing.T) {
	clearAuthEnvironment(t)

	actual, err := LoadAuth()
	if err != nil {
		t.Fatalf("LoadAuth() error = %v", err)
	}
	if actual.SessionTTL != 7*24*time.Hour || actual.CookieSecure ||
		actual.RateLimitWindow != time.Minute || actual.PerClientLimit != 10 ||
		actual.GlobalLimit != 200 {
		t.Fatalf("LoadAuth() = %+v, want development defaults", actual)
	}
}

func TestLoadAuthUsesEnvironment(t *testing.T) {
	clearAuthEnvironment(t)
	t.Setenv("AUTH_SESSION_TTL", "24h")
	t.Setenv("AUTH_COOKIE_SECURE", "true")
	t.Setenv("AUTH_RATE_LIMIT_WINDOW", "2m")
	t.Setenv("AUTH_PER_CLIENT_LIMIT", "4")
	t.Setenv("AUTH_GLOBAL_LIMIT", "40")

	actual, err := LoadAuth()
	if err != nil {
		t.Fatalf("LoadAuth() error = %v", err)
	}
	if actual.SessionTTL != 24*time.Hour || !actual.CookieSecure ||
		actual.RateLimitWindow != 2*time.Minute || actual.PerClientLimit != 4 ||
		actual.GlobalLimit != 40 {
		t.Fatalf("LoadAuth() = %+v, want environment values", actual)
	}
}

func TestLoadAuthRejectsInvalidValues(t *testing.T) {
	for _, test := range []struct {
		name     string
		variable string
		value    string
	}{
		{name: "invalid TTL", variable: "AUTH_SESSION_TTL", value: "forever"},
		{name: "invalid secure flag", variable: "AUTH_COOKIE_SECURE", value: "sometimes"},
		{name: "zero client limit", variable: "AUTH_PER_CLIENT_LIMIT", value: "0"},
		{name: "global below client", variable: "AUTH_GLOBAL_LIMIT", value: "1"},
	} {
		t.Run(test.name, func(t *testing.T) {
			clearAuthEnvironment(t)
			t.Setenv(test.variable, test.value)
			if _, err := LoadAuth(); err == nil {
				t.Fatal("LoadAuth() error = nil, want invalid configuration error")
			}
		})
	}
}

func clearAuthEnvironment(t *testing.T) {
	t.Helper()
	for _, name := range []string{
		"AUTH_SESSION_TTL",
		"AUTH_COOKIE_SECURE",
		"AUTH_RATE_LIMIT_WINDOW",
		"AUTH_PER_CLIENT_LIMIT",
		"AUTH_GLOBAL_LIMIT",
	} {
		t.Setenv(name, "")
	}
}
