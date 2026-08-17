BEGIN;

-- 密码重置挑战是短生命周期临时数据。回滚功能前先删除它们，
-- 避免旧约束因历史 password_reset 值而无法恢复。
DELETE FROM verification_challenges
WHERE purpose = 'password_reset';

ALTER TABLE verification_challenges
    DROP CONSTRAINT verification_challenges_purpose_check;

ALTER TABLE verification_challenges
    ADD CONSTRAINT verification_challenges_purpose_check
    CHECK (purpose IN ('register'));

COMMIT;
