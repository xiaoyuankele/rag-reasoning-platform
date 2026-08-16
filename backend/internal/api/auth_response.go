package api

import (
	"net/http"
	"time"

	"github.com/gin-gonic/gin"

	userdomain "rag-reasoning-platform/backend/internal/domain/user"
)

const sessionCookieName = "rag_session"

// publicUserResponse 是注册、登录和当前用户接口共用的公开 User DTO。
// 它不包含 password_hash 等仅限后端使用的字段。
type publicUserResponse struct {
	ID          int64             `json:"id"`
	Email       *string           `json:"email"`
	Phone       *string           `json:"phone"`
	DisplayName string            `json:"display_name"`
	Status      userdomain.Status `json:"status"`
	CreatedAt   time.Time         `json:"created_at"`
}

// newPublicUserResponse 把领域 User 转成稳定的 HTTP 响应契约。
func newPublicUserResponse(user userdomain.User) publicUserResponse {
	return publicUserResponse{
		ID:          user.ID,
		Email:       user.Email,
		Phone:       user.PhoneE164,
		DisplayName: user.DisplayName,
		Status:      user.Status,
		CreatedAt:   user.CreatedAt.UTC(),
	}
}

// authSessionResponse 是注册和登录成功时共用的响应形状。
type authSessionResponse struct {
	User             publicUserResponse `json:"user"`
	SessionExpiresAt time.Time          `json:"session_expires_at"`
}

// writeAuthSessionResponse 在事务成功后设置 Cookie 并返回公开用户信息。
func writeAuthSessionResponse(
	c *gin.Context,
	statusCode int,
	user userdomain.User,
	rawToken string,
	expiresAt time.Time,
	cookieSecure bool,
) {
	http.SetCookie(
		c.Writer,
		&http.Cookie{
			Name:     sessionCookieName,
			Value:    rawToken,
			Path:     "/",
			Expires:  expiresAt.UTC(),
			HttpOnly: true,
			Secure:   cookieSecure,
			SameSite: http.SameSiteLaxMode,
		},
	)
	c.JSON(
		statusCode,
		authSessionResponse{
			User:             newPublicUserResponse(user),
			SessionExpiresAt: expiresAt.UTC(),
		},
	)
}

// clearSessionCookie 让浏览器立即删除 rag_session。
func clearSessionCookie(c *gin.Context, cookieSecure bool) {
	http.SetCookie(
		c.Writer,
		&http.Cookie{
			Name:     sessionCookieName,
			Value:    "",
			Path:     "/",
			Expires:  time.Unix(1, 0).UTC(),
			MaxAge:   -1,
			HttpOnly: true,
			Secure:   cookieSecure,
			SameSite: http.SameSiteLaxMode,
		},
	)
}
