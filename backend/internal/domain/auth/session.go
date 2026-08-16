// Package auth 定义登录会话和验证码挑战的稳定领域概念。
package auth

import "time"

// Session 表示服务器保存的一次登录状态。
// TokenHash 是原始随机 Token 的摘要；原始 Token 只能短暂存在于 Cookie 和进程内存中。
type Session struct {
	ID        int64
	UserID    int64
	TokenHash string
	ExpiresAt time.Time
	RevokedAt *time.Time
	CreatedAt time.Time
}

// IsActive 判断 Session 在指定时刻是否仍可用于认证。
// 传入 now 而不是在方法内部调用 time.Now，能让测试保持确定性。
func (s Session) IsActive(now time.Time) bool {
	if s.RevokedAt != nil {
		return false
	}

	// 到达 ExpiresAt 的那一刻就已经过期，因此必须严格晚于 now。
	return s.ExpiresAt.After(now)
}
