package user

import (
	"errors"
	"strings"
	"testing"
)

func TestNormalizeDisplayName(t *testing.T) {
	tests := []struct {
		name        string
		input       string
		wanted      string
		wantedError error
	}{
		{name: "trims whitespace", input: "  Example User  ", wanted: "Example User"},
		{name: "accepts Chinese", input: "研究用户", wanted: "研究用户"},
		{name: "rejects blank", input: "  ", wantedError: ErrInvalidDisplayName},
		{name: "rejects over one hundred characters", input: strings.Repeat("文", 101), wantedError: ErrInvalidDisplayName},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			actual, err := NormalizeDisplayName(test.input)
			if !errors.Is(err, test.wantedError) {
				t.Fatalf("NormalizeDisplayName() error = %v, want %v", err, test.wantedError)
			}
			if actual != test.wanted {
				t.Fatalf("NormalizeDisplayName() = %q, want %q", actual, test.wanted)
			}
		})
	}
}
