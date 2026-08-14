# 文档文本块分页浏览架构

## 1. 核心目的

`GET /documents/:id/chunks?page=1&page_size=20` 用于查看一份文档解析后真正保存到
PostgreSQL 的统一文本块。它服务于前端预览、引用定位、Codex 调用和 PDF 解析质量排查，
不执行关键词检索、向量检索或远程模型调用。

## 2. 状态语义

- 文档不存在：`404 Not Found`；
- 文档存在但状态不是 `ready`：`409 Conflict`；
- ID、页码或每页数量非法：`400 Bad Request`；
- 查询成功：`200 OK`；
- 未分类的内部故障：`500 Internal Server Error`，不向客户端暴露数据库详情。

只允许 `ready` 的原因是文档重新处理期间可能仍保留上一版本的 chunks。Application 先检查
`Document.status`，再读取文本块，避免调用者把旧内容误认为当前正式结果。

## 3. 分层调用链

```text
HTTP path/query
  → DocumentChunkHandler
  → ChunkListService
      → Document Finder：确认存在且 ready
      → ChunkPageLister：执行 COUNT + LIMIT/OFFSET
  → PostgreSQL text_chunks
  → HTTP DTO + pagination
```

Domain 同时保留两个窄接口：

- `ChunkLister`：Worker 生成向量时读取一篇文档的全部 chunks；
- `ChunkPageLister`：HTTP 浏览时只读取当前页。

二者由同一个 PostgreSQL `ChunkRepository` 实现，但调用者不需要依赖自己用不到的方法。

## 4. 分页为什么在数据库完成

如果先调用全量 `ListByDocumentID`，再在 Go 中切片，数据库仍会传输全部正文，Go 也必须为全部
chunks 分配内存。当前接口使用：

```sql
COUNT(*)        -- 获得 total
LIMIT $2        -- 限制当前页数量
OFFSET $3       -- 跳过前面的记录
```

因此单次请求的正文内存规模受 `page_size` 控制。第一版复用项目统一规则：默认 20 条，最多
100 条，并按照 `chunk_index ASC` 保持原文顺序。
