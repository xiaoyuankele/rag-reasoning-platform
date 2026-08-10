BEGIN;

-- 文献标题与上传时的原始文件名是两个不同概念。
-- 标题允许为空：无法从 PDF 元数据识别标题时，前端回退显示 original_name。
ALTER TABLE documents
    ADD COLUMN title TEXT;

-- 第一版把标题限制为已经去除首尾空白的 1～500 个字符。
ALTER TABLE documents
    ADD CONSTRAINT documents_title_check
    CHECK (
        title IS NULL
        OR (
            title = BTRIM(title)
            AND CHAR_LENGTH(title) BETWEEN 1 AND 500
        )
    );

COMMIT;
