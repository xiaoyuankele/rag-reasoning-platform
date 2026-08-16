package user

import (
	"errors"
	"strings"
)

const (
	// MaxDisplayNameRunes 是显示名允许的最大 Unicode 字符数。
	MaxDisplayNameRunes = 100
)

var (
	// ErrInvalidDisplayName 表示规范化后的显示名为空或超过长度上限。
	ErrInvalidDisplayName = errors.New("display name must contain between 1 and 100 characters")
)

// NormalizeDisplayName 去除两端空白并校验显示名长度。
// 使用 []rune 计数，避免把一个中文字符误算成三个 UTF-8 字节。
func NormalizeDisplayName(rawDisplayName string) (string, error) {
	displayName := strings.TrimSpace(rawDisplayName)
	length := len([]rune(displayName))
	if length == 0 || length > MaxDisplayNameRunes {
		return "", ErrInvalidDisplayName
	}

	return displayName, nil
}
