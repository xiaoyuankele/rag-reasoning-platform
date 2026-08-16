// Package auth 编排注册、登录、Session 和当前用户相关用例。
package auth

import (
	"context"
	"time"

	authdomain "rag-reasoning-platform/backend/internal/domain/auth"
	userdomain "rag-reasoning-platform/backend/internal/domain/user"
)

// PasswordHasher 是 Application 对密码哈希能力的最小需求。
// 具体 Argon2id 参数、salt 和编码格式由 Infrastructure 负责。
type PasswordHasher interface {
	Hash(password string) (string, error)
	Verify(password, encodedHash string) (bool, error)
}

// VerificationCodeMatcher 是注册用例核对验证码摘要所需的最小能力。
// HMAC 密钥和比较细节由 Infrastructure 持有，Application 不接触密钥。
type VerificationCodeMatcher interface {
	Matches(
		expectedHash string,
		channel authdomain.VerificationChannel,
		destination string,
		purpose authdomain.VerificationPurpose,
		code string,
	) bool
}

// SessionTokenPair 同时携带只返回浏览器的原始 Token 和只写数据库的摘要。
// Raw 与 Hash 必须在不同边界使用，禁止把 Raw 写入数据库或日志。
type SessionTokenPair struct {
	Raw  string
	Hash string
}

// SessionTokenGenerator 生成不可预测的 Session Token 及其安全摘要。
type SessionTokenGenerator interface {
	Generate() (SessionTokenPair, error)
}

// RegistrationRecord 是 Application 交给事务仓储的注册数据。
type RegistrationRecord struct {
	ChallengeID               int64
	ExpectedChallengeCodeHash string
	DisplayName               string
	PasswordHash              string
	SessionTokenHash          string
	SessionExpiresAt          time.Time
	RegisteredAt              time.Time
}

// RegistrationResult 是注册事务成功后产生的用户和 Session。
type RegistrationResult struct {
	User    userdomain.User
	Session authdomain.Session
}

// RegistrationRepository 是注册用例需要的最小原子持久化能力。
// CreateRegistration 必须在同一事务中创建用户、消费验证码并创建 Session。
type RegistrationRepository interface {
	FindVerificationChallenge(
		ctx context.Context,
		challengeID int64,
	) (authdomain.VerificationChallenge, error)

	IncrementVerificationAttempts(
		ctx context.Context,
		challengeID int64,
		attemptedAt time.Time,
	) (int, error)

	CreateRegistration(
		ctx context.Context,
		record RegistrationRecord,
	) (RegistrationResult, error)
}

// LoginAccount 把公开 User 与仅用于认证核对的密码哈希组合起来。
// PasswordHash 不属于公开领域对象，也不得越过 Application 进入 Handler。
type LoginAccount struct {
	User         userdomain.User
	PasswordHash string
}

// SessionRecord 是 Application 请求持久化的一次新登录状态。
type SessionRecord struct {
	UserID    int64
	TokenHash string
	ExpiresAt time.Time
	CreatedAt time.Time
}

// LoginRepository 是登录用例查找凭据和创建 Session 所需的最小能力。
type LoginRepository interface {
	FindLoginAccount(
		ctx context.Context,
		normalizedIdentifier string,
	) (LoginAccount, error)

	CreateSession(
		ctx context.Context,
		record SessionRecord,
	) (authdomain.Session, error)
}
