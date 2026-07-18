-- 回滚第一份迁移。
-- 删除 documents 表时，属于该表的索引和约束也会一起删除。

BEGIN;

DROP TABLE IF EXISTS documents;

COMMIT;