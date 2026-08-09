ALTER TABLE text_chunks
    DROP CONSTRAINT IF EXISTS ck_text_chunks_page_range,
    DROP COLUMN IF EXISTS page_end,
    DROP COLUMN IF EXISTS page_start;
