ALTER TABLE document_jobs
    DROP CONSTRAINT IF EXISTS document_jobs_processing_page_metrics_check,
    DROP COLUMN IF EXISTS slowest_page_ms,
    DROP COLUMN IF EXISTS slowest_page_number,
    DROP COLUMN IF EXISTS page_count,
    DROP COLUMN IF EXISTS text_split_ms,
    DROP COLUMN IF EXISTS text_extract_ms,
    DROP COLUMN IF EXISTS metadata_read_ms,
    DROP COLUMN IF EXISTS source_open_ms,
    DROP COLUMN IF EXISTS python_total_ms,
    DROP COLUMN IF EXISTS chunk_write_ms;
