package verification

import (
	"errors"
	"testing"

	authdomain "rag-reasoning-platform/backend/internal/domain/auth"
)

func TestHMACCodeHasher(t *testing.T) {
	hasher, err := NewHMACCodeHasher(
		[]byte("0123456789abcdef0123456789abcdef"),
	)
	if err != nil {
		t.Fatalf("NewHMACCodeHasher() error = %v, want nil", err)
	}

	hash := hasher.Hash(
		authdomain.VerificationChannelEmail,
		"owner@example.com",
		authdomain.VerificationPurposeRegister,
		"123456",
	)
	if len(hash) != 64 {
		t.Fatalf("Hash() length = %d, want 64", len(hash))
	}
	if !hasher.Matches(
		hash,
		authdomain.VerificationChannelEmail,
		"owner@example.com",
		authdomain.VerificationPurposeRegister,
		"123456",
	) {
		t.Fatal("Matches() = false for the original code")
	}
	if hasher.Matches(
		hash,
		authdomain.VerificationChannelEmail,
		"owner@example.com",
		authdomain.VerificationPurposeRegister,
		"654321",
	) {
		t.Fatal("Matches() = true for a different code")
	}
	if hasher.Matches(
		hash,
		authdomain.VerificationChannelEmail,
		"another@example.com",
		authdomain.VerificationPurposeRegister,
		"123456",
	) {
		t.Fatal("Matches() = true for a different destination")
	}
	if hasher.Matches(
		"not-hex",
		authdomain.VerificationChannelEmail,
		"owner@example.com",
		authdomain.VerificationPurposeRegister,
		"123456",
	) {
		t.Fatal("Matches() = true for malformed stored hash")
	}
}

func TestNewHMACCodeHasherRejectsShortSecret(t *testing.T) {
	_, err := NewHMACCodeHasher([]byte("too-short"))
	if !errors.Is(err, ErrHMACSecretTooShort) {
		t.Fatalf("error = %v, want ErrHMACSecretTooShort", err)
	}
}
