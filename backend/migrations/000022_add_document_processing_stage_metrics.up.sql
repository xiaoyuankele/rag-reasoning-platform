-- 第二版文档处理指标补齐 Python 内部阶段和 chunks 写入耗时。
-- 全部字段保持可空，用 NULL 表示旧任务或本次没有执行/观测该阶段。
ALTER TABLE document_jobs
    ADD COLUMN chunk_write_ms BIGINT
        CHECK (chunk_write_ms >= 0),
    ADD COLUMN python_total_ms BIGINT
        CHECK (python_total_ms >= 0),
    ADD COLUMN source_open_ms BIGINT
        CHECK (source_open_ms >= 0),
    ADD COLUMN metadata_read_ms BIGINT
        CHECK (metadata_read_ms >= 0),
    ADD COLUMN text_extract_ms BIGINT
        CHECK (text_extract_ms >= 0),
    ADD COLUMN text_split_ms BIGINT
        CHECK (text_split_ms >= 0),
    ADD COLUMN page_count INTEGER
        CHECK (page_count >= 1),
    ADD COLUMN slowest_page_number INTEGER
        CHECK (slowest_page_number >= 1),
    ADD COLUMN slowest_page_ms BIGINT
        CHECK (slowest_page_ms >= 0),
    ADD CONSTRAINT document_jobs_processing_page_metrics_check
        CHECK (
            (page_count IS NULL
                AND slowest_page_number IS NULL
                AND slowest_page_ms IS NULL)
            OR
            (page_count IS NOT NULL
                AND slowest_page_number BETWEEN 1 AND page_count
                AND slowest_page_ms IS NOT NULL)
        );

COMMENT ON COLUMN document_jobs.chunk_write_ms IS
    'Milliseconds spent replacing the document text chunks';
COMMENT ON COLUMN document_jobs.python_total_ms IS
    'Milliseconds measured inside the Python document processing service';
COMMENT ON COLUMN document_jobs.source_open_ms IS
    'Milliseconds spent validating and opening the source parser';
COMMENT ON COLUMN document_jobs.metadata_read_ms IS
    'Milliseconds spent reading optional document metadata';
COMMENT ON COLUMN document_jobs.text_extract_ms IS
    'Milliseconds spent extracting text from all document pages';
COMMENT ON COLUMN document_jobs.text_split_ms IS
    'Milliseconds spent splitting normalized page text into chunks';
COMMENT ON COLUMN document_jobs.page_count IS
    'Physical page count observed by the document extractor';
COMMENT ON COLUMN document_jobs.slowest_page_number IS
    'One-based page number with the slowest text extraction duration';
COMMENT ON COLUMN document_jobs.slowest_page_ms IS
    'Milliseconds spent extracting text from the slowest page';
