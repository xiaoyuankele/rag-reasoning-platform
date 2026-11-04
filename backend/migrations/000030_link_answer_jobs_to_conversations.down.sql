BEGIN;

DROP INDEX IF EXISTS idx_answer_jobs_user_message;
DROP INDEX IF EXISTS uq_answer_jobs_active_conversation;

ALTER TABLE answer_jobs
    DROP CONSTRAINT IF EXISTS answer_jobs_conversation_link_check,
    DROP COLUMN IF EXISTS assistant_message_id,
    DROP COLUMN IF EXISTS user_message_id,
    DROP COLUMN IF EXISTS conversation_id;

COMMIT;
