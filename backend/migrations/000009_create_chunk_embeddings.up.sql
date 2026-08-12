BEGIN;

-- vector 是数据库级扩展，固定安装到 public，供业务 schema 和隔离测试 schema 使用。
CREATE EXTENSION IF NOT EXISTS vector WITH SCHEMA public;

-- 第一版每个 chunk 只保存一条当前有效向量，不保留多模型历史。
-- 模型名称和维度由 embedding_job 统一记录，避免在每个 chunk 上重复保存同一事实。
CREATE TABLE chunk_embeddings (
    chunk_id BIGINT PRIMARY KEY
        REFERENCES text_chunks (id)
        ON DELETE CASCADE,

    embedding_job_id BIGINT NOT NULL
        REFERENCES embedding_jobs (id)
        ON DELETE CASCADE,

    -- 当前第一版固定为 text-embedding-3-small 的 1536 维输出。
    -- 未来改变维度时必须通过受控迁移清空并全量重建，不能混合比较不同维度。
    embedding public.vector(1536) NOT NULL,

    created_at TIMESTAMPTZ NOT NULL DEFAULT CURRENT_TIMESTAMP,
    updated_at TIMESTAMPTZ NOT NULL DEFAULT CURRENT_TIMESTAMP
);

-- 删除 embedding job 或按 job 核对本次写入结果时，需要快速找到其全部向量。
CREATE INDEX idx_chunk_embeddings_job_id
    ON chunk_embeddings (embedding_job_id);

-- 当前数据规模先使用 pgvector 精确检索，不提前创建 HNSW/IVFFlat 近似索引。

COMMIT;
