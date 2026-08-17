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
		verificationConfig.SMTPHost != defaultVerificationSMTPHost ||
		verificationConfig.SMTPPort != defaultVerificationSMTPPort ||
		verificationConfig.SMTPFromAddress != defaultVerificationSMTPFromAddress ||
		verificationConfig.SMTPFromName != defaultVerificationSMTPFromName ||
		verificationConfig.SMTPTimeout != defaultVerificationSMTPTimeout ||
		verificationConfig.RateLimitWindow != time.Minute ||
		verificationConfig.PerClientLimit != 5 ||
		verificationConfig.GlobalLimit != 100 {
		t.Fatalf("LoadVerification() = %+v, want safe defaults", verificationConfig)
	}
}

func TestLoadVerificationUsesMailpitSMTPEnvironment(t *testing.T) {
	clearVerificationEnvironment(t)
	t.Setenv("VERIFICATION_HMAC_SECRET", testVerificationHMACSecret)
	t.Setenv("VERIFICATION_SENDER", " MAILPIT ")
	t.Setenv("VERIFICATION_SMTP_HOST", "mailpit")
	t.Setenv("VERIFICATION_SMTP_PORT", "2025")
	t.Setenv("VERIFICATION_SMTP_FROM_ADDRESS", "verify@example.test")
	t.Setenv("VERIFICATION_SMTP_FROM_NAME", "Local Verification")
	t.Setenv("VERIFICATION_SMTP_TIMEOUT", "7s")

	verificationConfig, err := LoadVerification()
	if err != nil {
		t.Fatalf("LoadVerification() error = %v, want nil", err)
	}
	if verificationConfig.Sender != VerificationSenderMailpit ||
		verificationConfig.SMTPHost != "mailpit" ||
		verificationConfig.SMTPPort != 2025 ||
		verificationConfig.SMTPFromAddress != "verify@example.test" ||
		verificationConfig.SMTPFromName != "Local Verification" ||
		verificationConfig.SMTPTimeout != 7*time.Second {
		t.Fatalf("LoadVerification() = %+v, want Mailpit SMTP values", verificationConfig)
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

func TestLoadVerificationRejectsInvalidMailpitSMTPConfiguration(t *testing.T) {
	for _, test := range []struct {
		name      string
		configure func(t *testing.T)
	}{
		{
			name: "invalid host",
			configure: func(t *testing.T) {
				t.Setenv("VERIFICATION_SMTP_HOST", "mailpit\r\ninvalid")
			},
		},
		{
			name: "invalid port",
			configure: func(t *testing.T) {
				t.Setenv("VERIFICATION_SMTP_PORT", "70000")
			},
		},
		{
			name: "invalid from address",
			configure: func(t *testing.T) {
				t.Setenv("VERIFICATION_SMTP_FROM_ADDRESS", "RAG <verify@example.test>")
			},
		},
		{
			name: "invalid from name",
			configure: func(t *testing.T) {
				t.Setenv("VERIFICATION_SMTP_FROM_NAME", "RAG\r\nBcc: attacker@example.test")
			},
		},
		{
			name: "invalid timeout",
			configure: func(t *testing.T) {
				t.Setenv("VERIFICATION_SMTP_TIMEOUT", "0s")
			},
		},
	} {
		t.Run(test.name, func(t *testing.T) {
			clearVerificationEnvironment(t)
			t.Setenv("VERIFICATION_HMAC_SECRET", testVerificationHMACSecret)
			t.Setenv("VERIFICATION_SENDER", VerificationSenderMailpit)
			test.configure(t)

			_, err := LoadVerification()
			if !errors.Is(err, ErrInvalidVerificationSMTPConfiguration) {
				t.Fatalf(
					"LoadVerification() error = %v, want %v",
					err,
					ErrInvalidVerificationSMTPConfiguration,
				)
			}
		})
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
		"VERIFICATION_SMTP_HOST",
		"VERIFICATION_SMTP_PORT",
		"VERIFICATION_SMTP_FROM_ADDRESS",
		"VERIFICATION_SMTP_FROM_NAME",
		"VERIFICATION_SMTP_TIMEOUT",
		"VERIFICATION_RATE_LIMIT_WINDOW",
		"VERIFICATION_PER_CLIENT_LIMIT",
		"VERIFICATION_GLOBAL_LIMIT",
	} {
		t.Setenv(name, "")
	}
}
