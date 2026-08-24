BEGIN;

-- answer_jobs 保存可跨请求、跨页面刷新恢复的异步问答任务。
-- 问题和答案属于用户私有数据，只允许通过 OwnerScope 查询；日志不得记录正文。
CREATE TABLE answer_jobs (
    id BIGINT GENERATED ALWAYS AS IDENTITY PRIMARY KEY,

    owner_user_id BIGINT NOT NULL
        REFERENCES users (id)
        ON DELETE CASCADE,

    -- 指定文档问答随文档删除；NULL 表示在当前用户全部可用语料中检索。
    document_id BIGINT
        REFERENCES documents (id)
        ON DELETE CASCADE,

    query TEXT NOT NULL,
    top_k INTEGER NOT NULL,
    requested_response_language TEXT NOT NULL,

    status TEXT NOT NULL DEFAULT 'queued',
    attempt_count INTEGER NOT NULL DEFAULT 0,
    error_code TEXT,
    error_message TEXT,
    next_attempt_at TIMESTAMPTZ NOT NULL DEFAULT CURRENT_TIMESTAMP,

    answer_text TEXT,
    resolved_response_language TEXT,
    sources JSONB,
    prompt_tokens INTEGER,
    completion_tokens INTEGER,
    total_tokens INTEGER,

    created_at TIMESTAMPTZ NOT NULL DEFAULT CURRENT_TIMESTAMP,
    updated_at TIMESTAMPTZ NOT NULL DEFAULT CURRENT_TIMESTAMP,
    started_at TIMESTAMPTZ,
    completed_at TIMESTAMPTZ,

    CONSTRAINT answer_jobs_query_check
        CHECK (
            LENGTH(BTRIM(query)) > 0
            AND CHAR_LENGTH(query) <= 1000
        ),
    CONSTRAINT answer_jobs_top_k_check
        CHECK (top_k BETWEEN 1 AND 20),
    CONSTRAINT answer_jobs_requested_language_check
        CHECK (requested_response_language IN ('auto', 'zh', 'en')),
    CONSTRAINT answer_jobs_status_check
        CHECK (status IN ('queued', 'processing', 'succeeded', 'failed', 'canceled')),
    CONSTRAINT answer_jobs_attempt_count_check
        CHECK (attempt_count >= 0),
    CONSTRAINT answer_jobs_resolved_language_check
        CHECK (
            resolved_response_language IS NULL
            OR resolved_response_language IN ('zh', 'en')
        ),
    CONSTRAINT answer_jobs_token_usage_check
        CHECK (
            (prompt_tokens IS NULL AND completion_tokens IS NULL AND total_tokens IS NULL)
            OR (
                prompt_tokens >= 0
                AND completion_tokens >= 0
                AND total_tokens >= 0
                AND prompt_tokens + completion_tokens = total_tokens
            )
        ),
    CONSTRAINT answer_jobs_success_result_check
        CHECK (
            status <> 'succeeded'
            OR (
                answer_text IS NOT NULL
                AND resolved_response_language IS NOT NULL
                AND sources IS NOT NULL
                AND prompt_tokens IS NOT NULL
            )
        ),
    CONSTRAINT answer_jobs_updated_at_check
        CHECK (updated_at >= created_at)
);

CREATE INDEX idx_answer_jobs_queue_claim
    ON answer_jobs (next_attempt_at, created_at, id)
    WHERE status = 'queued';

CREATE INDEX idx_answer_jobs_owner_status_created
    ON answer_jobs (owner_user_id, status, created_at DESC, id DESC);

-- 调度表只保存每个 Owner 最近一次获得任务的时间，不复制任务状态。
CREATE TABLE answer_owner_schedules (
    owner_user_id BIGINT PRIMARY KEY
        REFERENCES users (id)
        ON DELETE CASCADE,
    last_dispatched_at TIMESTAMPTZ,
    created_at TIMESTAMPTZ NOT NULL DEFAULT CURRENT_TIMESTAMP,
    updated_at TIMESTAMPTZ NOT NULL DEFAULT CURRENT_TIMESTAMP,
    CONSTRAINT answer_owner_schedules_updated_at_check
        CHECK (updated_at >= created_at)
);

CREATE INDEX idx_answer_owner_schedules_last_dispatched
    ON answer_owner_schedules (
        last_dispatched_at ASC NULLS FIRST,
        owner_user_id
    );

COMMIT;
