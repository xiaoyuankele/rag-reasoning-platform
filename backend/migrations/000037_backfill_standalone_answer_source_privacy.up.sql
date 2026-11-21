BEGIN;

-- 清理 migration 34–36 滚动窗口中旧 Worker 可能写入的 standalone 成功结果。
-- 任一来源缺少合法正整数文档 ID、文档已不存在或 Owner 不匹配时，整条结果删除。
DELETE FROM answer_jobs AS job
WHERE job.conversation_id IS NULL
  AND job.status = 'succeeded'
  AND EXISTS (
      SELECT 1
      FROM jsonb_array_elements(
          CASE
              WHEN jsonb_typeof(job.sources) = 'array' THEN job.sources
              ELSE '[null]'::jsonb
          END
      ) AS source(value)
      CROSS JOIN LATERAL (
          SELECT CASE
              WHEN jsonb_typeof(source.value) = 'object'
                   AND source.value ? 'document_id'
                  THEN source.value -> 'document_id'
              WHEN jsonb_typeof(source.value) = 'object'
                   AND source.value ? 'DocumentID'
                  THEN source.value -> 'DocumentID'
              ELSE NULL
          END AS value
      ) AS raw_document_id
      CROSS JOIN LATERAL (
          SELECT CASE
              WHEN jsonb_typeof(raw_document_id.value) = 'number'
                   AND (raw_document_id.value #>> '{}') ~ '^[1-9][0-9]*$'
                  THEN (raw_document_id.value #>> '{}')::numeric
              ELSE NULL
          END AS value
      ) AS parsed_document_id
      WHERE parsed_document_id.value IS NULL
         OR NOT EXISTS (
              SELECT 1
              FROM documents AS document
              WHERE document.id::numeric = parsed_document_id.value
                AND document.owner_user_id = job.owner_user_id
         )
  );

COMMIT;
