-- text_chunks 保存文档解析、标准化和分块后的统一文本数据。
CREATE TABLE IF NOT EXISTS text_chunks (
    id BIGINT GENERATED ALWAYS AS IDENTITY PRIMARY KEY,

    document_id BIGINT NOT NULL
        REFERENCES documents (id)
        ON DELETE CASCADE,

    -- chunk_index 从 0 开始，表示文本块在当前文档中的稳定顺序。
    chunk_index INTEGER NOT NULL
        CHECK (chunk_index >= 0),

    content TEXT NOT NULL
        CHECK (LENGTH(BTRIM(content)) > 0),

    created_at TIMESTAMPTZ NOT NULL DEFAULT CURRENT_TIMESTAMP,

    -- 同一文档中的块序号不能重复。
    CONSTRAINT uq_text_chunks_document_index
        UNIQUE (document_id, chunk_index)
);
