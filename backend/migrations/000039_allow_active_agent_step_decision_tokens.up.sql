BEGIN;

-- PersistDecision must durably record the model decision and its Token usage
-- before a Tool may run. Active Steps still cannot contain a Tool result or
-- an error. The active-state branch keeps the Token triple all-null or
-- complete; agent_steps_token_usage_check enforces non-negative arithmetic.
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
            AND (
                (
                    prompt_tokens IS NULL
                    AND completion_tokens IS NULL
                    AND total_tokens IS NULL
                )
                OR (
                    prompt_tokens IS NOT NULL
                    AND completion_tokens IS NOT NULL
                    AND total_tokens IS NOT NULL
                )
            )
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
