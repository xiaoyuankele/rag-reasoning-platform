package postgres

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/jackc/pgx/v5/pgxpool"

	authapplication "rag-reasoning-platform/backend/internal/application/auth"
	authdomain "rag-reasoning-platform/backend/internal/domain/auth"
	userdomain "rag-reasoning-platform/backend/internal/domain/user"
)

// AuthRegistrationRepository 使用 PostgreSQL 完成注册相关的原子持久化。
type AuthRegistrationRepository struct {
	pool *pgxpool.Pool
}

var _ authapplication.RegistrationRepository = (*AuthRegistrationRepository)(nil)

// NewAuthRegistrationRepository 创建注册事务仓储。
func NewAuthRegistrationRepository(pool *pgxpool.Pool) *AuthRegistrationRepository {
	return &AuthRegistrationRepository{pool: pool}
}

// FindVerificationChallenge 按服务端生成的 ID 查找验证码挑战。
func (r *AuthRegistrationRepository) FindVerificationChallenge(
	ctx context.Context,
	challengeID int64,
) (authdomain.VerificationChallenge, error) {
	const query = `
		SELECT
			id, channel, destination, purpose, code_hash,
			expires_at, consumed_at, attempt_count, send_count,
			last_sent_at, created_at, updated_at
		FROM verification_challenges
		WHERE id = $1
	`

	challenge, err := scanVerificationChallenge(
		r.pool.QueryRow(ctx, query, challengeID),
	)
	if errors.Is(err, pgx.ErrNoRows) {
		return authdomain.VerificationChallenge{}, authdomain.ErrVerificationChallengeNotFound
	}
	if err != nil {
		return authdomain.VerificationChallenge{}, fmt.Errorf(
			"find verification challenge by ID: %w",
			err,
		)
	}
	return challenge, nil
}

// IncrementVerificationAttempts 原子增加错误验证码尝试次数。
func (r *AuthRegistrationRepository) IncrementVerificationAttempts(
	ctx context.Context,
	challengeID int64,
	attemptedAt time.Time,
) (int, error) {
	const query = `
		UPDATE verification_challenges
		SET
			attempt_count = attempt_count + 1,
			updated_at = $2
		WHERE id = $1
		  AND consumed_at IS NULL
		  AND send_count > 0
		  AND last_sent_at IS NOT NULL
		  AND expires_at > $2
		  AND attempt_count < $3
		RETURNING attempt_count
	`

	var attemptCount int
	err := r.pool.QueryRow(
		ctx,
		query,
		challengeID,
		attemptedAt,
		authdomain.MaxVerificationAttempts,
	).Scan(&attemptCount)
	if errors.Is(err, pgx.ErrNoRows) {
		return 0, authapplication.ErrVerificationCodeInvalid
	}
	if err != nil {
		return 0, fmt.Errorf("increment verification attempts: %w", err)
	}
	return attemptCount, nil
}

// CreateRegistration 在一个事务内创建用户、消费挑战并创建 Session。
func (r *AuthRegistrationRepository) CreateRegistration(
	ctx context.Context,
	record authapplication.RegistrationRecord,
) (authapplication.RegistrationResult, error) {
	transaction, err := r.pool.Begin(ctx)
	if err != nil {
		return authapplication.RegistrationResult{}, fmt.Errorf(
			"begin registration transaction: %w",
			err,
		)
	}
	defer func() {
		_ = transaction.Rollback(ctx)
	}()

	challenge, err := lockRegistrationChallenge(
		ctx,
		transaction,
		record.ChallengeID,
	)
	if err != nil {
		return authapplication.RegistrationResult{}, err
	}
	if err := validateLockedRegistrationChallenge(challenge, record); err != nil {
		return authapplication.RegistrationResult{}, err
	}

	createdUser, err := insertRegisteredUser(
		ctx,
		transaction,
		challenge,
		record,
	)
	if err != nil {
		return authapplication.RegistrationResult{}, err
	}

	commandTag, err := transaction.Exec(
		ctx,
		`
			UPDATE verification_challenges
			SET consumed_at = $2, updated_at = $2
			WHERE id = $1 AND consumed_at IS NULL
		`,
		challenge.ID,
		record.RegisteredAt,
	)
	if err != nil {
		return authapplication.RegistrationResult{}, fmt.Errorf(
			"consume registration challenge: %w",
			err,
		)
	}
	if commandTag.RowsAffected() != 1 {
		return authapplication.RegistrationResult{}, authapplication.ErrVerificationCodeInvalid
	}

	createdSession, err := insertUserSession(
		ctx,
		transaction,
		createdUser.ID,
		record,
	)
	if err != nil {
		return authapplication.RegistrationResult{}, err
	}

	if err := transaction.Commit(ctx); err != nil {
		return authapplication.RegistrationResult{}, fmt.Errorf(
			"commit registration transaction: %w",
			err,
		)
	}

	return authapplication.RegistrationResult{
		User:    createdUser,
		Session: createdSession,
	}, nil
}

func lockRegistrationChallenge(
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
			"lock registration challenge: %w",
			err,
		)
	}
	return challenge, nil
}

func validateLockedRegistrationChallenge(
	challenge authdomain.VerificationChallenge,
	record authapplication.RegistrationRecord,
) error {
	if challenge.Purpose != authdomain.VerificationPurposeRegister ||
		!challenge.Channel.IsValid() || challenge.ConsumedAt != nil ||
		challenge.LastSentAt == nil || challenge.SendCount <= 0 ||
		challenge.CodeHash != record.ExpectedChallengeCodeHash {
		return authapplication.ErrVerificationCodeInvalid
	}
	if !challenge.ExpiresAt.After(record.RegisteredAt) {
		return authapplication.ErrVerificationCodeExpired
	}
	if challenge.AttemptCount >= authdomain.MaxVerificationAttempts {
		return authapplication.ErrVerificationAttemptsExceeded
	}
	return nil
}

func insertRegisteredUser(
	ctx context.Context,
	transaction pgx.Tx,
	challenge authdomain.VerificationChallenge,
	record authapplication.RegistrationRecord,
) (userdomain.User, error) {
	var email any
	var phone any
	var emailVerifiedAt any
	var phoneVerifiedAt any
	switch challenge.Channel {
	case authdomain.VerificationChannelEmail:
		email = challenge.Destination
		emailVerifiedAt = record.RegisteredAt
	case authdomain.VerificationChannelSMS:
		phone = challenge.Destination
		phoneVerifiedAt = record.RegisteredAt
	default:
		return userdomain.User{}, authapplication.ErrVerificationCodeInvalid
	}

	var createdUser userdomain.User
	var status string
	err := transaction.QueryRow(
		ctx,
		`
			INSERT INTO users (
				email, phone_e164, email_verified_at, phone_verified_at,
				display_name, password_hash, status, created_at, updated_at
			)
			VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $8)
			RETURNING
				id, email, phone_e164, email_verified_at, phone_verified_at,
				display_name, status, created_at, updated_at
		`,
		email,
		phone,
		emailVerifiedAt,
		phoneVerifiedAt,
		record.DisplayName,
		record.PasswordHash,
		userdomain.StatusActive,
		record.RegisteredAt,
	).Scan(
		&createdUser.ID,
		&createdUser.Email,
		&createdUser.PhoneE164,
		&createdUser.EmailVerifiedAt,
		&createdUser.PhoneVerifiedAt,
		&createdUser.DisplayName,
		&status,
		&createdUser.CreatedAt,
		&createdUser.UpdatedAt,
	)
	if err != nil {
		var postgresError *pgconn.PgError
		if errors.As(err, &postgresError) && postgresError.Code == "23505" &&
			(postgresError.ConstraintName == "uq_users_email" ||
				postgresError.ConstraintName == "uq_users_phone_e164") {
			return userdomain.User{}, authapplication.ErrContactAlreadyRegistered
		}
		return userdomain.User{}, fmt.Errorf("insert registered user: %w", err)
	}

	createdUser.Status = userdomain.Status(status)
	if !createdUser.Status.IsValid() {
		return userdomain.User{}, fmt.Errorf("invalid stored user status %q", status)
	}
	return createdUser, nil
}

func insertUserSession(
	ctx context.Context,
	transaction pgx.Tx,
	userID int64,
	record authapplication.RegistrationRecord,
) (authdomain.Session, error) {
	var createdSession authdomain.Session
	err := transaction.QueryRow(
		ctx,
		`
			INSERT INTO user_sessions (
				user_id, token_hash, expires_at, created_at
			)
			VALUES ($1, $2, $3, $4)
			RETURNING id, user_id, token_hash, expires_at, revoked_at, created_at
		`,
		userID,
		record.SessionTokenHash,
		record.SessionExpiresAt,
		record.RegisteredAt,
	).Scan(
		&createdSession.ID,
		&createdSession.UserID,
		&createdSession.TokenHash,
		&createdSession.ExpiresAt,
		&createdSession.RevokedAt,
		&createdSession.CreatedAt,
	)
	if err != nil {
		return authdomain.Session{}, fmt.Errorf("insert registration session: %w", err)
	}
	return createdSession, nil
}
