BEGIN;

-- document_processing_owner_schedules 只保存 Owner 调度游标。
-- 正式任务状态仍以 document_jobs 为唯一事实来源，避免复制 processing 计数。
CREATE TABLE document_processing_owner_schedules (
    owner_user_id BIGINT PRIMARY KEY
        REFERENCES users (id)
        ON DELETE CASCADE,

    last_dispatched_at TIMESTAMPTZ,
    created_at TIMESTAMPTZ NOT NULL DEFAULT CURRENT_TIMESTAMP,
    updated_at TIMESTAMPTZ NOT NULL DEFAULT CURRENT_TIMESTAMP,

    CONSTRAINT document_processing_owner_schedules_updated_at_check
        CHECK (updated_at >= created_at)
);

-- 为迁移执行前仍有 queued/processing 任务的 Owner 补齐调度游标。
-- last_dispatched_at 使用该 Owner 最近一次 started_at；从未开始过任务时保持 NULL。
INSERT INTO document_processing_owner_schedules (
    owner_user_id,
    last_dispatched_at
)
SELECT
    source_document.owner_user_id,
    MAX(job.started_at)
FROM document_jobs AS job
JOIN documents AS source_document
  ON source_document.id = job.document_id
WHERE job.status IN ('queued', 'processing')
GROUP BY source_document.owner_user_id
ON CONFLICT (owner_user_id) DO NOTHING;

-- 调度首先比较防饥饿状态和最老 queued 时间，再比较最近派发时间。
-- 当前规模下主键已经足以锁定 Owner；该索引帮助按最近派发时间排序。
CREATE INDEX idx_document_processing_owner_schedules_last_dispatched
    ON document_processing_owner_schedules (
        last_dispatched_at ASC NULLS FIRST,
        owner_user_id
    );

COMMIT;
