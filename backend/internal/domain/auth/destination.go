package auth

import (
	"errors"
	"net/mail"
	"regexp"
	"strings"
)

var (
	// ErrInvalidVerificationChannel 表示验证码渠道不受支持。
	ErrInvalidVerificationChannel = errors.New("verification channel must be email or sms")

	// ErrInvalidVerificationDestination 表示邮箱或手机号格式不合法。
	ErrInvalidVerificationDestination = errors.New("verification destination is invalid")

	// ErrInvalidVerificationPurpose 表示验证码用途不受支持。
	ErrInvalidVerificationPurpose = errors.New("verification purpose is invalid")

	// ErrVerificationChallengeNotFound 表示没有找到验证码挑战。
	ErrVerificationChallengeNotFound = errors.New("verification challenge not found")
)

var phoneE164Pattern = regexp.MustCompile(`^\+[1-9][0-9]{7,14}$`)

// NormalizeVerificationDestination 校验并规范化验证码接收地址。
// 邮箱统一去除两端空白并转为小写；手机号必须已经是 E.164 格式。
func NormalizeVerificationDestination(
	channel VerificationChannel,
	rawDestination string,
) (string, error) {
	destination := strings.TrimSpace(rawDestination)

	switch channel {
	case VerificationChannelEmail:
		destination = strings.ToLower(destination)
		if len(destination) < 3 || len(destination) > 320 {
			return "", ErrInvalidVerificationDestination
		}

		parsedAddress, err := mail.ParseAddress(destination)
		if err != nil || parsedAddress.Address != destination {
			return "", ErrInvalidVerificationDestination
		}

		return destination, nil

	case VerificationChannelSMS:
		if !phoneE164Pattern.MatchString(destination) {
			return "", ErrInvalidVerificationDestination
		}

		return destination, nil

	default:
		return "", ErrInvalidVerificationChannel
	}
}
