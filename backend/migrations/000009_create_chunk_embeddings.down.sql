BEGIN;

DROP TABLE IF EXISTS chunk_embeddings;

-- 故意保留 vector 扩展：它是数据库级共享能力，可能被其他表或 schema 使用。

COMMIT;
