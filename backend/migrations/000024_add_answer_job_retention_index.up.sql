BEGIN;

-- 保留期清理只扫描已结束任务；排队和执行中的任务永远不进入该索引。
CREATE INDEX idx_answer_jobs_terminal_completed
    ON answer_jobs (completed_at ASC, id ASC)
    WHERE status IN ('succeeded', 'failed', 'canceled');

COMMIT;
