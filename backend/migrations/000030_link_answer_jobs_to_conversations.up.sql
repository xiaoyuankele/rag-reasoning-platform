BEGIN;

ALTER TABLE answer_jobs
    ADD COLUMN conversation_id BIGINT
        REFERENCES conversations (id)
        ON DELETE CASCADE,
    ADD COLUMN user_message_id BIGINT
        REFERENCES conversation_messages (id)
        ON DELETE CASCADE,
    ADD COLUMN assistant_message_id BIGINT
        REFERENCES conversation_messages (id)
        ON DELETE CASCADE;

ALTER TABLE answer_jobs
    ADD CONSTRAINT answer_jobs_conversation_link_check
    CHECK (
        (conversation_id IS NULL AND user_message_id IS NULL AND assistant_message_id IS NULL)
        OR (
            conversation_id IS NOT NULL
            AND user_message_id IS NOT NULL
            AND assistant_message_id IS NOT NULL
        )
    );

CREATE UNIQUE INDEX uq_answer_jobs_active_conversation
    ON answer_jobs (conversation_id)
    WHERE conversation_id IS NOT NULL
      AND status IN ('queued', 'processing');

CREATE INDEX idx_answer_jobs_user_message
    ON answer_jobs (user_message_id)
    WHERE user_message_id IS NOT NULL;

COMMIT;
