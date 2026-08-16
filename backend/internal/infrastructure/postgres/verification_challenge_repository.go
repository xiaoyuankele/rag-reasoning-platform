package postgres

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	verificationapplication "rag-reasoning-platform/backend/internal/application/verification"
	authdomain "rag-reasoning-platform/backend/internal/domain/auth"
)

// VerificationChallengeRepository 使用 PostgreSQL 保存验证码挑战。
type VerificationChallengeRepository struct {
	pool *pgxpool.Pool
}

var _ verificationapplication.Repository = (*VerificationChallengeRepository)(nil)

// NewVerificationChallengeRepository 创建 PostgreSQL 验证码挑战仓储。
func NewVerificationChallengeRepository(
	pool *pgxpool.Pool,
) *VerificationChallengeRepository {
	return &VerificationChallengeRepository{pool: pool}
}

// FindLatest 查找相同渠道、地址和用途的最近一次挑战。
func (r *VerificationChallengeRepository) FindLatest(
	ctx context.Context,
	channel authdomain.VerificationChannel,
	destination string,
	purpose authdomain.VerificationPurpose,
) (authdomain.VerificationChallenge, error) {
	const query = `
		SELECT
			id,
			channel,
			destination,
			purpose,
			code_hash,
			expires_at,
			consumed_at,
			attempt_count,
			send_count,
			last_sent_at,
			created_at,
			updated_at
		FROM verification_challenges
		WHERE channel = $1
		  AND destination = $2
		  AND purpose = $3
		ORDER BY created_at DESC, id DESC
		LIMIT 1
	`

	foundChallenge, err := scanVerificationChallenge(
		r.pool.QueryRow(
			ctx,
			query,
			channel,
			destination,
			purpose,
		),
	)
	if errors.Is(err, pgx.ErrNoRows) {
		return authdomain.VerificationChallenge{}, authdomain.ErrVerificationChallengeNotFound
	}
	if err != nil {
		return authdomain.VerificationChallenge{}, fmt.Errorf(
			"find latest verification challenge: %w",
			err,
		)
	}

	return foundChallenge, nil
}

// Create 保存一条尚未确认发送成功的验证码挑战。
func (r *VerificationChallengeRepository) Create(
	ctx context.Context,
	challenge authdomain.VerificationChallenge,
	resendCooldown time.Duration,
) (authdomain.VerificationChallenge, error) {
	transaction, err := r.pool.Begin(ctx)
	if err != nil {
		return authdomain.VerificationChallenge{}, fmt.Errorf(
			"begin verification challenge transaction: %w",
			err,
		)
	}
	defer func() {
		_ = transaction.Rollback(ctx)
	}()

	// 相同渠道、地址和用途使用同一个事务级 advisory lock。
	// 它把并发的“检查冷却 + 创建”串行化，避免同时发送多条验证码。
	lockKey := verificationAdvisoryLockKey(
		challenge.Channel,
		challenge.Destination,
		challenge.Purpose,
	)
	if _, err := transaction.Exec(
		ctx,
		"SELECT pg_advisory_xact_lock(hashtextextended($1, 0))",
		lockKey,
	); err != nil {
		return authdomain.VerificationChallenge{}, fmt.Errorf(
			"lock verification destination: %w",
			err,
		)
	}

	var latestReservationAt time.Time
	if err := transaction.QueryRow(
		ctx,
		`
			SELECT COALESCE(last_sent_at, created_at)
			FROM verification_challenges
			WHERE channel = $1
			  AND destination = $2
			  AND purpose = $3
			ORDER BY created_at DESC, id DESC
			LIMIT 1
		`,
		challenge.Channel,
		challenge.Destination,
		challenge.Purpose,
	).Scan(&latestReservationAt); err != nil && !errors.Is(err, pgx.ErrNoRows) {
		return authdomain.VerificationChallenge{}, fmt.Errorf(
			"check verification resend cooldown: %w",
			err,
		)
	} else if err == nil {
		// pending 挑战也占用一次发送名额，防止第一个请求尚未 MarkSent 时
		// 第二个并发请求再次创建并发送。
		retryAt := latestReservationAt.Add(resendCooldown)
		if challenge.CreatedAt.Before(retryAt) {
			return authdomain.VerificationChallenge{},
				&verificationapplication.CooldownError{RetryAt: retryAt}
		}
	}

	const query = `
		INSERT INTO verification_challenges (
			channel,
			destination,
			purpose,
			code_hash,
			expires_at,
			consumed_at,
			attempt_count,
			send_count,
			last_sent_at,
			created_at,
			updated_at
		)
		VALUES (
			$1, $2, $3, $4, $5, $6,
			$7, $8, $9, $10, $11
		)
		RETURNING
			id,
			channel,
			destination,
			purpose,
			code_hash,
			expires_at,
			consumed_at,
			attempt_count,
			send_count,
			last_sent_at,
			created_at,
			updated_at
	`

	createdChallenge, err := scanVerificationChallenge(
		transaction.QueryRow(
			ctx,
			query,
			challenge.Channel,
			challenge.Destination,
			challenge.Purpose,
			challenge.CodeHash,
			challenge.ExpiresAt,
			challenge.ConsumedAt,
			challenge.AttemptCount,
			challenge.SendCount,
			challenge.LastSentAt,
			challenge.CreatedAt,
			challenge.UpdatedAt,
		),
	)
	if err != nil {
		return authdomain.VerificationChallenge{}, fmt.Errorf(
			"create verification challenge: %w",
			err,
		)
	}

	if err := transaction.Commit(ctx); err != nil {
		return authdomain.VerificationChallenge{}, fmt.Errorf(
			"commit verification challenge: %w",
			err,
		)
	}

	return createdChallenge, nil
}

// MarkSent 把一条仍有效的 pending 挑战标记为已经发送。
func (r *VerificationChallengeRepository) MarkSent(
	ctx context.Context,
	challengeID int64,
	sentAt time.Time,
) (authdomain.VerificationChallenge, error) {
	const query = `
		UPDATE verification_challenges
		SET
			send_count = 1,
			last_sent_at = $2,
			updated_at = $2
		WHERE id = $1
		  AND send_count = 0
		  AND last_sent_at IS NULL
		  AND consumed_at IS NULL
		  AND expires_at > $2
		RETURNING
			id,
			channel,
			destination,
			purpose,
			code_hash,
			expires_at,
			consumed_at,
			attempt_count,
			send_count,
			last_sent_at,
			created_at,
			updated_at
	`

	markedChallenge, err := scanVerificationChallenge(
		r.pool.QueryRow(ctx, query, challengeID, sentAt),
	)
	if errors.Is(err, pgx.ErrNoRows) {
		return authdomain.VerificationChallenge{}, authdomain.ErrVerificationChallengeNotFound
	}
	if err != nil {
		return authdomain.VerificationChallenge{}, fmt.Errorf(
			"mark verification challenge sent: %w",
			err,
		)
	}

	return markedChallenge, nil
}

// verificationAdvisoryLockKey 为同一验证码接收目标生成稳定、无歧义的文本锁键。
//
// PostgreSQL 的 text 类型不能包含 NUL 字节，因此这里不能使用 \x00 分隔字段。
// 长度前缀既只产生合法文本，又能区分 ("ab", "c") 和 ("a", "bc") 这类
// 直接拼接后相同、实际字段边界不同的输入。len 返回 UTF-8 字节数，正好对应
// PostgreSQL 实际接收到的字节序列长度。
func verificationAdvisoryLockKey(
	channel authdomain.VerificationChannel,
	destination string,
	purpose authdomain.VerificationPurpose,
) string {
	channelText := string(channel)
	purposeText := string(purpose)

	return fmt.Sprintf(
		"%d:%s%d:%s%d:%s",
		len(channelText),
		channelText,
		len(destination),
		destination,
		len(purposeText),
		purposeText,
	)
}

func scanVerificationChallenge(
	row pgx.Row,
) (authdomain.VerificationChallenge, error) {
	var challenge authdomain.VerificationChallenge
	var channel string
	var purpose string

	err := row.Scan(
		&challenge.ID,
		&channel,
		&challenge.Destination,
		&purpose,
		&challenge.CodeHash,
		&challenge.ExpiresAt,
		&challenge.ConsumedAt,
		&challenge.AttemptCount,
		&challenge.SendCount,
		&challenge.LastSentAt,
		&challenge.CreatedAt,
		&challenge.UpdatedAt,
	)
	if err != nil {
		return authdomain.VerificationChallenge{}, err
	}

	challenge.Channel = authdomain.VerificationChannel(channel)
	if !challenge.Channel.IsValid() {
		return authdomain.VerificationChallenge{}, fmt.Errorf(
			"invalid verification channel %q",
			channel,
		)
	}

	challenge.Purpose = authdomain.VerificationPurpose(purpose)
	if !challenge.Purpose.IsValid() {
		return authdomain.VerificationChallenge{}, fmt.Errorf(
			"invalid verification purpose %q",
			purpose,
		)
	}

	return challenge, nil
}
