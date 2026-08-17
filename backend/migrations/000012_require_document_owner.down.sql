BEGIN;

-- 回滚只重新允许 NULL，不会主动移除任何已有文档的所有者。
ALTER TABLE documents
    ALTER COLUMN owner_user_id DROP NOT NULL;

COMMIT;
