package access

import (
	"errors"
	"testing"
)

func TestNewOwnerScopeAcceptsPositiveUserID(t *testing.T) {
	scope, err := NewOwnerScope(42)
	if err != nil {
		t.Fatalf("NewOwnerScope() error = %v, want nil", err)
	}
	if !scope.IsValid() {
		t.Fatal("NewOwnerScope() returned an invalid scope")
	}
	if scope.OwnerUserID() != 42 {
		t.Fatalf("OwnerUserID() = %d, want 42", scope.OwnerUserID())
	}
}

func TestNewOwnerScopeRejectsNonPositiveUserID(t *testing.T) {
	tests := []struct {
		name   string
		userID int64
	}{
		{name: "zero user ID", userID: 0},
		{name: "negative user ID", userID: -1},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			scope, err := NewOwnerScope(test.userID)
			if !errors.Is(err, ErrInvalidOwnerScope) {
				t.Fatalf("NewOwnerScope() error = %v, want %v", err, ErrInvalidOwnerScope)
			}
			if scope.IsValid() {
				t.Fatalf("NewOwnerScope() scope = %+v, want invalid zero value", scope)
			}
		})
	}
}

func TestOwnerScopeZeroValueIsInvalid(t *testing.T) {
	var scope OwnerScope
	if scope.IsValid() {
		t.Fatal("zero-value OwnerScope must be invalid")
	}
	if scope.OwnerUserID() != 0 {
		t.Fatalf("zero-value OwnerUserID() = %d, want 0", scope.OwnerUserID())
	}
}
