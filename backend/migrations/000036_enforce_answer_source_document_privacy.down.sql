BEGIN;

DROP INDEX IF EXISTS idx_answer_jobs_standalone_succeeded_sources;
DROP INDEX IF EXISTS idx_answer_jobs_live_document;

CREATE OR REPLACE FUNCTION delete_standalone_answer_jobs_before_document()
RETURNS TRIGGER
LANGUAGE plpgsql
AS $$
BEGIN
    DELETE FROM answer_jobs
    WHERE document_id = OLD.id
      AND conversation_id IS NULL;
    RETURN OLD;
END;
$$;

COMMIT;
