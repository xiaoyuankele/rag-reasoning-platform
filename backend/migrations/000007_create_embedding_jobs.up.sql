-- embedding_jobs 保存“把一份文档的文本块转换为向量”的异步任务。
-- 它与 document_jobs 分开，避免文本解析失败和向量生成失败相互覆盖状态。
CREATE TABLE IF NOT EXISTS embedding_jobs (
    id BIGINT GENERATED ALWAYS AS IDENTITY PRIMARY KEY,

    document_id BIGINT NOT NULL
        REFERENCES documents (id)
        ON DELETE CASCADE,

    -- model_name 冻结任务创建时选用的模型，避免配置变化后无法追溯向量来源。
    model_name TEXT NOT NULL
        CHECK (LENGTH(BTRIM(model_name)) > 0),

    -- dimensions 冻结本次任务期望的向量维度。
    dimensions INTEGER NOT NULL
        CHECK (dimensions > 0),

    status TEXT NOT NULL DEFAULT 'queued'
        CHECK (status IN ('queued', 'processing', 'succeeded', 'failed')),

    attempt_count INTEGER NOT NULL DEFAULT 0
        CHECK (attempt_count >= 0),

    error_message TEXT,
    created_at TIMESTAMPTZ NOT NULL DEFAULT CURRENT_TIMESTAMP,
    updated_at TIMESTAMPTZ NOT NULL DEFAULT CURRENT_TIMESTAMP,
    started_at TIMESTAMPTZ,
    completed_at TIMESTAMPTZ
);

-- 第一版同一份文档同时最多只有一个排队中或执行中的向量任务。
-- 已完成或失败的历史任务不会阻止用户重新创建任务。
CREATE UNIQUE INDEX IF NOT EXISTS uq_embedding_jobs_active
    ON embedding_jobs (document_id)
    WHERE status IN ('queued', 'processing');

-- 为后续 Embedding Worker 按创建时间领取最早任务提供索引。
CREATE INDEX IF NOT EXISTS idx_embedding_jobs_status_created_at
    ON embedding_jobs (status, created_at, id);
