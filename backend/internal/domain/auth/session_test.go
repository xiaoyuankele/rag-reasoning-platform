package auth

import (
	"testing"
	"time"
)

func TestSessionIsActive(t *testing.T) {
	now := time.Date(2026, time.August, 16, 8, 0, 0, 0, time.UTC)
	revokedAt := now.Add(-time.Minute)

	testCases := []struct {
		name    string
		session Session
		want    bool
	}{
		{
			name: "unrevoked future session is active",
			session: Session{
				ExpiresAt: now.Add(time.Hour),
			},
			want: true,
		},
		{
			name: "session expires at exact boundary",
			session: Session{
				ExpiresAt: now,
			},
			want: false,
		},
		{
			name: "past session is inactive",
			session: Session{
				ExpiresAt: now.Add(-time.Second),
			},
			want: false,
		},
		{
			name: "revoked future session is inactive",
			session: Session{
				ExpiresAt: now.Add(time.Hour),
				RevokedAt: &revokedAt,
			},
			want: false,
		},
	}

	for _, testCase := range testCases {
		t.Run(testCase.name, func(t *testing.T) {
			if actual := testCase.session.IsActive(now); actual != testCase.want {
				t.Fatalf("IsActive() = %t, want %t", actual, testCase.want)
			}
		})
	}
}
