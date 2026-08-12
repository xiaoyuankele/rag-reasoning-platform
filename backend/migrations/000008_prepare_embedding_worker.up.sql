BEGIN;

-- queued 任务只有到达 next_attempt_at 后才允许被 Worker 领取。
-- 新建任务和历史任务默认立即可执行；临时失败时 Repository 会把它改为未来时间。
ALTER TABLE embedding_jobs
    ADD COLUMN next_attempt_at TIMESTAMPTZ NOT NULL DEFAULT CURRENT_TIMESTAMP,
    ADD COLUMN prompt_tokens INTEGER,
    ADD COLUMN total_tokens INTEGER,
    ADD CONSTRAINT embedding_jobs_prompt_tokens_check
        CHECK (prompt_tokens IS NULL OR prompt_tokens >= 0),
    ADD CONSTRAINT embedding_jobs_total_tokens_check
        CHECK (total_tokens IS NULL OR total_tokens >= 0);

DROP INDEX idx_embedding_jobs_status_created_at;

-- 只索引 queued 任务，并优先领取已经到期、创建时间最早的任务。
CREATE INDEX idx_embedding_jobs_ready_queue
    ON embedding_jobs (next_attempt_at, created_at, id)
    WHERE status = 'queued';

COMMIT;
