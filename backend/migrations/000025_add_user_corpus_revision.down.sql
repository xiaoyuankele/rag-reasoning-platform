BEGIN;

ALTER TABLE users
    DROP CONSTRAINT IF EXISTS users_corpus_revision_positive;

ALTER TABLE users
    DROP COLUMN IF EXISTS corpus_revision;

COMMIT;
