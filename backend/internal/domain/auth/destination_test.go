package auth

import (
	"errors"
	"testing"
)

func TestNormalizeVerificationDestination(t *testing.T) {
	testCases := []struct {
		name        string
		channel     VerificationChannel
		destination string
		want        string
		wantErr     error
	}{
		{
			name:        "email is trimmed and lowercased",
			channel:     VerificationChannelEmail,
			destination: "  Owner@Example.COM  ",
			want:        "owner@example.com",
		},
		{
			name:        "valid E164 phone is preserved",
			channel:     VerificationChannelSMS,
			destination: "+8613800000000",
			want:        "+8613800000000",
		},
		{
			name:        "display name email syntax is rejected",
			channel:     VerificationChannelEmail,
			destination: "Owner <owner@example.com>",
			wantErr:     ErrInvalidVerificationDestination,
		},
		{
			name:        "local phone is rejected",
			channel:     VerificationChannelSMS,
			destination: "13800000000",
			wantErr:     ErrInvalidVerificationDestination,
		},
		{
			name:        "unknown channel is rejected",
			channel:     VerificationChannel("push"),
			destination: "owner@example.com",
			wantErr:     ErrInvalidVerificationChannel,
		},
	}

	for _, testCase := range testCases {
		t.Run(testCase.name, func(t *testing.T) {
			actual, err := NormalizeVerificationDestination(
				testCase.channel,
				testCase.destination,
			)
			if !errors.Is(err, testCase.wantErr) {
				t.Fatalf("error = %v, want %v", err, testCase.wantErr)
			}
			if actual != testCase.want {
				t.Fatalf("destination = %q, want %q", actual, testCase.want)
			}
		})
	}
}
