BEGIN;

-- answer_jobs 的行锁只覆盖领取事务。以下字段把任务所有权延长到
-- 检索、远程生成和答案落库期间，允许多个 Answer Worker 进程安全协作。
ALTER TABLE answer_jobs
    ADD COLUMN worker_id TEXT,
    ADD COLUMN lease_token TEXT,
    ADD COLUMN lease_expires_at TIMESTAMPTZ,
    ADD COLUMN heartbeat_at TIMESTAMPTZ;

-- 恢复流程只扫描 processing 中已经到期的任务。
CREATE INDEX idx_answer_jobs_expired_lease
    ON answer_jobs (lease_expires_at, id)
    WHERE status = 'processing';

-- lease_token 是 fencing token；同一时刻不能属于两条 processing 任务。
CREATE UNIQUE INDEX uq_answer_jobs_active_lease_token
    ON answer_jobs (lease_token)
    WHERE lease_token IS NOT NULL;

COMMIT;
