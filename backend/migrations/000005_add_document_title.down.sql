BEGIN;

ALTER TABLE documents
    DROP COLUMN title;

COMMIT;
