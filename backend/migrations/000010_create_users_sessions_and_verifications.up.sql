BEGIN;

-- users 保存个人账户。
-- 未完成验证的邮箱或手机号只存在于 verification_challenges，不能提前写入 users。
CREATE TABLE users (
    id BIGINT GENERATED ALWAYS AS IDENTITY PRIMARY KEY,

    -- P6 允许邮箱或手机号二选一注册，因此两列分别可空。
    email TEXT,
    phone_e164 TEXT,
    email_verified_at TIMESTAMPTZ,
    phone_verified_at TIMESTAMPTZ,

    display_name TEXT NOT NULL
        CHECK (LENGTH(BTRIM(display_name)) BETWEEN 1 AND 100),

    -- 这里只保存带算法、参数、salt 和结果的 Argon2id 编码值。
    password_hash TEXT NOT NULL
        CHECK (LENGTH(BTRIM(password_hash)) > 0),

    status TEXT NOT NULL DEFAULT 'active'
        CHECK (status IN ('active', 'disabled')),

    created_at TIMESTAMPTZ NOT NULL DEFAULT CURRENT_TIMESTAMP,
    updated_at TIMESTAMPTZ NOT NULL DEFAULT CURRENT_TIMESTAMP,

    -- 邮箱必须由 Application 规范化成“去除两端空白 + 小写”后才能入库。
    CONSTRAINT users_email_normalized_check CHECK (
        email IS NULL
        OR (
            email = LOWER(BTRIM(email))
            AND LENGTH(email) BETWEEN 3 AND 320
        )
    ),

    -- 第一版手机号使用 E.164：加号、非零国家码和最多 15 位数字。
    CONSTRAINT users_phone_e164_check CHECK (
        phone_e164 IS NULL
        OR phone_e164 ~ '^\+[1-9][0-9]{7,14}$'
    ),

    -- 写入 users 的联系方式必须已经完成验证，不能只写地址而没有验证时间。
    CONSTRAINT users_email_verification_pair_check CHECK (
        (email IS NULL AND email_verified_at IS NULL)
        OR (email IS NOT NULL AND email_verified_at IS NOT NULL)
    ),
    CONSTRAINT users_phone_verification_pair_check CHECK (
        (phone_e164 IS NULL AND phone_verified_at IS NULL)
        OR (phone_e164 IS NOT NULL AND phone_verified_at IS NOT NULL)
    ),

    -- 每个账户至少绑定一种已验证联系方式。
    CONSTRAINT users_verified_contact_required_check CHECK (
        email IS NOT NULL OR phone_e164 IS NOT NULL
    ),

    CONSTRAINT users_updated_at_check CHECK (updated_at >= created_at)
);

-- PostgreSQL 的普通 UNIQUE 允许多个 NULL；部分唯一索引让非空联系方式保持全局唯一。
CREATE UNIQUE INDEX uq_users_email
    ON users (email)
    WHERE email IS NOT NULL;

CREATE UNIQUE INDEX uq_users_phone_e164
    ON users (phone_e164)
    WHERE phone_e164 IS NOT NULL;

-- user_sessions 保存服务端登录状态。
-- 浏览器持有原始随机 Token，数据库只保存它的 SHA-256 小写十六进制摘要。
CREATE TABLE user_sessions (
    id BIGINT GENERATED ALWAYS AS IDENTITY PRIMARY KEY,

    user_id BIGINT NOT NULL
        REFERENCES users (id)
        ON DELETE CASCADE,

    token_hash TEXT NOT NULL UNIQUE
        CHECK (token_hash ~ '^[0-9a-f]{64}$'),

    expires_at TIMESTAMPTZ NOT NULL,
    revoked_at TIMESTAMPTZ,
    created_at TIMESTAMPTZ NOT NULL DEFAULT CURRENT_TIMESTAMP,

    CONSTRAINT user_sessions_expiry_check CHECK (expires_at > created_at),
    CONSTRAINT user_sessions_revoked_at_check CHECK (
        revoked_at IS NULL OR revoked_at >= created_at
    )
);

-- 查询某个用户的活跃设备和批量撤销 Session 时使用。
CREATE INDEX idx_user_sessions_user_created_at
    ON user_sessions (user_id, created_at DESC, id DESC);

-- 清理过期 Session 时使用；是否已经过期仍由查询时与当前时间比较。
CREATE INDEX idx_user_sessions_expires_at
    ON user_sessions (expires_at);

-- verification_challenges 保存注册前的验证码挑战。
-- 它先于 User 出现，因此故意不依赖 users 表。
CREATE TABLE verification_challenges (
    id BIGINT GENERATED ALWAYS AS IDENTITY PRIMARY KEY,

    channel TEXT NOT NULL
        CHECK (channel IN ('email', 'sms')),

    destination TEXT NOT NULL
        CHECK (LENGTH(BTRIM(destination)) BETWEEN 3 AND 320),

    purpose TEXT NOT NULL
        CHECK (purpose IN ('register')),

    -- 保存带服务端密钥的 HMAC-SHA-256 摘要，不保存六位明文验证码。
    code_hash TEXT NOT NULL
        CHECK (code_hash ~ '^[0-9a-f]{64}$'),

    expires_at TIMESTAMPTZ NOT NULL,
    consumed_at TIMESTAMPTZ,

    attempt_count INTEGER NOT NULL DEFAULT 0
        CHECK (attempt_count BETWEEN 0 AND 5),

    send_count INTEGER NOT NULL DEFAULT 0
        CHECK (send_count >= 0),

    last_sent_at TIMESTAMPTZ,
    created_at TIMESTAMPTZ NOT NULL DEFAULT CURRENT_TIMESTAMP,
    updated_at TIMESTAMPTZ NOT NULL DEFAULT CURRENT_TIMESTAMP,

    CONSTRAINT verification_challenges_expiry_check CHECK (
        expires_at > created_at
    ),
    CONSTRAINT verification_challenges_email_normalized_check CHECK (
        channel <> 'email'
        OR (
            destination = LOWER(BTRIM(destination))
            AND LENGTH(destination) BETWEEN 3 AND 320
        )
    ),
    CONSTRAINT verification_challenges_phone_e164_check CHECK (
        channel <> 'sms'
        OR destination ~ '^\+[1-9][0-9]{7,14}$'
    ),
    CONSTRAINT verification_challenges_consumed_at_check CHECK (
        consumed_at IS NULL
        OR (consumed_at >= created_at AND consumed_at <= expires_at)
    ),
    CONSTRAINT verification_challenges_last_sent_at_check CHECK (
        last_sent_at IS NULL OR last_sent_at >= created_at
    ),
    CONSTRAINT verification_challenges_updated_at_check CHECK (
        updated_at >= created_at
    )
);

-- 创建挑战、判断重发冷却和排查滥用时，需要按规范化联系方式查找最近记录。
CREATE INDEX idx_verification_challenges_destination
    ON verification_challenges (
        channel,
        destination,
        purpose,
        created_at DESC,
        id DESC
    );

-- 定期清理已过期且未消费的挑战时使用。
CREATE INDEX idx_verification_challenges_open_expiry
    ON verification_challenges (expires_at)
    WHERE consumed_at IS NULL;

COMMIT;
