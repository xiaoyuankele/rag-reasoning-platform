BEGIN;

CREATE INDEX idx_answer_jobs_conversation_latest
    ON answer_jobs (conversation_id, id DESC)
    WHERE conversation_id IS NOT NULL;

COMMIT;
