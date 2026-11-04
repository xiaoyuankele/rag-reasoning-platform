BEGIN;

DROP TRIGGER IF EXISTS trg_delete_standalone_answer_jobs_before_document ON documents;
DROP FUNCTION IF EXISTS delete_standalone_answer_jobs_before_document();

ALTER TABLE answer_jobs
    DROP CONSTRAINT answer_jobs_document_id_fkey;

ALTER TABLE answer_jobs
    ADD CONSTRAINT answer_jobs_document_id_fkey
    FOREIGN KEY (document_id)
    REFERENCES documents (id)
    ON DELETE CASCADE;

COMMIT;
