BEGIN;

-- canceled 是用户主动终止等待中或排队中任务后的稳定终态。
-- 它不属于活动任务，因此不会阻止同一文档以后重新申请向量化。
ALTER TABLE embedding_jobs
    DROP CONSTRAINT embedding_jobs_status_check;

ALTER TABLE embedding_jobs
    ADD CONSTRAINT embedding_jobs_status_check
        CHECK (status IN (
            'waiting_document',
            'queued',
            'processing',
            'succeeded',
            'failed',
            'canceled'
        ));

COMMIT;
