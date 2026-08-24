BEGIN;

-- embedding_owner_schedules 只保存向量 Worker 的 Owner 调度游标。
-- 正式任务状态和 processing 数量仍以 embedding_jobs 为唯一事实来源。
CREATE TABLE embedding_owner_schedules (
    owner_user_id BIGINT PRIMARY KEY
        REFERENCES users (id)
        ON DELETE CASCADE,

    last_dispatched_at TIMESTAMPTZ,
    created_at TIMESTAMPTZ NOT NULL DEFAULT CURRENT_TIMESTAMP,
    updated_at TIMESTAMPTZ NOT NULL DEFAULT CURRENT_TIMESTAMP,

    CONSTRAINT embedding_owner_schedules_updated_at_check
        CHECK (updated_at >= created_at)
);

-- 为迁移执行前已有活动向量任务的 Owner 补齐调度游标。
-- waiting_document 以后可能转为 queued，因此也必须提前登记。
INSERT INTO embedding_owner_schedules (
    owner_user_id,
    last_dispatched_at
)
SELECT
    source_document.owner_user_id,
    MAX(job.started_at)
FROM embedding_jobs AS job
JOIN documents AS source_document
  ON source_document.id = job.document_id
WHERE job.status IN ('waiting_document', 'queued', 'processing')
GROUP BY source_document.owner_user_id
ON CONFLICT (owner_user_id) DO NOTHING;

CREATE INDEX idx_embedding_owner_schedules_last_dispatched
    ON embedding_owner_schedules (
        last_dispatched_at ASC NULLS FIRST,
        owner_user_id
    );

COMMIT;
