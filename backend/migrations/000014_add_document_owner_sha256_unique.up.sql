BEGIN;

-- 同一用户只能保存一份内容完全相同的文档。
-- 在创建唯一索引前先给出清晰诊断，避免历史重复数据只表现为晦涩的索引错误。
DO $$
BEGIN
    IF EXISTS (
        SELECT 1
        FROM documents
        GROUP BY owner_user_id, sha256
        HAVING COUNT(*) > 1
    ) THEN
        RAISE EXCEPTION
            'cannot enable per-owner document deduplication: duplicate owner/hash rows exist';
    END IF;
END
$$;

CREATE UNIQUE INDEX uq_documents_owner_sha256
    ON documents (owner_user_id, sha256);

COMMIT;
