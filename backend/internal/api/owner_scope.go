package api

import (
	"github.com/gin-gonic/gin"

	accessdomain "rag-reasoning-platform/backend/internal/domain/access"
)

// ownerScopeFromContext 把认证中间件写入的 Actor 转换为数据访问范围。
// 所有者只来自可信服务端上下文，绝不读取客户端提交的 user_id。
func ownerScopeFromContext(c *gin.Context) (accessdomain.OwnerScope, bool) {
	identity, found := authenticatedIdentityFromContext(c)
	if !found {
		return accessdomain.OwnerScope{}, false
	}

	scope, err := accessdomain.NewOwnerScope(identity.Actor.UserID)
	if err != nil {
		return accessdomain.OwnerScope{}, false
	}
	return scope, true
}
