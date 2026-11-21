BEGIN;

CREATE INDEX idx_answer_jobs_live_document
    ON answer_jobs (document_id)
    WHERE document_id IS NOT NULL;

CREATE INDEX idx_answer_jobs_standalone_succeeded_sources
    ON answer_jobs USING GIN (sources jsonb_path_ops)
    WHERE conversation_id IS NULL
      AND status = 'succeeded'
      AND sources IS NOT NULL;

CREATE OR REPLACE FUNCTION delete_standalone_answer_jobs_before_document()
RETURNS TRIGGER
LANGUAGE plpgsql
AS $$
BEGIN
    DELETE FROM answer_jobs
    WHERE conversation_id IS NULL
      AND (
          document_id = OLD.id
          OR (
              status = 'succeeded'
              AND (
                  sources @> jsonb_build_array(
                      jsonb_build_object('document_id', OLD.id)
                  )
                  OR sources @> jsonb_build_array(
                      jsonb_build_object('DocumentID', OLD.id)
                  )
              )
          )
      );
    RETURN OLD;
END;
$$;

COMMIT;
