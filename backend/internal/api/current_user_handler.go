package api

import (
	"net/http"

	"github.com/gin-gonic/gin"
)

// CurrentUserHandler 返回 Middleware 已经验证的当前用户。
type CurrentUserHandler struct{}

// NewCurrentUserHandler 创建当前用户 Handler。
func NewCurrentUserHandler() *CurrentUserHandler {
	return &CurrentUserHandler{}
}

// RegisterRoutes 在已经挂载 AuthMiddleware 的路由组中注册 /me。
func (h *CurrentUserHandler) RegisterRoutes(routes gin.IRoutes) {
	routes.GET("/me", h.Get)
}

// Get 处理 GET /users/me。
func (h *CurrentUserHandler) Get(c *gin.Context) {
	identity, found := authenticatedIdentityFromContext(c)
	if !found {
		writeAuthenticationRequired(c)
		return
	}
	c.JSON(http.StatusOK, gin.H{"user": newPublicUserResponse(identity.User)})
}
