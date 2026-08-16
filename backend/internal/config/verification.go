package config

import (
	"errors"
	"fmt"
	"os"
	"strings"
	"time"
)

const (
	// VerificationSenderFake 表示只把验证码保存在当前进程内存中。
	// 它不会访问邮件或短信服务，因此适合作为开发和自动化测试默认值。
	VerificationSenderFake = "fake"

	minimumVerificationHMACSecretBytes = 32
	defaultVerificationRateLimitWindow = time.Minute
	defaultVerificationPerClientLimit  = 5
	defaultVerificationGlobalLimit     = 100
	maximumVerificationPerClientLimit  = 1000
	maximumVerificationGlobalLimit     = 10000
)

var (
	// ErrVerificationHMACSecretRequired 表示服务端没有配置验证码摘要密钥。
	ErrVerificationHMACSecretRequired = errors.New(
		"VERIFICATION_HMAC_SECRET must be provided",
	)

	// ErrVerificationHMACSecretTooShort 表示验证码摘要密钥未达到安全下限。
	ErrVerificationHMACSecretTooShort = errors.New(
		"VERIFICATION_HMAC_SECRET must be at least 32 bytes",
	)

	// ErrInvalidVerificationSender 表示配置了当前版本不支持的发送器。
	ErrInvalidVerificationSender = errors.New(
		"VERIFICATION_SENDER must be fake",
	)

	// ErrInvalidVerificationRateLimits 表示全局限额小于单客户端限额。
	ErrInvalidVerificationRateLimits = errors.New(
		"verification global rate limit must not be smaller than the per-client limit",
	)
)

// VerificationConfig 保存验证码 HTTP 能力所需的安全配置。
// HMACSecret 属于敏感值，只允许传给 HMAC Infrastructure，禁止记录到日志。
type VerificationConfig struct {
	HMACSecret      string
	Sender          string
	RateLimitWindow time.Duration
	PerClientLimit  int
	GlobalLimit     int
}

// LoadVerification 从环境变量加载验证码摘要、Sender 和单实例限流配置。
func LoadVerification() (VerificationConfig, error) {
	hmacSecret := strings.TrimSpace(os.Getenv("VERIFICATION_HMAC_SECRET"))
	if hmacSecret == "" {
		return VerificationConfig{}, ErrVerificationHMACSecretRequired
	}
	if len(hmacSecret) < minimumVerificationHMACSecretBytes {
		return VerificationConfig{}, ErrVerificationHMACSecretTooShort
	}

	sender := strings.ToLower(strings.TrimSpace(os.Getenv("VERIFICATION_SENDER")))
	if sender == "" {
		sender = VerificationSenderFake
	}
	if sender != VerificationSenderFake {
		return VerificationConfig{}, ErrInvalidVerificationSender
	}

	rateLimitWindow, err := loadPositiveDuration(
		"VERIFICATION_RATE_LIMIT_WINDOW",
		defaultVerificationRateLimitWindow,
	)
	if err != nil {
		return VerificationConfig{}, fmt.Errorf(
			"load verification rate-limit window: %w",
			err,
		)
	}

	perClientLimit, err := loadPositiveBoundedInt(
		"VERIFICATION_PER_CLIENT_LIMIT",
		defaultVerificationPerClientLimit,
		maximumVerificationPerClientLimit,
	)
	if err != nil {
		return VerificationConfig{}, fmt.Errorf(
			"load verification per-client limit: %w",
			err,
		)
	}

	globalLimit, err := loadPositiveBoundedInt(
		"VERIFICATION_GLOBAL_LIMIT",
		defaultVerificationGlobalLimit,
		maximumVerificationGlobalLimit,
	)
	if err != nil {
		return VerificationConfig{}, fmt.Errorf(
			"load verification global limit: %w",
			err,
		)
	}
	if globalLimit < perClientLimit {
		return VerificationConfig{}, ErrInvalidVerificationRateLimits
	}

	return VerificationConfig{
		HMACSecret:      hmacSecret,
		Sender:          sender,
		RateLimitWindow: rateLimitWindow,
		PerClientLimit:  perClientLimit,
		GlobalLimit:     globalLimit,
	}, nil
}
