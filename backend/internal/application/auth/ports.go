// Package auth 编排注册、登录、Session 和当前用户相关用例。
package auth

// PasswordHasher 是 Application 对密码哈希能力的最小需求。
// 具体 Argon2id 参数、salt 和编码格式由 Infrastructure 负责。
type PasswordHasher interface {
	Hash(password string) (string, error)
	Verify(password, encodedHash string) (bool, error)
}
