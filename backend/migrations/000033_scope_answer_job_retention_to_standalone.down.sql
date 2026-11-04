BEGIN;

DROP INDEX IF EXISTS idx_answer_jobs_standalone_owner_created;
DROP INDEX IF EXISTS idx_answer_jobs_standalone_terminal_completed;

CREATE INDEX idx_answer_jobs_terminal_completed
    ON answer_jobs (completed_at ASC, id ASC)
    WHERE status IN ('succeeded', 'failed', 'canceled');

COMMIT;
