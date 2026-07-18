-- 创建文档元数据表。
-- 文件本体保存在 storage/，数据库只保存文件信息和处理状态。

BEGIN;

CREATE TABLE documents (
    -- IDENTITY 由 PostgreSQL 自动生成递增 ID。
    id BIGINT GENERATED ALWAYS AS IDENTITY PRIMARY KEY,

    -- 用户上传时的原始文件名。
    original_name TEXT NOT NULL,

    -- 文件在 storage/ 中的相对路径，不能重复。
    storage_path TEXT NOT NULL UNIQUE,

    -- 文件的 MIME 类型，例如 application/pdf。
    mime_type TEXT NOT NULL,

    -- 文件大小，单位为字节，不能为负数。
    size_bytes BIGINT NOT NULL
        CHECK (size_bytes >= 0),

    -- 文件内容的 SHA-256，用于完整性检查和后续重复文件判断。
    sha256 TEXT NOT NULL
        CHECK (sha256 ~ '^[0-9a-f]{64}$'),

    -- 文档处理状态。
    status TEXT NOT NULL DEFAULT 'uploaded'
        CHECK (status IN ('uploaded', 'processing', 'ready', 'failed')),

    -- 处理失败时保存错误原因；成功时通常为空。
    error_message TEXT,

    -- TIMESTAMPTZ 会保存带时区语义的时间。
    created_at TIMESTAMPTZ NOT NULL DEFAULT CURRENT_TIMESTAMP,
    updated_at TIMESTAMPTZ NOT NULL DEFAULT CURRENT_TIMESTAMP
);

-- 加速按状态查询和按创建时间排序。
CREATE INDEX idx_documents_status_created_at
    ON documents (status, created_at DESC);

-- 加速根据文件哈希查找重复内容。
CREATE INDEX idx_documents_sha256
    ON documents (sha256);

COMMIT;