// Package access 定义跨业务用例共享的可信访问范围。
package access

import "errors"

var (
	// ErrInvalidOwnerScope 表示数据所有者范围缺少有效的用户 ID。
	// 它通常意味着后端调用链存在编程或组装错误，而不是前端参数错误。
	ErrInvalidOwnerScope = errors.New("owner scope is invalid")
)

// OwnerScope 表示一次业务操作被允许访问的数据所有者范围。
//
// ownerUserID 使用未导出字段，调用方不能通过结构体字面量绕过校验；
// 必须使用 NewOwnerScope 从已经认证的 Actor.UserID 创建。
type OwnerScope struct {
	ownerUserID int64
}

// NewOwnerScope 使用可信的正整数用户 ID 创建所有权范围。
func NewOwnerScope(ownerUserID int64) (OwnerScope, error) {
	if ownerUserID <= 0 {
		return OwnerScope{}, ErrInvalidOwnerScope
	}

	return OwnerScope{ownerUserID: ownerUserID}, nil
}

// OwnerUserID 返回已经通过构造函数校验的数据所有者 ID。
func (s OwnerScope) OwnerUserID() int64 {
	return s.ownerUserID
}

// IsValid 判断 Scope 是否包含有效的数据所有者。
// Go 结构体始终存在零值，因此 Repository 仍应在执行 SQL 前进行防御性检查。
func (s OwnerScope) IsValid() bool {
	return s.ownerUserID > 0
}
