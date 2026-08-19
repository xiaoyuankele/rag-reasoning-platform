BEGIN;

-- 旧版本不认识 canceled。回滚结构前将其转换为 failed，保留历史记录。
UPDATE embedding_jobs
SET
    status = 'failed',
    error_message = COALESCE(
        error_message,
        'embedding job canceled before migration rollback'
    ),
    updated_at = CURRENT_TIMESTAMP
WHERE status = 'canceled';

ALTER TABLE embedding_jobs
    DROP CONSTRAINT embedding_jobs_status_check;

ALTER TABLE embedding_jobs
    ADD CONSTRAINT embedding_jobs_status_check
        CHECK (status IN (
            'waiting_document',
            'queued',
            'processing',
            'succeeded',
            'failed'
        ));

COMMIT;
