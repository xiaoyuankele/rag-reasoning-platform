package auth

import (
	"testing"
	"time"
)

func TestVerificationTypesAreValid(t *testing.T) {
	if !VerificationChannelEmail.IsValid() {
		t.Fatal("email verification channel should be valid")
	}
	if !VerificationChannelSMS.IsValid() {
		t.Fatal("sms verification channel should be valid")
	}
	if VerificationChannel("push").IsValid() {
		t.Fatal("unknown verification channel should be invalid")
	}

	if !VerificationPurposeRegister.IsValid() {
		t.Fatal("register verification purpose should be valid")
	}
	if VerificationPurpose("login").IsValid() {
		t.Fatal("unknown verification purpose should be invalid")
	}
}

func TestVerificationChallengeCanAttempt(t *testing.T) {
	now := time.Date(2026, time.August, 16, 8, 0, 0, 0, time.UTC)
	consumedAt := now.Add(-time.Minute)

	testCases := []struct {
		name      string
		challenge VerificationChallenge
		want      bool
	}{
		{
			name: "open challenge can be attempted",
			challenge: VerificationChallenge{
				Channel:      VerificationChannelEmail,
				Purpose:      VerificationPurposeRegister,
				ExpiresAt:    now.Add(time.Minute),
				AttemptCount: MaxVerificationAttempts - 1,
			},
			want: true,
		},
		{
			name: "expired challenge cannot be attempted",
			challenge: VerificationChallenge{
				Channel:   VerificationChannelEmail,
				Purpose:   VerificationPurposeRegister,
				ExpiresAt: now,
			},
			want: false,
		},
		{
			name: "consumed challenge cannot be attempted",
			challenge: VerificationChallenge{
				Channel:    VerificationChannelEmail,
				Purpose:    VerificationPurposeRegister,
				ExpiresAt:  now.Add(time.Minute),
				ConsumedAt: &consumedAt,
			},
			want: false,
		},
		{
			name: "attempt limit blocks challenge",
			challenge: VerificationChallenge{
				Channel:      VerificationChannelEmail,
				Purpose:      VerificationPurposeRegister,
				ExpiresAt:    now.Add(time.Minute),
				AttemptCount: MaxVerificationAttempts,
			},
			want: false,
		},
		{
			name: "invalid channel blocks challenge",
			challenge: VerificationChallenge{
				Channel:   VerificationChannel("push"),
				Purpose:   VerificationPurposeRegister,
				ExpiresAt: now.Add(time.Minute),
			},
			want: false,
		},
		{
			name: "negative attempt count blocks challenge",
			challenge: VerificationChallenge{
				Channel:      VerificationChannelEmail,
				Purpose:      VerificationPurposeRegister,
				ExpiresAt:    now.Add(time.Minute),
				AttemptCount: -1,
			},
			want: false,
		},
	}

	for _, testCase := range testCases {
		t.Run(testCase.name, func(t *testing.T) {
			if actual := testCase.challenge.CanAttempt(now); actual != testCase.want {
				t.Fatalf("CanAttempt() = %t, want %t", actual, testCase.want)
			}
		})
	}
}
