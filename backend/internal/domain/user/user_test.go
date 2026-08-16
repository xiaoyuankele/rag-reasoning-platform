package user

import (
	"testing"
	"time"
)

func TestStatusRules(t *testing.T) {
	testCases := []struct {
		name           string
		status         Status
		valid          bool
		authentication bool
	}{
		{
			name:           "active account can authenticate",
			status:         StatusActive,
			valid:          true,
			authentication: true,
		},
		{
			name:           "disabled account cannot authenticate",
			status:         StatusDisabled,
			valid:          true,
			authentication: false,
		},
		{
			name:           "unknown account status is invalid",
			status:         Status("unknown"),
			valid:          false,
			authentication: false,
		},
	}

	for _, testCase := range testCases {
		t.Run(testCase.name, func(t *testing.T) {
			if actual := testCase.status.IsValid(); actual != testCase.valid {
				t.Fatalf("IsValid() = %t, want %t", actual, testCase.valid)
			}

			if actual := testCase.status.AllowsAuthentication(); actual != testCase.authentication {
				t.Fatalf(
					"AllowsAuthentication() = %t, want %t",
					actual,
					testCase.authentication,
				)
			}
		})
	}
}

func TestUserHasVerifiedContact(t *testing.T) {
	email := "user@example.com"
	phone := "+8613800000000"
	emptyContact := "   "
	verifiedAt := time.Date(2026, time.August, 16, 8, 0, 0, 0, time.UTC)

	testCases := []struct {
		name string
		user User
		want bool
	}{
		{
			name: "verified email is enough",
			user: User{
				Email:           &email,
				EmailVerifiedAt: &verifiedAt,
			},
			want: true,
		},
		{
			name: "verified phone is enough",
			user: User{
				PhoneE164:       &phone,
				PhoneVerifiedAt: &verifiedAt,
			},
			want: true,
		},
		{
			name: "unverified email is not enough",
			user: User{
				Email: &email,
			},
			want: false,
		},
		{
			name: "verification timestamp without contact is not enough",
			user: User{
				EmailVerifiedAt: &verifiedAt,
			},
			want: false,
		},
		{
			name: "missing contacts are not verified",
			user: User{},
			want: false,
		},
		{
			name: "blank verified contact is not enough",
			user: User{
				Email:           &emptyContact,
				EmailVerifiedAt: &verifiedAt,
			},
			want: false,
		},
	}

	for _, testCase := range testCases {
		t.Run(testCase.name, func(t *testing.T) {
			if actual := testCase.user.HasVerifiedContact(); actual != testCase.want {
				t.Fatalf("HasVerifiedContact() = %t, want %t", actual, testCase.want)
			}
		})
	}
}
