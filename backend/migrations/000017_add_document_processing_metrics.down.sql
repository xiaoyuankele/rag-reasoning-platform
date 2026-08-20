ALTER TABLE document_jobs
    DROP COLUMN IF EXISTS error_code,
    DROP COLUMN IF EXISTS chunk_count,
    DROP COLUMN IF EXISTS file_bytes,
    DROP COLUMN IF EXISTS total_ms,
    DROP COLUMN IF EXISTS processor_ms,
    DROP COLUMN IF EXISTS queue_wait_ms;
