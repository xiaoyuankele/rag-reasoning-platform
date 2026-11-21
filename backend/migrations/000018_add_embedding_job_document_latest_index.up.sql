BEGIN;

-- 支持按一批 document_id 查询每份文档最新的向量任务。
-- document_id 是外键，但 PostgreSQL 不会自动为外键列创建查询索引。
CREATE INDEX IF NOT EXISTS idx_embedding_jobs_document_latest
    ON embedding_jobs (document_id, id DESC);

COMMIT;
