// Package verification 编排验证码申请和消费用例。
package verification

import (
	"context"
	"errors"
	"fmt"
	"time"

	authdomain "rag-reasoning-platform/backend/internal/domain/auth"
)

const (
	// DefaultChallengeTTL 是验证码默认有效期。
	DefaultChallengeTTL = 10 * time.Minute

	// DefaultResendCooldown 是同一联系方式再次发送验证码前的等待时间。
	DefaultResendCooldown = 60 * time.Second
)

var (
	// ErrVerificationCooldown 表示同一联系方式仍处于重发冷却期。
	ErrVerificationCooldown = errors.New("verification code resend is temporarily unavailable")

	// ErrVerificationDeliveryUnavailable 表示发送渠道暂时无法交付验证码。
	// Handler 只识别该稳定类别，不把 Sender 的内部错误直接暴露给客户端。
	ErrVerificationDeliveryUnavailable = errors.New("verification delivery is temporarily unavailable")
)

// CooldownError 除了稳定错误类别，还携带下一次允许发送的时间。
type CooldownError struct {
	RetryAt time.Time
}

func (e *CooldownError) Error() string {
	return fmt.Sprintf(
		"%s until %s",
		ErrVerificationCooldown,
		e.RetryAt.Format(time.RFC3339),
	)
}

// Unwrap 让 errors.Is(err, ErrVerificationCooldown) 可以识别该结构化错误。
func (e *CooldownError) Unwrap() error {
	return ErrVerificationCooldown
}

// RequestInput 是申请验证码用例的输入。
type RequestInput struct {
	Channel     authdomain.VerificationChannel
	Destination string
	Purpose     authdomain.VerificationPurpose
}

// RequestOutput 是申请成功后可以安全返回给调用方的数据。
// 它故意不包含六位验证码或 code_hash。
type RequestOutput struct {
	ChallengeID int64
	ExpiresAt   time.Time
	ResendAfter time.Time
}

// Message 是 Application 交给发送适配器的内部消息。
// Code 属于敏感数据，Sender 不得把它写入日志。
type Message struct {
	ChallengeID int64
	Channel     authdomain.VerificationChannel
	Destination string
	Purpose     authdomain.VerificationPurpose
	Code        string
	ExpiresAt   time.Time
}

// Repository 是验证码申请用例需要的最小持久化能力。
type Repository interface {
	FindLatest(
		ctx context.Context,
		channel authdomain.VerificationChannel,
		destination string,
		purpose authdomain.VerificationPurpose,
	) (authdomain.VerificationChallenge, error)

	Create(
		ctx context.Context,
		challenge authdomain.VerificationChallenge,
		resendCooldown time.Duration,
	) (authdomain.VerificationChallenge, error)

	MarkSent(
		ctx context.Context,
		challengeID int64,
		sentAt time.Time,
	) (authdomain.VerificationChallenge, error)
}

// CodeGenerator 负责生成不可预测的六位数字验证码。
type CodeGenerator interface {
	Generate() (string, error)
}

// CodeHasher 负责计算和核对验证码 HMAC 摘要。
type CodeHasher interface {
	Hash(
		channel authdomain.VerificationChannel,
		destination string,
		purpose authdomain.VerificationPurpose,
		code string,
	) string

	Matches(
		expectedHash string,
		channel authdomain.VerificationChannel,
		destination string,
		purpose authdomain.VerificationPurpose,
		code string,
	) bool
}

// Sender 负责把验证码交给指定通信渠道。
type Sender interface {
	Send(ctx context.Context, message Message) error
}

// Service 编排申请验证码的完整流程。
type Service struct {
	repository     Repository
	generator      CodeGenerator
	hasher         CodeHasher
	sender         Sender
	now            func() time.Time
	challengeTTL   time.Duration
	resendCooldown time.Duration
}

// NewService 创建验证码应用服务。
func NewService(
	repository Repository,
	generator CodeGenerator,
	hasher CodeHasher,
	sender Sender,
	now func() time.Time,
	challengeTTL time.Duration,
	resendCooldown time.Duration,
) *Service {
	if now == nil {
		now = time.Now
	}
	if challengeTTL <= 0 {
		challengeTTL = DefaultChallengeTTL
	}
	if resendCooldown <= 0 {
		resendCooldown = DefaultResendCooldown
	}

	return &Service{
		repository:     repository,
		generator:      generator,
		hasher:         hasher,
		sender:         sender,
		now:            now,
		challengeTTL:   challengeTTL,
		resendCooldown: resendCooldown,
	}
}

// RequestCode 创建、发送并确认一条注册验证码挑战。
func (s *Service) RequestCode(
	ctx context.Context,
	input RequestInput,
) (RequestOutput, error) {
	if !input.Purpose.IsValid() {
		return RequestOutput{}, authdomain.ErrInvalidVerificationPurpose
	}

	destination, err := authdomain.NormalizeVerificationDestination(
		input.Channel,
		input.Destination,
	)
	if err != nil {
		return RequestOutput{}, err
	}

	now := s.now().UTC()
	latestChallenge, err := s.repository.FindLatest(
		ctx,
		input.Channel,
		destination,
		input.Purpose,
	)
	if err != nil && !errors.Is(err, authdomain.ErrVerificationChallengeNotFound) {
		return RequestOutput{}, fmt.Errorf("find latest verification challenge: %w", err)
	}
	if err == nil && latestChallenge.LastSentAt != nil {
		retryAt := latestChallenge.LastSentAt.Add(s.resendCooldown)
		if now.Before(retryAt) {
			return RequestOutput{}, &CooldownError{RetryAt: retryAt}
		}
	}

	code, err := s.generator.Generate()
	if err != nil {
		return RequestOutput{}, fmt.Errorf("generate verification code: %w", err)
	}

	createdChallenge, err := s.repository.Create(
		ctx,
		authdomain.VerificationChallenge{
			Channel:     input.Channel,
			Destination: destination,
			Purpose:     input.Purpose,
			CodeHash: s.hasher.Hash(
				input.Channel,
				destination,
				input.Purpose,
				code,
			),
			ExpiresAt: now.Add(s.challengeTTL),
			CreatedAt: now,
			UpdatedAt: now,
		},
		s.resendCooldown,
	)
	if err != nil {
		return RequestOutput{}, fmt.Errorf("create verification challenge: %w", err)
	}

	if err := s.sender.Send(
		ctx,
		Message{
			ChallengeID: createdChallenge.ID,
			Channel:     createdChallenge.Channel,
			Destination: createdChallenge.Destination,
			Purpose:     createdChallenge.Purpose,
			Code:        code,
			ExpiresAt:   createdChallenge.ExpiresAt,
		},
	); err != nil {
		// 外部发送失败不能通过数据库事务“回滚”。保留 send_count=0 的记录，
		// 后续注册用例会拒绝消费它，定时清理再删除过期记录。
		return RequestOutput{}, fmt.Errorf(
			"%w: %w",
			ErrVerificationDeliveryUnavailable,
			err,
		)
	}

	sentAt := s.now().UTC()
	sentChallenge, err := s.repository.MarkSent(
		ctx,
		createdChallenge.ID,
		sentAt,
	)
	if err != nil {
		// 如果发送成功但落库失败，客户端得到失败；旧验证码不会被视为可消费。
		// pending 记录仍会占用一次冷却窗口，客户端稍后才能安全地重新申请。
		return RequestOutput{}, fmt.Errorf("mark verification challenge sent: %w", err)
	}

	return RequestOutput{
		ChallengeID: sentChallenge.ID,
		ExpiresAt:   sentChallenge.ExpiresAt,
		ResendAfter: sentAt.Add(s.resendCooldown),
	}, nil
}
