-- document_jobs 保存异步解析任务。
-- HTTP 请求只负责创建 queued 任务，后台 worker 再领取并执行。
CREATE TABLE IF NOT EXISTS document_jobs (
    id BIGINT GENERATED ALWAYS AS IDENTITY PRIMARY KEY,

    document_id BIGINT NOT NULL
        REFERENCES documents (id)
        ON DELETE CASCADE,

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

-- 同一文档同时最多只能有一个 queued 或 processing 任务。
CREATE UNIQUE INDEX IF NOT EXISTS uq_document_jobs_active
    ON document_jobs (document_id)
    WHERE status IN ('queued', 'processing');

-- worker 将按 queued 状态和创建时间领取最早任务。
CREATE INDEX IF NOT EXISTS idx_document_jobs_status_created_at
    ON document_jobs (status, created_at, id);
