package config

import (
	"errors"
	"testing"
	"time"
)

const testVerificationHMACSecret = "0123456789abcdef0123456789abcdef"

func TestLoadVerificationUsesSafeDefaults(t *testing.T) {
	clearVerificationEnvironment(t)
	t.Setenv("VERIFICATION_HMAC_SECRET", testVerificationHMACSecret)

	verificationConfig, err := LoadVerification()
	if err != nil {
		t.Fatalf("LoadVerification() error = %v, want nil", err)
	}
	if verificationConfig.HMACSecret != testVerificationHMACSecret ||
		verificationConfig.Sender != VerificationSenderFake ||
		verificationConfig.RateLimitWindow != time.Minute ||
		verificationConfig.PerClientLimit != 5 ||
		verificationConfig.GlobalLimit != 100 {
		t.Fatalf("LoadVerification() = %+v, want safe defaults", verificationConfig)
	}
}

func TestLoadVerificationUsesEnvironment(t *testing.T) {
	clearVerificationEnvironment(t)
	t.Setenv("VERIFICATION_HMAC_SECRET", "  "+testVerificationHMACSecret+"  ")
	t.Setenv("VERIFICATION_SENDER", " FAKE ")
	t.Setenv("VERIFICATION_RATE_LIMIT_WINDOW", "2m")
	t.Setenv("VERIFICATION_PER_CLIENT_LIMIT", "7")
	t.Setenv("VERIFICATION_GLOBAL_LIMIT", "200")

	verificationConfig, err := LoadVerification()
	if err != nil {
		t.Fatalf("LoadVerification() error = %v, want nil", err)
	}
	if verificationConfig.HMACSecret != testVerificationHMACSecret ||
		verificationConfig.Sender != VerificationSenderFake ||
		verificationConfig.RateLimitWindow != 2*time.Minute ||
		verificationConfig.PerClientLimit != 7 ||
		verificationConfig.GlobalLimit != 200 {
		t.Fatalf("LoadVerification() = %+v, want environment values", verificationConfig)
	}
}

func TestLoadVerificationRejectsInvalidSecurityConfiguration(t *testing.T) {
	tests := []struct {
		name        string
		configure   func(t *testing.T)
		wantedError error
	}{
		{
			name:        "missing secret",
			configure:   func(t *testing.T) {},
			wantedError: ErrVerificationHMACSecretRequired,
		},
		{
			name: "short secret",
			configure: func(t *testing.T) {
				t.Setenv("VERIFICATION_HMAC_SECRET", "too-short")
			},
			wantedError: ErrVerificationHMACSecretTooShort,
		},
		{
			name: "unsupported sender",
			configure: func(t *testing.T) {
				t.Setenv("VERIFICATION_HMAC_SECRET", testVerificationHMACSecret)
				t.Setenv("VERIFICATION_SENDER", "sms")
			},
			wantedError: ErrInvalidVerificationSender,
		},
		{
			name: "global below client",
			configure: func(t *testing.T) {
				t.Setenv("VERIFICATION_HMAC_SECRET", testVerificationHMACSecret)
				t.Setenv("VERIFICATION_PER_CLIENT_LIMIT", "10")
				t.Setenv("VERIFICATION_GLOBAL_LIMIT", "9")
			},
			wantedError: ErrInvalidVerificationRateLimits,
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			clearVerificationEnvironment(t)
			test.configure(t)

			_, err := LoadVerification()
			if !errors.Is(err, test.wantedError) {
				t.Fatalf("LoadVerification() error = %v, want %v", err, test.wantedError)
			}
		})
	}
}

func TestLoadVerificationRejectsInvalidRateLimitValues(t *testing.T) {
	for _, test := range []struct {
		name  string
		value string
	}{
		{name: "invalid window", value: "never"},
		{name: "zero client limit", value: "0"},
		{name: "zero global limit", value: "0"},
	} {
		t.Run(test.name, func(t *testing.T) {
			clearVerificationEnvironment(t)
			t.Setenv("VERIFICATION_HMAC_SECRET", testVerificationHMACSecret)

			switch test.name {
			case "invalid window":
				t.Setenv("VERIFICATION_RATE_LIMIT_WINDOW", test.value)
			case "zero client limit":
				t.Setenv("VERIFICATION_PER_CLIENT_LIMIT", test.value)
			case "zero global limit":
				t.Setenv("VERIFICATION_GLOBAL_LIMIT", test.value)
			}

			if _, err := LoadVerification(); err == nil {
				t.Fatal("LoadVerification() error = nil, want invalid rate-limit error")
			}
		})
	}
}

func clearVerificationEnvironment(t *testing.T) {
	t.Helper()

	for _, name := range []string{
		"VERIFICATION_HMAC_SECRET",
		"VERIFICATION_SENDER",
		"VERIFICATION_RATE_LIMIT_WINDOW",
		"VERIFICATION_PER_CLIENT_LIMIT",
		"VERIFICATION_GLOBAL_LIMIT",
	} {
		t.Setenv(name, "")
	}
}
