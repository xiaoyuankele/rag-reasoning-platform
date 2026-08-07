-- 为统一文本块增加可选来源页码，供 PDF 检索结果定位原文。
ALTER TABLE text_chunks
    ADD COLUMN page_start INTEGER,
    ADD COLUMN page_end INTEGER,
    ADD CONSTRAINT ck_text_chunks_page_range
        CHECK (
            (page_start IS NULL AND page_end IS NULL)
            OR (
                page_start >= 1
                AND page_end >= page_start
            )
        );
