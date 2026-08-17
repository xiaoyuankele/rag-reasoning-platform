package auth

import (
	"context"
	"errors"
	"fmt"
	"time"

	authdomain "rag-reasoning-platform/backend/internal/domain/auth"
	userdomain "rag-reasoning-platform/backend/internal/domain/user"
)

var (
	// ErrInvalidPasswordResetRequest 表示重置请求字段格式不合法。
	ErrInvalidPasswordResetRequest = errors.New("password reset request is invalid")

	// ErrPasswordResetAccountNotFound 是 Repository 返回给 Application 的内部类别。
	// Application 会把它转换成统一验证码错误，Handler 不得暴露账户是否存在。
	ErrPasswordResetAccountNotFound = errors.New("password reset account not found")

	// ErrPasswordResetDependencies 表示重置服务缺少必要端口。
	ErrPasswordResetDependencies = errors.New("password reset service dependencies are incomplete")
)

// PasswordResetInput 是重置密码用例从 HTTP 边界接收的数据。
type PasswordResetInput struct {
	VerificationID   int64
	VerificationCode string
	NewPassword      string
}

// PasswordResetService 编排验证码核对、新密码哈希和原子重置事务。
type PasswordResetService struct {
	repository     PasswordResetRepository
	passwordHasher PasswordHasher
	codeMatcher    VerificationCodeMatcher
	now            func() time.Time
}

// NewPasswordResetService 创建密码重置应用服务。
func NewPasswordResetService(
	repository PasswordResetRepository,
	passwordHasher PasswordHasher,
	codeMatcher VerificationCodeMatcher,
	now func() time.Time,
) (*PasswordResetService, error) {
	if repository == nil || passwordHasher == nil || codeMatcher == nil {
		return nil, ErrPasswordResetDependencies
	}
	if now == nil {
		now = time.Now
	}

	return &PasswordResetService{
		repository:     repository,
		passwordHasher: passwordHasher,
		codeMatcher:    codeMatcher,
		now:            now,
	}, nil
}

// ResetPassword 核对一次性验证码并撤销账户全部旧 Session。
func (s *PasswordResetService) ResetPassword(
	ctx context.Context,
	input PasswordResetInput,
) error {
	if input.VerificationID <= 0 || !isSixDigitCode(input.VerificationCode) {
		return ErrInvalidPasswordResetRequest
	}
	if err := userdomain.ValidatePassword(input.NewPassword); err != nil {
		return err
	}

	now := s.now().UTC()
	challenge, err := s.repository.FindVerificationChallenge(
		ctx,
		input.VerificationID,
	)
	if errors.Is(err, authdomain.ErrVerificationChallengeNotFound) {
		return ErrVerificationCodeInvalid
	}
	if err != nil {
		return fmt.Errorf("find password reset verification challenge: %w", err)
	}
	if err := validatePasswordResetChallenge(challenge, now); err != nil {
		return err
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
			return fmt.Errorf("increment password reset verification attempts: %w", incrementErr)
		}
		if attemptCount >= authdomain.MaxVerificationAttempts {
			return ErrVerificationAttemptsExceeded
		}
		return ErrVerificationCodeInvalid
	}

	passwordHash, err := s.passwordHasher.Hash(input.NewPassword)
	if err != nil {
		return fmt.Errorf("hash password reset password: %w", err)
	}
	if err := s.repository.ResetPassword(
		ctx,
		PasswordResetRecord{
			ChallengeID:               challenge.ID,
			ExpectedChallengeCodeHash: challenge.CodeHash,
			PasswordHash:              passwordHash,
			ResetAt:                   now,
		},
	); errors.Is(err, ErrPasswordResetAccountNotFound) {
		// 不区分“验证码不存在”和“联系方式没有账户”，避免账户枚举。
		return ErrVerificationCodeInvalid
	} else if err != nil {
		return fmt.Errorf("reset account password: %w", err)
	}

	return nil
}

func validatePasswordResetChallenge(
	challenge authdomain.VerificationChallenge,
	now time.Time,
) error {
	if challenge.Purpose != authdomain.VerificationPurposePasswordReset ||
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
