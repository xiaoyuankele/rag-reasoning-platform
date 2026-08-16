// Package user 定义个人账户领域中稳定的数据和规则。
package user

import (
	"strings"
	"time"
)

// Status 表示账户当前是否允许登录和使用系统。
type Status string

const (
	// StatusActive 表示账户可以正常认证和使用系统。
	StatusActive Status = "active"

	// StatusDisabled 表示账户已被停用，已有 Session 也不应继续授权。
	StatusDisabled Status = "disabled"
)

// IsValid 判断状态是否属于当前系统支持的集合。
func (s Status) IsValid() bool {
	return s == StatusActive || s == StatusDisabled
}

// AllowsAuthentication 判断该状态是否允许创建或继续使用 Session。
func (s Status) AllowsAuthentication() bool {
	return s == StatusActive
}

// User 表示一个独立的个人账户。
//
// Email 和 PhoneE164 使用指针表达数据库 NULL：用户可以只绑定其中一种联系方式。
// 未验证的联系方式不进入 User，而是暂存在验证码挑战中。
type User struct {
	ID int64

	Email           *string
	PhoneE164       *string
	EmailVerifiedAt *time.Time
	PhoneVerifiedAt *time.Time

	DisplayName string
	Status      Status
	CreatedAt   time.Time
	UpdatedAt   time.Time
}

// HasVerifiedContact 判断用户是否至少拥有一种已经验证的联系方式。
func (u User) HasVerifiedContact() bool {
	emailVerified := u.Email != nil &&
		strings.TrimSpace(*u.Email) != "" &&
		u.EmailVerifiedAt != nil
	phoneVerified := u.PhoneE164 != nil &&
		strings.TrimSpace(*u.PhoneE164) != "" &&
		u.PhoneVerifiedAt != nil

	return emailVerified || phoneVerified
}
