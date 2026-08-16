package auth

import "time"

const (
	// MaxVerificationAttempts 是同一个验证码挑战允许的最大验证次数。
	MaxVerificationAttempts = 5
)

// VerificationChannel 表示验证码通过哪种通信渠道送达。
type VerificationChannel string

const (
	// VerificationChannelEmail 表示通过邮箱发送。
	VerificationChannelEmail VerificationChannel = "email"

	// VerificationChannelSMS 表示通过短信发送。
	VerificationChannelSMS VerificationChannel = "sms"
)

// IsValid 判断渠道是否属于当前契约支持的集合。
func (c VerificationChannel) IsValid() bool {
	return c == VerificationChannelEmail || c == VerificationChannelSMS
}

// VerificationPurpose 表示验证码准备授权的动作。
// 独立类型可以防止未来“注册验证码”被误用于找回密码等其他操作。
type VerificationPurpose string

const (
	// VerificationPurposeRegister 表示验证码只用于注册新账户。
	VerificationPurposeRegister VerificationPurpose = "register"
)

// IsValid 判断用途是否属于当前契约支持的集合。
func (p VerificationPurpose) IsValid() bool {
	return p == VerificationPurposeRegister
}

// VerificationChallenge 表示一次有限次数、有限时间且只能消费一次的验证过程。
type VerificationChallenge struct {
	ID           int64
	Channel      VerificationChannel
	Destination  string
	Purpose      VerificationPurpose
	CodeHash     string
	ExpiresAt    time.Time
	ConsumedAt   *time.Time
	AttemptCount int
	SendCount    int
	LastSentAt   *time.Time
	CreatedAt    time.Time
	UpdatedAt    time.Time
}

// CanAttempt 判断在指定时刻能否继续核对验证码。
func (c VerificationChallenge) CanAttempt(now time.Time) bool {
	if !c.Channel.IsValid() || !c.Purpose.IsValid() {
		return false
	}

	if c.ConsumedAt != nil ||
		c.AttemptCount < 0 ||
		c.AttemptCount >= MaxVerificationAttempts {
		return false
	}

	return c.ExpiresAt.After(now)
}
