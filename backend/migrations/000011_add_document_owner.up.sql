BEGIN;

-- Release A 只建立文档与个人用户的归属关系。
-- 字段暂时允许 NULL，让已有单用户数据可以通过受控命令显式认领；
-- 面向用户的 Repository 查询必须排除 owner_user_id IS NULL 的历史记录。
ALTER TABLE documents
    ADD COLUMN owner_user_id BIGINT;

ALTER TABLE documents
    ADD CONSTRAINT documents_owner_user_id_fkey
    FOREIGN KEY (owner_user_id)
    REFERENCES users (id)
    ON DELETE RESTRICT;

-- 支持“当前用户的文档列表”按创建时间和 ID 稳定倒序分页。
CREATE INDEX idx_documents_owner_created_at
    ON documents (owner_user_id, created_at DESC, id DESC);

COMMIT;
