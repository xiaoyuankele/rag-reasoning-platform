BEGIN;

DROP INDEX IF EXISTS uq_answer_jobs_active_lease_token;
DROP INDEX IF EXISTS idx_answer_jobs_expired_lease;

ALTER TABLE answer_jobs
    DROP COLUMN IF EXISTS heartbeat_at,
    DROP COLUMN IF EXISTS lease_expires_at,
    DROP COLUMN IF EXISTS lease_token,
    DROP COLUMN IF EXISTS worker_id;

COMMIT;
