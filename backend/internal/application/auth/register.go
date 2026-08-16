package auth

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"

	authdomain "rag-reasoning-platform/backend/internal/domain/auth"
	userdomain "rag-reasoning-platform/backend/internal/domain/user"
)

const (
	// DefaultSessionTTL 是第一版登录状态的默认有效期。
	DefaultSessionTTL = 7 * 24 * time.Hour
)

var (
	// ErrInvalidRegistrationRequest 表示注册输入缺少必要字段或格式不合法。
	ErrInvalidRegistrationRequest = errors.New("registration request is invalid")

	// ErrVerificationCodeInvalid 表示挑战不存在、已消费、未发送或验证码错误。
	ErrVerificationCodeInvalid = errors.New("verification code is invalid")

	// ErrVerificationCodeExpired 表示验证码已经过期。
	ErrVerificationCodeExpired = errors.New("verification code has expired")

	// ErrVerificationAttemptsExceeded 表示挑战已经达到最大核对次数。
	ErrVerificationAttemptsExceeded = errors.New("verification attempts exceeded")

	// ErrContactAlreadyRegistered 表示该邮箱或手机号已经绑定其他账户。
	ErrContactAlreadyRegistered = errors.New("contact is already registered")

	// ErrRegisterDependencies 表示注册服务缺少运行所需的端口实现。
	ErrRegisterDependencies = errors.New("register service dependencies are incomplete")
)

// RegisterInput 是注册用例从 HTTP 边界接收的数据。
type RegisterInput struct {
	VerificationID   int64
	VerificationCode string
	DisplayName      string
	Password         string
}

// RegisterOutput 是注册成功后交给 Handler 的安全结果。
// SessionToken 只能用于 Set-Cookie，不能放进 JSON 响应或日志。
type RegisterOutput struct {
	User             userdomain.User
	SessionToken     string
	SessionExpiresAt time.Time
}

// RegisterService 编排验证码消费、密码哈希、用户创建和 Session 创建。
type RegisterService struct {
	repository     RegistrationRepository
	passwordHasher PasswordHasher
	codeMatcher    VerificationCodeMatcher
	tokenGenerator SessionTokenGenerator
	now            func() time.Time
	sessionTTL     time.Duration
}

// NewRegisterService 创建注册应用服务。
func NewRegisterService(
	repository RegistrationRepository,
	passwordHasher PasswordHasher,
	codeMatcher VerificationCodeMatcher,
	tokenGenerator SessionTokenGenerator,
	now func() time.Time,
	sessionTTL time.Duration,
) (*RegisterService, error) {
	if repository == nil || passwordHasher == nil || codeMatcher == nil ||
		tokenGenerator == nil {
		return nil, ErrRegisterDependencies
	}
	if now == nil {
		now = time.Now
	}
	if sessionTTL <= 0 {
		sessionTTL = DefaultSessionTTL
	}

	return &RegisterService{
		repository:     repository,
		passwordHasher: passwordHasher,
		codeMatcher:    codeMatcher,
		tokenGenerator: tokenGenerator,
		now:            now,
		sessionTTL:     sessionTTL,
	}, nil
}

// Register 完成一次已验证联系方式注册。
func (s *RegisterService) Register(
	ctx context.Context,
	input RegisterInput,
) (RegisterOutput, error) {
	if input.VerificationID <= 0 || !isSixDigitCode(input.VerificationCode) {
		return RegisterOutput{}, ErrInvalidRegistrationRequest
	}

	displayName, err := userdomain.NormalizeDisplayName(input.DisplayName)
	if err != nil {
		return RegisterOutput{}, err
	}
	if err := userdomain.ValidatePassword(input.Password); err != nil {
		return RegisterOutput{}, err
	}

	now := s.now().UTC()
	challenge, err := s.repository.FindVerificationChallenge(
		ctx,
		input.VerificationID,
	)
	if errors.Is(err, authdomain.ErrVerificationChallengeNotFound) {
		return RegisterOutput{}, ErrVerificationCodeInvalid
	}
	if err != nil {
		return RegisterOutput{}, fmt.Errorf("find registration verification challenge: %w", err)
	}

	if err := validateRegistrationChallenge(challenge, now); err != nil {
		return RegisterOutput{}, err
	}
	if !s.codeMatcher.Matches(
		challenge.CodeHash,
		challenge.Channel,
		challenge.Destination,
		challenge.Purpose,
		input.VerificationCode,
	) {
		attemptCount, incrementErr := s.repository.IncrementVerificationAttempts(
			ctx,
			challenge.ID,
			now,
		)
		if incrementErr != nil {
			return RegisterOutput{}, fmt.Errorf("increment verification attempts: %w", incrementErr)
		}
		if attemptCount >= authdomain.MaxVerificationAttempts {
			return RegisterOutput{}, ErrVerificationAttemptsExceeded
		}
		return RegisterOutput{}, ErrVerificationCodeInvalid
	}

	passwordHash, err := s.passwordHasher.Hash(input.Password)
	if err != nil {
		return RegisterOutput{}, fmt.Errorf("hash registration password: %w", err)
	}
	tokenPair, err := s.tokenGenerator.Generate()
	if err != nil {
		return RegisterOutput{}, fmt.Errorf("generate registration session token: %w", err)
	}

	result, err := s.repository.CreateRegistration(
		ctx,
		RegistrationRecord{
			ChallengeID:               challenge.ID,
			ExpectedChallengeCodeHash: challenge.CodeHash,
			DisplayName:               displayName,
			PasswordHash:              passwordHash,
			SessionTokenHash:          tokenPair.Hash,
			SessionExpiresAt:          now.Add(s.sessionTTL),
			RegisteredAt:              now,
		},
	)
	if err != nil {
		return RegisterOutput{}, fmt.Errorf("create registration: %w", err)
	}

	return RegisterOutput{
		User:             result.User,
		SessionToken:     tokenPair.Raw,
		SessionExpiresAt: result.Session.ExpiresAt,
	}, nil
}

func validateRegistrationChallenge(
	challenge authdomain.VerificationChallenge,
	now time.Time,
) error {
	if challenge.Purpose != authdomain.VerificationPurposeRegister ||
		!challenge.Channel.IsValid() || challenge.ConsumedAt != nil ||
		challenge.LastSentAt == nil || challenge.SendCount <= 0 {
		return ErrVerificationCodeInvalid
	}
	if !challenge.ExpiresAt.After(now) {
		return ErrVerificationCodeExpired
	}
	if challenge.AttemptCount >= authdomain.MaxVerificationAttempts {
		return ErrVerificationAttemptsExceeded
	}
	return nil
}

func isSixDigitCode(code string) bool {
	if len(code) != 6 || strings.TrimSpace(code) != code {
		return false
	}
	for index := 0; index < len(code); index++ {
		if code[index] < '0' || code[index] > '9' {
			return false
		}
	}
	return true
}
