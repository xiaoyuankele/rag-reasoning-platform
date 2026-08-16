package auth

import (
	"errors"
	"testing"
)

func TestNormalizeLoginIdentifier(t *testing.T) {
	tests := []struct {
		name        string
		input       string
		wanted      string
		wantedError error
	}{
		{name: "normalizes email", input: "  User@Example.COM ", wanted: "user@example.com"},
		{name: "accepts E164 phone", input: " +8613812345678 ", wanted: "+8613812345678"},
		{name: "rejects ordinary text", input: "example-user", wantedError: ErrInvalidLoginIdentifier},
		{name: "rejects invalid email", input: "user@", wantedError: ErrInvalidLoginIdentifier},
		{name: "rejects invalid phone", input: "+0123", wantedError: ErrInvalidLoginIdentifier},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			actual, err := NormalizeLoginIdentifier(test.input)
			if !errors.Is(err, test.wantedError) {
				t.Fatalf("NormalizeLoginIdentifier() error = %v, want %v", err, test.wantedError)
			}
			if actual != test.wanted {
				t.Fatalf("NormalizeLoginIdentifier() = %q, want %q", actual, test.wanted)
			}
		})
	}
}
