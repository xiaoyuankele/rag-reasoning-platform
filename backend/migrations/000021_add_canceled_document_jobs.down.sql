BEGIN;

DROP INDEX IF EXISTS idx_document_jobs_document_latest;

-- 旧版本不认识 canceled。回滚结构前转成 failed，保留任务历史。
UPDATE document_jobs
SET
    status = 'failed',
    error_message = COALESCE(
        error_message,
        'document processing job canceled before migration rollback'
    ),
    updated_at = CURRENT_TIMESTAMP,
    completed_at = COALESCE(completed_at, CURRENT_TIMESTAMP)
WHERE status = 'canceled';

ALTER TABLE document_jobs
    DROP CONSTRAINT document_jobs_status_check;

ALTER TABLE document_jobs
    ADD CONSTRAINT document_jobs_status_check
        CHECK (status IN (
            'queued',
            'processing',
            'succeeded',
            'failed'
        ));

COMMIT;
