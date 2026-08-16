package api

import (
	"time"

	userdomain "rag-reasoning-platform/backend/internal/domain/user"
)

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
