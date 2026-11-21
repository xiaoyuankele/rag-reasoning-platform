BEGIN;

DROP INDEX idx_answer_jobs_user_message;

CREATE UNIQUE INDEX uq_answer_jobs_user_message
    ON answer_jobs (user_message_id)
    WHERE user_message_id IS NOT NULL;

COMMIT;
