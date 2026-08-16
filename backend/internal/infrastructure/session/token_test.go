package session

import (
	"crypto/sha256"
	"encoding/hex"
	"strings"
	"testing"
)

func TestTokenGeneratorCreatesDistinctTokenAndMatchingHash(t *testing.T) {
	generator := NewTokenGenerator()

	first, err := generator.Generate()
	if err != nil {
		t.Fatalf("Generate() first error = %v", err)
	}
	second, err := generator.Generate()
	if err != nil {
		t.Fatalf("Generate() second error = %v", err)
	}

	if first.Raw == "" || first.Hash == "" || first.Raw == first.Hash {
		t.Fatalf("Generate() first = %+v, want separated raw token and hash", first)
	}
	if first.Raw == second.Raw || first.Hash == second.Hash {
		t.Fatal("Generate() produced duplicate session tokens")
	}
	if len(first.Hash) != 64 || strings.ToLower(first.Hash) != first.Hash {
		t.Fatalf("Generate() hash = %q, want 64 lowercase hex characters", first.Hash)
	}

	expected := sha256.Sum256([]byte(first.Raw))
	if first.Hash != hex.EncodeToString(expected[:]) {
		t.Fatalf("Generate() hash = %q, want SHA-256 of raw token", first.Hash)
	}
}
