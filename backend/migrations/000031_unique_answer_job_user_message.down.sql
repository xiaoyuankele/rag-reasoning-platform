BEGIN;

DROP INDEX IF EXISTS uq_answer_jobs_user_message;

CREATE INDEX idx_answer_jobs_user_message
    ON answer_jobs (user_message_id)
    WHERE user_message_id IS NOT NULL;

COMMIT;
