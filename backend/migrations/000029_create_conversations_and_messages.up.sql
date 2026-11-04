BEGIN;

CREATE TABLE conversations (
    id BIGINT GENERATED ALWAYS AS IDENTITY PRIMARY KEY,
    owner_user_id BIGINT NOT NULL
        REFERENCES users (id)
        ON DELETE CASCADE,
    title TEXT NOT NULL DEFAULT 'New conversation'
        CHECK (LENGTH(BTRIM(title)) BETWEEN 1 AND 200),
    status TEXT NOT NULL DEFAULT 'active'
        CHECK (status IN ('active', 'archived')),
    last_message_at TIMESTAMPTZ,
    created_at TIMESTAMPTZ NOT NULL DEFAULT CURRENT_TIMESTAMP,
    updated_at TIMESTAMPTZ NOT NULL DEFAULT CURRENT_TIMESTAMP,
    CONSTRAINT conversations_updated_at_check CHECK (updated_at >= created_at)
);

CREATE INDEX idx_conversations_owner_updated
    ON conversations (owner_user_id, updated_at DESC, id DESC);

CREATE TABLE conversation_messages (
    id BIGINT GENERATED ALWAYS AS IDENTITY PRIMARY KEY,
    conversation_id BIGINT NOT NULL
        REFERENCES conversations (id)
        ON DELETE CASCADE,
    sequence_no BIGINT NOT NULL CHECK (sequence_no > 0),
    role TEXT NOT NULL
        CHECK (role IN ('user', 'assistant')),
    content TEXT NOT NULL
        CHECK (LENGTH(BTRIM(content)) > 0),
    status TEXT NOT NULL DEFAULT 'completed'
        CHECK (status IN ('completed', 'generating', 'failed', 'interrupted')),
    client_message_id TEXT,
    created_at TIMESTAMPTZ NOT NULL DEFAULT CURRENT_TIMESTAMP,
    updated_at TIMESTAMPTZ NOT NULL DEFAULT CURRENT_TIMESTAMP,
    CONSTRAINT conversation_messages_updated_at_check
        CHECK (updated_at >= created_at),
    CONSTRAINT conversation_messages_client_id_check
        CHECK (client_message_id IS NULL OR LENGTH(BTRIM(client_message_id)) BETWEEN 1 AND 128),
    CONSTRAINT uq_conversation_message_sequence
        UNIQUE (conversation_id, sequence_no)
);

CREATE UNIQUE INDEX uq_conversation_user_client_message
    ON conversation_messages (conversation_id, client_message_id)
    WHERE client_message_id IS NOT NULL;

CREATE INDEX idx_conversation_messages_conversation_sequence
    ON conversation_messages (conversation_id, sequence_no ASC, id ASC);

COMMIT;
