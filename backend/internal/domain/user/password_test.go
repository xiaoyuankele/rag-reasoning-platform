package user

import (
	"errors"
	"strings"
	"testing"
)

func TestValidatePassword(t *testing.T) {
	testCases := []struct {
		name     string
		password string
		wantErr  error
	}{
		{
			name:     "valid password",
			password: "Example123",
		},
		{
			name:     "too short",
			password: "Abc123",
			wantErr:  ErrPasswordTooShort,
		},
		{
			name:     "too long",
			password: "Aa1" + strings.Repeat("a", MaxPasswordBytes),
			wantErr:  ErrPasswordTooLong,
		},
		{
			name:     "symbol is rejected",
			password: "Example1!",
			wantErr:  ErrPasswordInvalidCharacter,
		},
		{
			name:     "space is rejected",
			password: "Example 123",
			wantErr:  ErrPasswordInvalidCharacter,
		},
		{
			name:     "unicode is rejected",
			password: "Example密码1",
			wantErr:  ErrPasswordInvalidCharacter,
		},
		{
			name:     "uppercase is required",
			password: "example123",
			wantErr:  ErrPasswordMissingUppercase,
		},
		{
			name:     "lowercase is required",
			password: "EXAMPLE123",
			wantErr:  ErrPasswordMissingLowercase,
		},
		{
			name:     "digit is required",
			password: "ExampleOnly",
			wantErr:  ErrPasswordMissingDigit,
		},
	}

	for _, testCase := range testCases {
		t.Run(testCase.name, func(t *testing.T) {
			err := ValidatePassword(testCase.password)
			if !errors.Is(err, testCase.wantErr) {
				t.Fatalf("ValidatePassword() error = %v, want %v", err, testCase.wantErr)
			}
		})
	}
}
