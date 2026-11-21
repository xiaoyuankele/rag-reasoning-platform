BEGIN;

LOCK TABLE answer_jobs IN ACCESS EXCLUSIVE MODE;

DO $$
BEGIN
    IF EXISTS (
        SELECT 1
        FROM answer_jobs
        WHERE requested_document_id IS DISTINCT FROM document_id
    ) THEN
        RAISE EXCEPTION
            'cannot drop requested_document_id: live document scope has already been cleared'
            USING ERRCODE = '23514';
    END IF;
END;
$$;

ALTER TABLE answer_jobs
    DROP COLUMN IF EXISTS requested_document_id;

COMMIT;
