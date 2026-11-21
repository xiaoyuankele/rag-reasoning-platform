BEGIN;

-- migration 37 only deletes unsafe legacy standalone answer results.
-- That privacy cleanup is intentionally irreversible; down performs no data restoration.

COMMIT;
