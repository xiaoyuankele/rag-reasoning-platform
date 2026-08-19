BEGIN;

-- waiting_document 保存“用户已经申请向量化，但文档文本块尚未就绪”的持久意图。
-- Embedding Worker 仍然只领取 queued；解析成功事务负责把等待任务激活。
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

-- waiting_document 也属于活动任务，避免用户在解析完成前重复保存相同意图。
DROP INDEX uq_embedding_jobs_active;

CREATE UNIQUE INDEX uq_embedding_jobs_active
    ON embedding_jobs (document_id)
    WHERE status IN ('waiting_document', 'queued', 'processing');

COMMIT;
