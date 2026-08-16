package password

import (
	"errors"
	"strings"
	"testing"
)

func TestArgon2idHasherHashAndVerify(t *testing.T) {
	hasher, err := NewArgon2idHasher(DefaultParameters())
	if err != nil {
		t.Fatalf("NewArgon2idHasher() error = %v, want nil", err)
	}

	firstHash, err := hasher.Hash("Example123")
	if err != nil {
		t.Fatalf("first Hash() error = %v, want nil", err)
	}
	secondHash, err := hasher.Hash("Example123")
	if err != nil {
		t.Fatalf("second Hash() error = %v, want nil", err)
	}

	if !strings.HasPrefix(firstHash, "$argon2id$v=19$") {
		t.Fatalf("Hash() = %q, want Argon2id encoded prefix", firstHash)
	}
	if firstHash == secondHash {
		t.Fatal("two password hashes are equal, want independent random salts")
	}

	matches, err := hasher.Verify("Example123", firstHash)
	if err != nil {
		t.Fatalf("Verify() error = %v, want nil", err)
	}
	if !matches {
		t.Fatal("Verify() = false for correct password")
	}

	matches, err = hasher.Verify("Wrong123", firstHash)
	if err != nil {
		t.Fatalf("Verify() wrong-password error = %v, want nil", err)
	}
	if matches {
		t.Fatal("Verify() = true for wrong password")
	}
}

func TestArgon2idHasherRejectsMalformedOrUnsafeHash(t *testing.T) {
	hasher, err := NewArgon2idHasher(DefaultParameters())
	if err != nil {
		t.Fatalf("NewArgon2idHasher() error = %v, want nil", err)
	}

	for _, encodedHash := range []string{
		"not-an-argon2-hash",
		"$argon2id$v=19$m=999999,t=2,p=1$c2FsdHNhbHRzYWx0c2FsdA$a2V5a2V5a2V5a2V5a2V5a2V5a2V5a2V5a2V5a2V5a2U",
	} {
		_, err := hasher.Verify("Example123", encodedHash)
		if !errors.Is(err, ErrInvalidEncodedHash) {
			t.Fatalf("Verify(%q) error = %v, want invalid encoded hash", encodedHash, err)
		}
	}
}

func TestNewArgon2idHasherRejectsUnsafeParameters(t *testing.T) {
	parameters := DefaultParameters()
	parameters.MemoryKiB = 1024

	_, err := NewArgon2idHasher(parameters)
	if !errors.Is(err, ErrInvalidParameters) {
		t.Fatalf("error = %v, want ErrInvalidParameters", err)
	}
}
