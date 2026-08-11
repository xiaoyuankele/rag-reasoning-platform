BEGIN;

DROP INDEX idx_text_chunks_content_trgm;

-- 故意保留 pg_trgm 扩展：它是数据库级共享能力，可能已被其他索引使用。
-- 回滚本迁移只删除本项目创建的业务索引，不破坏其他数据库对象。

COMMIT;
