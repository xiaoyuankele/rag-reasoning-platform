package config

import (
	"errors"
	"fmt"
	"net/mail"
	"os"
	"strings"
	"time"
)

const (
	// VerificationSenderFake 表示只把验证码保存在当前进程内存中。
	// 它不会访问邮件或短信服务，因此适合作为开发和自动化测试默认值。
	VerificationSenderFake = "fake"

	// VerificationSenderMailpit 表示通过无认证 SMTP 把验证码交给本地 Mailpit。
	// 该模式只用于本机人工联调，不能直接作为生产邮件发送方案。
	VerificationSenderMailpit = "mailpit"

	minimumVerificationHMACSecretBytes = 32
	defaultVerificationRateLimitWindow = time.Minute
	defaultVerificationPerClientLimit  = 5
	defaultVerificationGlobalLimit     = 100
	maximumVerificationPerClientLimit  = 1000
	maximumVerificationGlobalLimit     = 10000
	defaultVerificationSMTPHost        = "127.0.0.1"
	defaultVerificationSMTPPort        = 1025
	defaultVerificationSMTPFromAddress = "no-reply@rag.local"
	defaultVerificationSMTPFromName    = "RAG Reasoning Platform"
	defaultVerificationSMTPTimeout     = 5 * time.Second
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
		"VERIFICATION_SENDER must be fake or mailpit",
	)

	// ErrInvalidVerificationSMTPConfiguration 表示 Mailpit SMTP 地址或发件人配置不安全。
	ErrInvalidVerificationSMTPConfiguration = errors.New(
		"verification SMTP configuration is invalid",
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
	SMTPHost        string
	SMTPPort        int
	SMTPFromAddress string
	SMTPFromName    string
	SMTPTimeout     time.Duration
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
	if sender != VerificationSenderFake && sender != VerificationSenderMailpit {
		return VerificationConfig{}, ErrInvalidVerificationSender
	}

	smtpHost := defaultVerificationSMTPHost
	smtpPort := defaultVerificationSMTPPort
	smtpFromAddress := defaultVerificationSMTPFromAddress
	smtpFromName := defaultVerificationSMTPFromName
	smtpTimeout := defaultVerificationSMTPTimeout
	if sender == VerificationSenderMailpit {
		var err error
		smtpHost = environmentOrDefault(
			"VERIFICATION_SMTP_HOST",
			defaultVerificationSMTPHost,
		)
		if strings.ContainsAny(smtpHost, "\r\n\t /\\") {
			return VerificationConfig{}, fmt.Errorf(
				"%w: VERIFICATION_SMTP_HOST contains invalid characters",
				ErrInvalidVerificationSMTPConfiguration,
			)
		}

		smtpPort, err = loadPositiveBoundedInt(
			"VERIFICATION_SMTP_PORT",
			defaultVerificationSMTPPort,
			maxPort,
		)
		if err != nil {
			return VerificationConfig{}, fmt.Errorf(
				"%w: load VERIFICATION_SMTP_PORT: %w",
				ErrInvalidVerificationSMTPConfiguration,
				err,
			)
		}

		smtpFromAddress = environmentOrDefault(
			"VERIFICATION_SMTP_FROM_ADDRESS",
			defaultVerificationSMTPFromAddress,
		)
		parsedFromAddress, parseErr := mail.ParseAddress(smtpFromAddress)
		if parseErr != nil || parsedFromAddress.Address != smtpFromAddress {
			return VerificationConfig{}, fmt.Errorf(
				"%w: VERIFICATION_SMTP_FROM_ADDRESS must be one plain email address",
				ErrInvalidVerificationSMTPConfiguration,
			)
		}

		smtpFromName = environmentOrDefault(
			"VERIFICATION_SMTP_FROM_NAME",
			defaultVerificationSMTPFromName,
		)
		if strings.ContainsAny(smtpFromName, "\r\n") {
			return VerificationConfig{}, fmt.Errorf(
				"%w: VERIFICATION_SMTP_FROM_NAME contains a line break",
				ErrInvalidVerificationSMTPConfiguration,
			)
		}

		smtpTimeout, err = loadPositiveDuration(
			"VERIFICATION_SMTP_TIMEOUT",
			defaultVerificationSMTPTimeout,
		)
		if err != nil {
			return VerificationConfig{}, fmt.Errorf(
				"%w: load VERIFICATION_SMTP_TIMEOUT: %w",
				ErrInvalidVerificationSMTPConfiguration,
				err,
			)
		}
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
		SMTPHost:        smtpHost,
		SMTPPort:        smtpPort,
		SMTPFromAddress: smtpFromAddress,
		SMTPFromName:    smtpFromName,
		SMTPTimeout:     smtpTimeout,
		RateLimitWindow: rateLimitWindow,
		PerClientLimit:  perClientLimit,
		GlobalLimit:     globalLimit,
	}, nil
}
