BEGIN;

-- pg_trgm 把文本拆成 trigram（连续三字符片段），为中英文子字符串查询
-- 提供 GIN 操作类。扩展固定安装到 public，使业务 schema 和隔离测试 schema
-- 都可以显式引用同一个数据库级能力。
CREATE EXTENSION IF NOT EXISTS pg_trgm WITH SCHEMA public;

-- 该索引支持 Repository 中的 ILIKE '%keyword%' 字面子串查询。
-- 创建普通索引会短暂阻塞对 text_chunks 的写入；项目当前数据量小，
-- 后续真实生产零停机迁移需要单独设计 CONCURRENTLY 流程。
CREATE INDEX idx_text_chunks_content_trgm
    ON text_chunks
    USING GIN (content public.gin_trgm_ops);

COMMIT;
