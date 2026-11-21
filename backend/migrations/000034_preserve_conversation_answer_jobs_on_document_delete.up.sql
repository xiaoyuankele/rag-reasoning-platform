BEGIN;

ALTER TABLE answer_jobs
    DROP CONSTRAINT answer_jobs_document_id_fkey;

ALTER TABLE answer_jobs
    ADD CONSTRAINT answer_jobs_document_id_fkey
    FOREIGN KEY (document_id)
    REFERENCES documents (id)
    ON DELETE SET NULL;

CREATE FUNCTION delete_standalone_answer_jobs_before_document()
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

CREATE TRIGGER trg_delete_standalone_answer_jobs_before_document
BEFORE DELETE ON documents
FOR EACH ROW
EXECUTE FUNCTION delete_standalone_answer_jobs_before_document();

COMMIT;
