BEGIN;

ALTER TABLE answer_jobs
    ADD COLUMN requested_document_id BIGINT;

UPDATE answer_jobs
SET requested_document_id = document_id
WHERE document_id IS NOT NULL;

COMMIT;
