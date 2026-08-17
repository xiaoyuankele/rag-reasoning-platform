BEGIN;

-- 验证码用途是安全边界：注册验证码不能用于重置密码，反之亦然。
ALTER TABLE verification_challenges
    DROP CONSTRAINT verification_challenges_purpose_check;

ALTER TABLE verification_challenges
    ADD CONSTRAINT verification_challenges_purpose_check
    CHECK (purpose IN ('register', 'password_reset'));

COMMIT;
