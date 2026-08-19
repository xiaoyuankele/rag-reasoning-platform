BEGIN;

-- 回滚前先把新状态收敛为旧版本能够识别的终态，避免恢复旧 CHECK 约束失败。
UPDATE embedding_jobs
SET
    status = 'failed',
    error_message = COALESCE(
        error_message,
        'embedding request canceled by migration rollback'
    ),
    updated_at = CURRENT_TIMESTAMP,
    completed_at = CURRENT_TIMESTAMP
WHERE status = 'waiting_document';

DROP INDEX uq_embedding_jobs_active;

ALTER TABLE embedding_jobs
    DROP CONSTRAINT embedding_jobs_status_check;

ALTER TABLE embedding_jobs
    ADD CONSTRAINT embedding_jobs_status_check
        CHECK (status IN ('queued', 'processing', 'succeeded', 'failed'));

CREATE UNIQUE INDEX uq_embedding_jobs_active
    ON embedding_jobs (document_id)
    WHERE status IN ('queued', 'processing');

COMMIT;
