package postgres

import (
	"context"
	"errors"
	"fmt"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	authapplication "rag-reasoning-platform/backend/internal/application/auth"
	authdomain "rag-reasoning-platform/backend/internal/domain/auth"
	userdomain "rag-reasoning-platform/backend/internal/domain/user"
)

// AuthSessionRepository 使用 PostgreSQL 查找登录账户并保存 Session。
type AuthSessionRepository struct {
	pool *pgxpool.Pool
}

var _ authapplication.LoginRepository = (*AuthSessionRepository)(nil)

// NewAuthSessionRepository 创建登录与 Session 仓储。
func NewAuthSessionRepository(pool *pgxpool.Pool) *AuthSessionRepository {
	return &AuthSessionRepository{pool: pool}
}

// FindLoginAccount 按规范化邮箱或 E.164 手机号读取账户与密码哈希。
func (r *AuthSessionRepository) FindLoginAccount(
	ctx context.Context,
	normalizedIdentifier string,
) (authapplication.LoginAccount, error) {
	const query = `
		SELECT
			id, email, phone_e164, email_verified_at, phone_verified_at,
			display_name, status, created_at, updated_at, password_hash
		FROM users
		WHERE email = $1 OR phone_e164 = $1
		LIMIT 1
	`

	var account authapplication.LoginAccount
	var status string
	err := r.pool.QueryRow(ctx, query, normalizedIdentifier).Scan(
		&account.User.ID,
		&account.User.Email,
		&account.User.PhoneE164,
		&account.User.EmailVerifiedAt,
		&account.User.PhoneVerifiedAt,
		&account.User.DisplayName,
		&status,
		&account.User.CreatedAt,
		&account.User.UpdatedAt,
		&account.PasswordHash,
	)
	if errors.Is(err, pgx.ErrNoRows) {
		return authapplication.LoginAccount{}, authapplication.ErrLoginAccountNotFound
	}
	if err != nil {
		return authapplication.LoginAccount{}, fmt.Errorf("find login account: %w", err)
	}

	account.User.Status = userdomain.Status(status)
	if !account.User.Status.IsValid() {
		return authapplication.LoginAccount{}, fmt.Errorf("invalid stored user status %q", status)
	}
	return account, nil
}

// CreateSession 只为当前仍处于 active 状态的用户创建 Session。
// INSERT ... SELECT 让状态检查和写入成为一个数据库语句，避免两者之间的竞态。
func (r *AuthSessionRepository) CreateSession(
	ctx context.Context,
	record authapplication.SessionRecord,
) (authdomain.Session, error) {
	const query = `
		INSERT INTO user_sessions (
			user_id,
			token_hash,
			expires_at,
			created_at
		)
		SELECT id, $2, $3, $4
		FROM users
		WHERE id = $1 AND status = 'active'
		RETURNING id, user_id, token_hash, expires_at, revoked_at, created_at
	`

	var createdSession authdomain.Session
	err := r.pool.QueryRow(
		ctx,
		query,
		record.UserID,
		record.TokenHash,
		record.ExpiresAt,
		record.CreatedAt,
	).Scan(
		&createdSession.ID,
		&createdSession.UserID,
		&createdSession.TokenHash,
		&createdSession.ExpiresAt,
		&createdSession.RevokedAt,
		&createdSession.CreatedAt,
	)
	if errors.Is(err, pgx.ErrNoRows) {
		return authdomain.Session{}, authapplication.ErrInvalidCredentials
	}
	if err != nil {
		return authdomain.Session{}, fmt.Errorf("insert user session: %w", err)
	}
	return createdSession, nil
}
