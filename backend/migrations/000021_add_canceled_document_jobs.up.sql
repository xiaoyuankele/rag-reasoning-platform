BEGIN;

-- canceled 表示用户在 Worker 领取前主动终止了解析任务。
-- canceled 是稳定终态，不再计入 queued/processing 活动任务容量。
ALTER TABLE document_jobs
    DROP CONSTRAINT document_jobs_status_check;

ALTER TABLE document_jobs
    ADD CONSTRAINT document_jobs_status_check
        CHECK (status IN (
            'queued',
            'processing',
            'succeeded',
            'failed',
            'canceled'
        ));

-- 支持按照一批 document_id 查询每份文档最新的解析任务。
CREATE INDEX IF NOT EXISTS idx_document_jobs_document_latest
    ON document_jobs (document_id, id DESC);

COMMIT;
