-- 第一版文档处理指标直接保存到每次 document_jobs 记录。
-- 字段保持可空：历史任务没有指标，执行在处理器之前中断时也可能没有对应数值。
ALTER TABLE document_jobs
    ADD COLUMN queue_wait_ms BIGINT
        CHECK (queue_wait_ms >= 0),
    ADD COLUMN processor_ms BIGINT
        CHECK (processor_ms >= 0),
    ADD COLUMN total_ms BIGINT
        CHECK (total_ms >= 0),
    ADD COLUMN file_bytes BIGINT
        CHECK (file_bytes >= 0),
    ADD COLUMN chunk_count INTEGER
        CHECK (chunk_count >= 0),
    ADD COLUMN error_code TEXT;

COMMENT ON COLUMN document_jobs.queue_wait_ms IS
    'Milliseconds from job creation until the worker claimed it';
COMMENT ON COLUMN document_jobs.processor_ms IS
    'Milliseconds spent in the document processor, including the Python subprocess';
COMMENT ON COLUMN document_jobs.total_ms IS
    'Milliseconds from worker claim until terminal task finalization';
COMMENT ON COLUMN document_jobs.file_bytes IS
    'Source document size observed by the worker';
COMMENT ON COLUMN document_jobs.chunk_count IS
    'Number of chunks produced by the processor';
COMMENT ON COLUMN document_jobs.error_code IS
    'Stable backend diagnostic category without raw error details';
