BEGIN;

-- corpus_revision 是每个用户“当前可检索语料版本”的单调递增编号。
-- Redis 问答缓存把它写入 Key；文档生命周期改变时递增版本即可自然失效旧 Key。
ALTER TABLE users
    ADD COLUMN corpus_revision BIGINT NOT NULL DEFAULT 1;

ALTER TABLE users
    ADD CONSTRAINT users_corpus_revision_positive
    CHECK (corpus_revision > 0);

COMMIT;
