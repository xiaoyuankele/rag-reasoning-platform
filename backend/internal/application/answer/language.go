package answer

import (
	"errors"
	"strings"
	"unicode"
)

// ResponseLanguage 表示问答结果允许使用的语言偏好。
//
// auto 表示由后端根据问题中的主要字符自动选择；zh 和 en 分别表示
// 强制使用中文或英文。它属于问答用例的输入偏好，不是文献领域模型。
type ResponseLanguage string

const (
	ResponseLanguageAuto    ResponseLanguage = "auto"
	ResponseLanguageChinese ResponseLanguage = "zh"
	ResponseLanguageEnglish ResponseLanguage = "en"
)

var (
	// ErrInvalidResponseLanguage 表示调用者提交了当前不支持的回答语言。
	ErrInvalidResponseLanguage = errors.New(
		"response language must be auto, zh, or en",
	)
)

// resolveResponseLanguage 校验语言偏好，并把 auto 解析成最终使用的 zh 或 en。
//
// 空字符串代表调用者没有提供该字段，与 auto 的行为相同。第一版只统计
// 汉字和拉丁字母；英文字符更多时选择英文，其他情况回退中文。
func resolveResponseLanguage(
	requested ResponseLanguage,
	query string,
) (ResponseLanguage, error) {
	normalized := ResponseLanguage(
		strings.ToLower(strings.TrimSpace(string(requested))),
	)

	switch normalized {
	case "", ResponseLanguageAuto:
		return detectPrimaryResponseLanguage(query), nil
	case ResponseLanguageChinese, ResponseLanguageEnglish:
		return normalized, nil
	default:
		return "", ErrInvalidResponseLanguage
	}
}

// detectPrimaryResponseLanguage 使用 Unicode 脚本数量判断问题的主要语言。
//
// 这不是自然语言识别模型，只是 auto 模式的轻量默认机制。无法判断或
// 中英文数量相同时回退中文，符合当前产品的主要用户环境。
func detectPrimaryResponseLanguage(query string) ResponseLanguage {
	var hanCount int
	var latinCount int

	for _, character := range query {
		switch {
		case unicode.Is(unicode.Han, character):
			hanCount++
		case unicode.Is(unicode.Latin, character):
			latinCount++
		}
	}

	if latinCount > hanCount {
		return ResponseLanguageEnglish
	}
	return ResponseLanguageChinese
}
