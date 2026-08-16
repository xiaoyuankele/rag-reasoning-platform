package user

import "errors"

const (
	// MinPasswordBytes 是当前产品允许的最小密码字节数。
	MinPasswordBytes = 8

	// MaxPasswordBytes 限制异常大输入，避免密码哈希成为内存拒绝服务入口。
	MaxPasswordBytes = 128
)

var (
	// ErrPasswordTooShort 表示密码不足 8 个 ASCII 字符。
	ErrPasswordTooShort = errors.New("password must be at least 8 characters")

	// ErrPasswordTooLong 表示密码超过 128 个 ASCII 字符。
	ErrPasswordTooLong = errors.New("password must be at most 128 characters")

	// ErrPasswordInvalidCharacter 表示密码包含字母和数字以外的字符。
	ErrPasswordInvalidCharacter = errors.New("password may contain only ASCII letters and digits")

	// ErrPasswordMissingUppercase 表示密码没有大写字母。
	ErrPasswordMissingUppercase = errors.New("password must contain an uppercase letter")

	// ErrPasswordMissingLowercase 表示密码没有小写字母。
	ErrPasswordMissingLowercase = errors.New("password must contain a lowercase letter")

	// ErrPasswordMissingDigit 表示密码没有数字。
	ErrPasswordMissingDigit = errors.New("password must contain a digit")
)

// ValidatePassword 执行注册阶段的完整密码策略。
// 当前规则只接受 ASCII，因此字符数和字节数相同。
func ValidatePassword(password string) error {
	if len(password) < MinPasswordBytes {
		return ErrPasswordTooShort
	}
	if len(password) > MaxPasswordBytes {
		return ErrPasswordTooLong
	}

	var hasUppercase bool
	var hasLowercase bool
	var hasDigit bool

	for index := 0; index < len(password); index++ {
		character := password[index]

		switch {
		case character >= 'A' && character <= 'Z':
			hasUppercase = true
		case character >= 'a' && character <= 'z':
			hasLowercase = true
		case character >= '0' && character <= '9':
			hasDigit = true
		default:
			return ErrPasswordInvalidCharacter
		}
	}

	if !hasUppercase {
		return ErrPasswordMissingUppercase
	}
	if !hasLowercase {
		return ErrPasswordMissingLowercase
	}
	if !hasDigit {
		return ErrPasswordMissingDigit
	}

	return nil
}
