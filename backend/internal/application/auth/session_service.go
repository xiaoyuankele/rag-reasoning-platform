package auth

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"

	authdomain "rag-reasoning-platform/backend/internal/domain/auth"
	userdomain "rag-reasoning-platform/backend/internal/domain/user"
)

var (
	// ErrAuthenticationRequired 统一表示 Cookie 缺失、伪造、过期、撤销或账户不可用。
	ErrAuthenticationRequired = errors.New("authentication is required")

	// ErrSessionServiceDependencies 表示 Session 服务缺少必要端口。
	ErrSessionServiceDependencies = errors.New("session service dependencies are incomplete")
)

// Actor 是业务用例可以信任的最小请求身份。
// 它只能由 SessionService 恢复，绝不能从前端 JSON 或查询参数构造。
type Actor struct {
	UserID    int64
	SessionID int64
}

// AuthenticatedIdentity 保存一次已验证请求的 Actor 和公开用户资料。
// Middleware 把它放入请求上下文，/users/me 可以直接复用同一次数据库查询结果。
type AuthenticatedIdentity struct {
	Actor   Actor
	User    userdomain.User
	Session authdomain.Session
}

// SessionService 编排 Cookie Token 认证和幂等退出。
type SessionService struct {
	repository  SessionAuthenticationRepository
	tokenHasher SessionTokenHasher
	now         func() time.Time
}

// NewSessionService 创建 Session 认证服务。
func NewSessionService(
	repository SessionAuthenticationRepository,
	tokenHasher SessionTokenHasher,
	now func() time.Time,
) (*SessionService, error) {
	if repository == nil || tokenHasher == nil {
		return nil, ErrSessionServiceDependencies
	}
	if now == nil {
		now = time.Now
	}
	return &SessionService{
		repository:  repository,
		tokenHasher: tokenHasher,
		now:         now,
	}, nil
}

// Authenticate 把原始 Cookie Token 恢复成可信身份。
func (s *SessionService) Authenticate(
	ctx context.Context,
	rawToken string,
) (AuthenticatedIdentity, error) {
	if strings.TrimSpace(rawToken) == "" {
		return AuthenticatedIdentity{}, ErrAuthenticationRequired
	}
	tokenHash, err := s.tokenHasher.Hash(rawToken)
	if err != nil {
		return AuthenticatedIdentity{}, ErrAuthenticationRequired
	}

	now := s.now().UTC()
	identity, err := s.repository.FindAuthenticatedIdentity(ctx, tokenHash, now)
	if errors.Is(err, ErrAuthenticationRequired) {
		return AuthenticatedIdentity{}, ErrAuthenticationRequired
	}
	if err != nil {
		return AuthenticatedIdentity{}, fmt.Errorf("find authenticated identity: %w", err)
	}
	if identity.Actor.UserID <= 0 || identity.Actor.SessionID <= 0 ||
		identity.User.ID != identity.Actor.UserID ||
		identity.Session.ID != identity.Actor.SessionID ||
		identity.Session.UserID != identity.Actor.UserID ||
		!identity.Session.IsActive(now) ||
		!identity.User.Status.AllowsAuthentication() {
		return AuthenticatedIdentity{}, ErrAuthenticationRequired
	}

	return identity, nil
}

// Logout 按 Cookie Token 撤销 Session。
// 缺失或格式非法的 Token 按“已经退出”处理，因此该操作天然幂等。
func (s *SessionService) Logout(ctx context.Context, rawToken string) error {
	if strings.TrimSpace(rawToken) == "" {
		return nil
	}
	tokenHash, err := s.tokenHasher.Hash(rawToken)
	if err != nil {
		return nil
	}
	if err := s.repository.RevokeSession(ctx, tokenHash, s.now().UTC()); err != nil {
		return fmt.Errorf("revoke logout session: %w", err)
	}
	return nil
}
