package postgres

import (
	"strings"
	"testing"
	"unicode/utf8"

	authdomain "rag-reasoning-platform/backend/internal/domain/auth"
)

func TestVerificationAdvisoryLockKeyProducesValidUnambiguousText(t *testing.T) {
	t.Parallel()

	firstKey := verificationAdvisoryLockKey(
		authdomain.VerificationChannel("ab"),
		"c",
		authdomain.VerificationPurpose("d"),
	)
	secondKey := verificationAdvisoryLockKey(
		authdomain.VerificationChannel("a"),
		"bc",
		authdomain.VerificationPurpose("d"),
	)
	unicodeKey := verificationAdvisoryLockKey(
		authdomain.VerificationChannelEmail,
		"用户@example.com",
		authdomain.VerificationPurposeRegister,
	)

	if firstKey == secondKey {
		t.Fatalf(
			"different field boundaries produced the same key %q",
			firstKey,
		)
	}
	if strings.ContainsRune(unicodeKey, '\x00') {
		t.Fatalf("lock key %q contains a PostgreSQL-invalid NUL byte", unicodeKey)
	}
	if !utf8.ValidString(unicodeKey) {
		t.Fatalf("lock key %q is not valid UTF-8", unicodeKey)
	}
	if unicodeKey != verificationAdvisoryLockKey(
		authdomain.VerificationChannelEmail,
		"用户@example.com",
		authdomain.VerificationPurposeRegister,
	) {
		t.Fatal("the same input did not produce a stable lock key")
	}
}
