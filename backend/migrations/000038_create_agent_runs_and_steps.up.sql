BEGIN;

-- agent_runs 保存会话内可恢复、可审计的有界 Agent 执行。
-- OwnerScope、conversation/message 归属和与 Answer Job 的跨表互斥由创建事务在
-- 锁定同一 conversations 行后验证；浏览器或模型提供的用户身份永不可信。
CREATE TABLE agent_runs (
    id BIGINT GENERATED ALWAYS AS IDENTITY PRIMARY KEY,

    owner_user_id BIGINT NOT NULL
        REFERENCES users (id)
        ON DELETE CASCADE,
    conversation_id BIGINT NOT NULL
        REFERENCES conversations (id)
        ON DELETE CASCADE,
    user_message_id BIGINT NOT NULL
        REFERENCES conversation_messages (id)
        ON DELETE CASCADE,
    assistant_message_id BIGINT NOT NULL
        REFERENCES conversation_messages (id)
        ON DELETE CASCADE,

    client_run_id TEXT NOT NULL,
    requested_document_id BIGINT,
    response_language TEXT NOT NULL DEFAULT 'auto',

    status TEXT NOT NULL DEFAULT 'queued',
    step_count INTEGER NOT NULL DEFAULT 0,
    max_steps INTEGER NOT NULL,
    max_total_tokens INTEGER NOT NULL,
    deadline_at TIMESTAMPTZ NOT NULL,

    attempt_count INTEGER NOT NULL DEFAULT 0,
    next_attempt_at TIMESTAMPTZ NOT NULL DEFAULT CURRENT_TIMESTAMP,
    worker_id TEXT,
    lease_token TEXT,
    lease_expires_at TIMESTAMPTZ,
    heartbeat_at TIMESTAMPTZ,

    error_code TEXT,
    error_message TEXT,
    result_json JSONB,
    prompt_tokens INTEGER,
    completion_tokens INTEGER,
    total_tokens INTEGER,

    created_at TIMESTAMPTZ NOT NULL DEFAULT CURRENT_TIMESTAMP,
    updated_at TIMESTAMPTZ NOT NULL DEFAULT CURRENT_TIMESTAMP,
    started_at TIMESTAMPTZ,
    completed_at TIMESTAMPTZ,

    CONSTRAINT agent_runs_client_run_id_check
        CHECK (
            LENGTH(BTRIM(client_run_id)) BETWEEN 1 AND 128
        ),
    CONSTRAINT agent_runs_requested_document_id_check
        CHECK (
            requested_document_id IS NULL
            OR requested_document_id > 0
        ),
    CONSTRAINT agent_runs_response_language_check
        CHECK (response_language IN ('auto', 'zh', 'en')),
    CONSTRAINT agent_runs_status_check
        CHECK (status IN ('queued', 'processing', 'succeeded', 'failed', 'canceled')),
    CONSTRAINT agent_runs_step_budget_check
        CHECK (
            max_steps BETWEEN 1 AND 5
            AND step_count BETWEEN 0 AND max_steps
        ),
    CONSTRAINT agent_runs_token_budget_check
        CHECK (max_total_tokens BETWEEN 1 AND 48000),
    CONSTRAINT agent_runs_deadline_check
        CHECK (deadline_at > created_at),
    CONSTRAINT agent_runs_attempt_count_check
        CHECK (
            attempt_count BETWEEN 0 AND 2
            AND (status <> 'processing' OR attempt_count > 0)
        ),
    CONSTRAINT agent_runs_error_code_check
        CHECK (
            error_code IS NULL
            OR error_code IN (
                'agent_execution_failed',
                'agent_temporarily_unavailable',
                'agent_step_limit_exceeded',
                'agent_token_budget_exceeded',
                'agent_deadline_exceeded',
                'agent_invalid_tool_call',
                'agent_tool_failed',
                'agent_worker_interrupted'
            )
        ),
    CONSTRAINT agent_runs_error_shape_check
        CHECK (
            (error_code IS NULL AND error_message IS NULL)
            OR (
                error_code IS NOT NULL
                AND error_message IS NOT NULL
                AND error_message = BTRIM(error_message)
                AND OCTET_LENGTH(error_message) BETWEEN 1 AND 1000
            )
        ),
    CONSTRAINT agent_runs_token_usage_check
        CHECK (
            (prompt_tokens IS NULL AND completion_tokens IS NULL AND total_tokens IS NULL)
            OR (
                prompt_tokens >= 0
                AND completion_tokens >= 0
                AND total_tokens >= 0
                AND prompt_tokens + completion_tokens = total_tokens
                AND total_tokens <= max_total_tokens
            )
        ),
    CONSTRAINT agent_runs_terminal_time_check
        CHECK (
            (status IN ('queued', 'processing') AND completed_at IS NULL)
            OR (status IN ('succeeded', 'failed', 'canceled') AND completed_at IS NOT NULL)
        ),
    CONSTRAINT agent_runs_success_result_check
        CHECK (
            (
                status = 'succeeded'
                AND result_json IS NOT NULL
                AND step_count > 0
                AND total_tokens IS NOT NULL
            )
            OR (status <> 'succeeded' AND result_json IS NULL)
        ),
    CONSTRAINT agent_runs_result_size_check
        CHECK (
            result_json IS NULL
            OR OCTET_LENGTH(result_json::TEXT) <= 32768
        ),
    CONSTRAINT agent_runs_failure_error_check
        CHECK (
            status <> 'failed'
            OR (
                error_code IS NOT NULL
                AND LENGTH(BTRIM(error_code)) > 0
                AND error_message IS NOT NULL
                AND LENGTH(BTRIM(error_message)) > 0
            )
        ),
    CONSTRAINT agent_runs_lease_fields_check
        CHECK (
            (
                status = 'processing'
                AND worker_id IS NOT NULL
                AND LENGTH(BTRIM(worker_id)) > 0
                AND lease_token IS NOT NULL
                AND LENGTH(BTRIM(lease_token)) > 0
                AND lease_expires_at IS NOT NULL
                AND heartbeat_at IS NOT NULL
            )
            OR (
                status <> 'processing'
                AND worker_id IS NULL
                AND lease_token IS NULL
                AND lease_expires_at IS NULL
                AND heartbeat_at IS NULL
            )
        ),
    CONSTRAINT agent_runs_started_time_check
        CHECK (
            (status = 'processing' AND started_at IS NOT NULL)
            OR status <> 'processing'
        ),
    CONSTRAINT agent_runs_updated_at_check
        CHECK (updated_at >= created_at)
);

CREATE UNIQUE INDEX uq_agent_runs_conversation_client_run
    ON agent_runs (conversation_id, client_run_id);

CREATE UNIQUE INDEX uq_agent_runs_user_message
    ON agent_runs (user_message_id);

CREATE UNIQUE INDEX uq_agent_runs_assistant_message
    ON agent_runs (assistant_message_id);

CREATE UNIQUE INDEX uq_agent_runs_active_conversation
    ON agent_runs (conversation_id)
    WHERE status IN ('queued', 'processing');

CREATE INDEX idx_agent_runs_queue_claim
    ON agent_runs (next_attempt_at, created_at, id)
    WHERE status = 'queued';

CREATE INDEX idx_agent_runs_owner_status_created
    ON agent_runs (owner_user_id, status, created_at DESC, id DESC);

CREATE INDEX idx_agent_runs_conversation_latest
    ON agent_runs (conversation_id, id DESC);

CREATE INDEX idx_agent_runs_expired_lease
    ON agent_runs (lease_expires_at, id)
    WHERE status = 'processing';

-- lease_token 是 fencing token；任何 processing Agent Run 都必须具有独立 token。
CREATE UNIQUE INDEX uq_agent_runs_active_lease_token
    ON agent_runs (lease_token)
    WHERE lease_token IS NOT NULL;

-- agent_steps 保存模型已经决定并对外可审计的 action，不保存思维链、完整 Prompt
-- 或 Provider 原始响应。arguments/result 由 Application 进一步执行 Tool Schema 校验。
CREATE TABLE agent_steps (
    id BIGINT GENERATED ALWAYS AS IDENTITY PRIMARY KEY,
    agent_run_id BIGINT NOT NULL
        REFERENCES agent_runs (id)
        ON DELETE CASCADE,
    sequence_no INTEGER NOT NULL,
    kind TEXT NOT NULL,
    status TEXT NOT NULL DEFAULT 'pending',
    tool_name TEXT,
    arguments_json JSONB,
    result_json JSONB,
    error_code TEXT,
    error_message TEXT,
    prompt_tokens INTEGER,
    completion_tokens INTEGER,
    total_tokens INTEGER,
    created_at TIMESTAMPTZ NOT NULL DEFAULT CURRENT_TIMESTAMP,
    updated_at TIMESTAMPTZ NOT NULL DEFAULT CURRENT_TIMESTAMP,
    started_at TIMESTAMPTZ,
    completed_at TIMESTAMPTZ,

    CONSTRAINT agent_steps_sequence_check
        CHECK (sequence_no > 0),
    CONSTRAINT agent_steps_kind_check
        CHECK (kind IN ('tool_call', 'final')),
    CONSTRAINT agent_steps_status_check
        CHECK (status IN ('pending', 'running', 'succeeded', 'failed')),
    CONSTRAINT agent_steps_error_code_check
        CHECK (
            error_code IS NULL
            OR error_code IN (
                'agent_execution_failed',
                'agent_temporarily_unavailable',
                'agent_step_limit_exceeded',
                'agent_token_budget_exceeded',
                'agent_deadline_exceeded',
                'agent_invalid_tool_call',
                'agent_tool_failed',
                'agent_worker_interrupted'
            )
        ),
    CONSTRAINT agent_steps_error_shape_check
        CHECK (
            (error_code IS NULL AND error_message IS NULL)
            OR (
                error_code IS NOT NULL
                AND error_message IS NOT NULL
                AND error_message = BTRIM(error_message)
                AND OCTET_LENGTH(error_message) BETWEEN 1 AND 1000
            )
        ),
    CONSTRAINT agent_steps_tool_shape_check
        CHECK (
            (
                kind = 'tool_call'
                AND tool_name IS NOT NULL
                AND LENGTH(BTRIM(tool_name)) BETWEEN 1 AND 100
                AND tool_name = BTRIM(tool_name)
                AND OCTET_LENGTH(tool_name) <= 100
                AND arguments_json IS NOT NULL
            )
            OR (
                kind = 'final'
                AND tool_name IS NULL
                AND arguments_json IS NULL
            )
        ),
    CONSTRAINT agent_steps_arguments_size_check
        CHECK (
            arguments_json IS NULL
            OR OCTET_LENGTH(arguments_json::TEXT) <= 8192
        ),
    CONSTRAINT agent_steps_result_size_check
        CHECK (
            result_json IS NULL
            OR OCTET_LENGTH(result_json::TEXT) <= 32768
        ),
    CONSTRAINT agent_steps_token_usage_check
        CHECK (
            (prompt_tokens IS NULL AND completion_tokens IS NULL AND total_tokens IS NULL)
            OR (
                prompt_tokens >= 0
                AND completion_tokens >= 0
                AND total_tokens >= 0
                AND prompt_tokens + completion_tokens = total_tokens
            )
        ),
    CONSTRAINT agent_steps_status_time_check
        CHECK (
            (status = 'pending' AND started_at IS NULL AND completed_at IS NULL)
            OR (status = 'running' AND started_at IS NOT NULL AND completed_at IS NULL)
            OR (status IN ('succeeded', 'failed') AND started_at IS NOT NULL AND completed_at IS NOT NULL)
        ),
    CONSTRAINT agent_steps_success_result_check
        CHECK (status <> 'succeeded' OR result_json IS NOT NULL),
    CONSTRAINT agent_steps_failure_error_check
        CHECK (
            status <> 'failed'
            OR (
                error_code IS NOT NULL
                AND LENGTH(BTRIM(error_code)) > 0
                AND error_message IS NOT NULL
                AND LENGTH(BTRIM(error_message)) > 0
            )
        ),
    CONSTRAINT agent_steps_state_payload_check
        CHECK (
            (
                status IN ('pending', 'running')
                AND result_json IS NULL
                AND error_code IS NULL
                AND error_message IS NULL
                AND prompt_tokens IS NULL
                AND completion_tokens IS NULL
                AND total_tokens IS NULL
            )
            OR (
                status = 'succeeded'
                AND result_json IS NOT NULL
                AND error_code IS NULL
                AND error_message IS NULL
                AND prompt_tokens IS NOT NULL
                AND completion_tokens IS NOT NULL
                AND total_tokens IS NOT NULL
            )
            OR (
                status = 'failed'
                AND result_json IS NULL
                AND error_code IS NOT NULL
                AND error_message IS NOT NULL
            )
        ),
    CONSTRAINT agent_steps_updated_at_check
        CHECK (updated_at >= created_at),
    CONSTRAINT uq_agent_steps_run_sequence
        UNIQUE (agent_run_id, sequence_no)
);

CREATE UNIQUE INDEX uq_agent_steps_active_run
    ON agent_steps (agent_run_id)
    WHERE status IN ('pending', 'running');

CREATE UNIQUE INDEX uq_agent_steps_final_run
    ON agent_steps (agent_run_id)
    WHERE kind = 'final';

CREATE INDEX idx_agent_steps_run_sequence
    ON agent_steps (agent_run_id, sequence_no ASC, id ASC);

-- 调度表只保存每个 Owner 最近一次获得 Agent Run 的时间，不复制 Run 状态。
CREATE TABLE agent_owner_schedules (
    owner_user_id BIGINT PRIMARY KEY
        REFERENCES users (id)
        ON DELETE CASCADE,
    last_dispatched_at TIMESTAMPTZ,
    created_at TIMESTAMPTZ NOT NULL DEFAULT CURRENT_TIMESTAMP,
    updated_at TIMESTAMPTZ NOT NULL DEFAULT CURRENT_TIMESTAMP,
    CONSTRAINT agent_owner_schedules_updated_at_check
        CHECK (updated_at >= created_at)
);

CREATE INDEX idx_agent_owner_schedules_last_dispatched
    ON agent_owner_schedules (
        last_dispatched_at ASC NULLS FIRST,
        owner_user_id
    );

COMMIT;
