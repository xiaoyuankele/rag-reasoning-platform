BEGIN;

-- 按依赖关系的反方向删除：Session 依赖 User，必须先删除 Session。
DROP TABLE IF EXISTS user_sessions;
DROP TABLE IF EXISTS verification_challenges;
DROP TABLE IF EXISTS users;

COMMIT;
