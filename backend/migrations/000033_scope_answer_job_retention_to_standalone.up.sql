BEGIN;

DROP INDEX idx_answer_jobs_terminal_completed;

CREATE INDEX idx_answer_jobs_standalone_terminal_completed
    ON answer_jobs (completed_at ASC, id ASC)
    WHERE conversation_id IS NULL
      AND status IN ('succeeded', 'failed', 'canceled');

CREATE INDEX idx_answer_jobs_standalone_owner_created
    ON answer_jobs (owner_user_id, created_at DESC, id DESC)
    WHERE conversation_id IS NULL;

COMMIT;
