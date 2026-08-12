BEGIN;

DROP INDEX idx_embedding_jobs_ready_queue;

CREATE INDEX idx_embedding_jobs_status_created_at
    ON embedding_jobs (status, created_at, id);

ALTER TABLE embedding_jobs
    DROP CONSTRAINT embedding_jobs_total_tokens_check,
    DROP CONSTRAINT embedding_jobs_prompt_tokens_check,
    DROP COLUMN total_tokens,
    DROP COLUMN prompt_tokens,
    DROP COLUMN next_attempt_at;

COMMIT;
