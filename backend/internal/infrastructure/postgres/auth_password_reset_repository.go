package postgres

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	authapplication "rag-reasoning-platform/backend/internal/application/auth"
	authdomain "rag-reasoning-platform/backend/internal/domain/auth"
)

// AuthPasswordResetRepository 使用 PostgreSQL 原子更新密码并撤销旧 Session。
type AuthPasswordResetRepository struct {
	pool *pgxpool.Pool
}

var _ authapplication.PasswordResetRepository = (*AuthPasswordResetRepository)(nil)

// NewAuthPasswordResetRepository 创建密码重置事务仓储。
func NewAuthPasswordResetRepository(
	pool *pgxpool.Pool,
) *AuthPasswordResetRepository {
	return &AuthPasswordResetRepository{pool: pool}
}

// FindVerificationChallenge 复用注册仓储已经验证过的挑战查询能力。
func (r *AuthPasswordResetRepository) FindVerificationChallenge(
	ctx context.Context,
	challengeID int64,
) (authdomain.VerificationChallenge, error) {
	return NewAuthRegistrationRepository(r.pool).FindVerificationChallenge(
		ctx,
		challengeID,
	)
}

// IncrementVerificationAttempts 原子记录一次错误验证码尝试。
func (r *AuthPasswordResetRepository) IncrementVerificationAttempts(
	ctx context.Context,
	challengeID int64,
	attemptedAt time.Time,
) (int, error) {
	return NewAuthRegistrationRepository(r.pool).IncrementVerificationAttempts(
		ctx,
		challengeID,
		attemptedAt,
	)
}

// ResetPassword 在一个事务内更新密码、消费挑战并撤销该用户全部 Session。
func (r *AuthPasswordResetRepository) ResetPassword(
	ctx context.Context,
	record authapplication.PasswordResetRecord,
) error {
	transaction, err := r.pool.Begin(ctx)
	if err != nil {
		return fmt.Errorf("begin password reset transaction: %w", err)
	}
	defer func() {
		_ = transaction.Rollback(ctx)
	}()

	challenge, err := lockPasswordResetChallenge(
		ctx,
		transaction,
		record.ChallengeID,
	)
	if err != nil {
		return err
	}
	if err := validateLockedPasswordResetChallenge(challenge, record); err != nil {
		return err
	}

	userID, err := updatePasswordResetAccount(
		ctx,
		transaction,
		challenge,
		record,
	)
	if err != nil {
		return err
	}

	commandTag, err := transaction.Exec(
		ctx,
		`
			UPDATE verification_challenges
			SET consumed_at = $2, updated_at = $2
			WHERE id = $1 AND consumed_at IS NULL
		`,
		challenge.ID,
		record.ResetAt,
	)
	if err != nil {
		return fmt.Errorf("consume password reset challenge: %w", err)
	}
	if commandTag.RowsAffected() != 1 {
		return authapplication.ErrVerificationCodeInvalid
	}

	if _, err := transaction.Exec(
		ctx,
		`
			UPDATE user_sessions
			SET revoked_at = $2
			WHERE user_id = $1 AND revoked_at IS NULL
		`,
		userID,
		record.ResetAt,
	); err != nil {
		return fmt.Errorf("revoke sessions after password reset: %w", err)
	}

	if err := transaction.Commit(ctx); err != nil {
		return fmt.Errorf("commit password reset transaction: %w", err)
	}
	return nil
}

func lockPasswordResetChallenge(
	ctx context.Context,
	transaction pgx.Tx,
	challengeID int64,
) (authdomain.VerificationChallenge, error) {
	challenge, err := scanVerificationChallenge(
		transaction.QueryRow(
			ctx,
			`
				SELECT
					id, channel, destination, purpose, code_hash,
					expires_at, consumed_at, attempt_count, send_count,
					last_sent_at, created_at, updated_at
				FROM verification_challenges
				WHERE id = $1
				FOR UPDATE
			`,
			challengeID,
		),
	)
	if errors.Is(err, pgx.ErrNoRows) {
		return authdomain.VerificationChallenge{}, authapplication.ErrVerificationCodeInvalid
	}
	if err != nil {
		return authdomain.VerificationChallenge{}, fmt.Errorf(
			"lock password reset challenge: %w",
			err,
		)
	}
	return challenge, nil
}

func validateLockedPasswordResetChallenge(
	challenge authdomain.VerificationChallenge,
	record authapplication.PasswordResetRecord,
) error {
	if challenge.Purpose != authdomain.VerificationPurposePasswordReset ||
		!challenge.Channel.IsValid() || challenge.ConsumedAt != nil ||
		challenge.LastSentAt == nil || challenge.SendCount <= 0 ||
		challenge.CodeHash != record.ExpectedChallengeCodeHash {
		return authapplication.ErrVerificationCodeInvalid
	}
	if !challenge.ExpiresAt.After(record.ResetAt) {
		return authapplication.ErrVerificationCodeExpired
	}
	if challenge.AttemptCount >= authdomain.MaxVerificationAttempts {
		return authapplication.ErrVerificationAttemptsExceeded
	}
	return nil
}

func updatePasswordResetAccount(
	ctx context.Context,
	transaction pgx.Tx,
	challenge authdomain.VerificationChallenge,
	record authapplication.PasswordResetRecord,
) (int64, error) {
	var query string
	switch challenge.Channel {
	case authdomain.VerificationChannelEmail:
		query = `
			UPDATE users
			SET password_hash = $1, updated_at = $2
			WHERE email = $3
			  AND email_verified_at IS NOT NULL
			  AND status = 'active'
			RETURNING id
		`
	case authdomain.VerificationChannelSMS:
		query = `
			UPDATE users
			SET password_hash = $1, updated_at = $2
			WHERE phone_e164 = $3
			  AND phone_verified_at IS NOT NULL
			  AND status = 'active'
			RETURNING id
		`
	default:
		return 0, authapplication.ErrVerificationCodeInvalid
	}

	var userID int64
	err := transaction.QueryRow(
		ctx,
		query,
		record.PasswordHash,
		record.ResetAt,
		challenge.Destination,
	).Scan(&userID)
	if errors.Is(err, pgx.ErrNoRows) {
		return 0, authapplication.ErrPasswordResetAccountNotFound
	}
	if err != nil {
		return 0, fmt.Errorf("update password reset account: %w", err)
	}
	return userID, nil
}
