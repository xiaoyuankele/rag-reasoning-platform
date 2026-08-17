BEGIN;

-- Release B 在历史数据已经显式认领后关闭迁移窗口。
-- 如果仍存在 owner_user_id IS NULL 的记录，PostgreSQL 会拒绝本次 ALTER，
-- 整条迁移保持未应用，服务也不会在不完整的数据边界上继续启动。
ALTER TABLE documents
    ALTER COLUMN owner_user_id SET NOT NULL;

COMMIT;
