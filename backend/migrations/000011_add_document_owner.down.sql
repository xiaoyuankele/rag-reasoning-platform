BEGIN;

DROP INDEX IF EXISTS idx_documents_owner_created_at;

ALTER TABLE documents
    DROP CONSTRAINT IF EXISTS documents_owner_user_id_fkey;

ALTER TABLE documents
    DROP COLUMN IF EXISTS owner_user_id;

COMMIT;
