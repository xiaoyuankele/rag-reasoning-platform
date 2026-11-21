BEGIN;

DO $$
BEGIN
    IF EXISTS (
        SELECT 1
        FROM agent_steps
        WHERE status IN ('pending', 'running')
          AND (
            prompt_tokens IS NOT NULL
            OR completion_tokens IS NOT NULL
            OR total_tokens IS NOT NULL
          )
    ) THEN
        RAISE EXCEPTION
            'cannot restore migration 38 while active Agent Steps contain decision Token usage'
            USING ERRCODE = 'check_violation';
    END IF;
END
$$;

ALTER TABLE agent_steps
    DROP CONSTRAINT agent_steps_state_payload_check;

ALTER TABLE agent_steps
    ADD CONSTRAINT agent_steps_state_payload_check
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
    );

COMMIT;
