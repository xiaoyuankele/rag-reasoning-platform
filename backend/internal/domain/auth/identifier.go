package auth

import (
	"errors"
	"strings"
)

var (
	// ErrInvalidLoginIdentifier 表示登录标识符不是受支持的邮箱或 E.164 手机号。
	ErrInvalidLoginIdentifier = errors.New("login identifier must be an email address or E.164 phone number")
)

// NormalizeLoginIdentifier 判断标识符类型，并复用验证码联系方式的规范化规则。
// 返回值只包含规范化后的地址；Application 不需要关心用户使用邮箱还是手机号登录。
func NormalizeLoginIdentifier(rawIdentifier string) (string, error) {
	identifier := strings.TrimSpace(rawIdentifier)
	var channel VerificationChannel
	switch {
	case strings.Contains(identifier, "@"):
		channel = VerificationChannelEmail
	case strings.HasPrefix(identifier, "+"):
		channel = VerificationChannelSMS
	default:
		return "", ErrInvalidLoginIdentifier
	}

	normalized, err := NormalizeVerificationDestination(channel, identifier)
	if err != nil {
		return "", ErrInvalidLoginIdentifier
	}
	return normalized, nil
}
