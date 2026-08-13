# P4 语义检索架构与分阶段路线

## 一、核心目标

P4 保留 `GET /search` 作为独立关键词检索，并新增独立语义检索能力。第一版先验证向量存储、
相似度结果和真实问题测试集，最后再根据证据决定是否开发混合检索。

```text
GET /search                关键词检索，保持可独立使用
POST /semantic-search      语义检索，P4 后续实现
混合检索                   暂不实现，等待真实测试集结论
```

## 二、已经确认的生命周期边界

```text
documents.status
    管理：原始文件 → 文本提取 → text_chunks

embedding_jobs.status
    管理：text_chunks → 向量生成 → chunk_embeddings
```

两套状态互不覆盖。文档文本处理成功后，即使向量生成失败，文件查询和关键词检索仍然可用。

```text
uploaded → processing → ready
                         │
                         └─ embedding queued → processing → succeeded
                                                     └────→ failed
```

## 三、技术选择

- Embedding 来源：可切换的远程 API；当前默认使用阿里云百炼，保留 OpenAI 实现；
- 第一版模型：DashScope 使用 `text-embedding-v4`，OpenAI 使用 `text-embedding-3-small`；
- 第一版向量维度：1536；
- 自动化测试：使用 Fake Embedder，不产生远程费用；
- 向量存储：使用同一个 PostgreSQL 数据库中的 pgvector；
- 任务触发：第一版使用 `POST /documents/:id/embeddings` 手动触发；
- 自动触发：等任务执行和失败恢复稳定后再接入文本 Worker 成功收尾；
- 混合检索：先建立真实问题测试集，再决定是否实现。

### 第一版向量版本策略

- 每个 `text_chunk` 只保留一条当前有效向量，不保留多模型历史；
- `chunk_embeddings.chunk_id` 使用主键约束，重新生成时在同一事务内原子覆盖；
- 模型名称和维度统一记录在 `embedding_jobs`，不在每个向量行重复保存；
- 第一版数据库列固定为 `vector(1536)`；
- 当前不考虑切换 Embedding 模型；如果以后调整输出维度，必须通过受控迁移清空当前向量、
  修改固定维度并全量重新生成，不能让不同维度参与同一次相似度比较；
- 第一版使用精确余弦检索，不提前建立 HNSW/IVFFlat 近似索引，等真实数据测量后再决定。

## 四、分层数据流

### 4.1 向量任务入队

```text
HTTP POST /documents/:id/embeddings
  ↓
Handler：解析 ID、映射 HTTP 状态
  ↓
Application：确认 documents.status == ready
  ↓
Domain：Embedding Job、状态和仓储端口
  ↓
Infrastructure：向 embedding_jobs 插入 queued 任务
```

### 4.2 后续向量执行

```text
Embedding Worker 领取 queued 任务
  ↓
读取当前文档的 text_chunks
  ↓
分批调用远程 Embedding API
  ↓
校验返回数量、顺序和向量维度
  ↓
事务保存 chunk_embeddings 并把任务标记为 succeeded
```

远程网络调用不能放进 PostgreSQL 事务。先在事务外取得和校验全部结果，再用数据库事务完成
“保存向量 + 标记任务成功”的原子收尾。

### 4.3 后续语义检索

```text
用户问题
  ↓
同一个 Embedding 模型生成查询向量
  ↓
PostgreSQL + pgvector 计算相似度
  ↓
返回文本块、文献标题、原始文件名、物理页码和相似度
```

文档向量和查询向量必须由相同模型、相同维度生成，否则不能直接比较。

## 五、当前已经完成

- 第 7 号迁移创建 `embedding_jobs`；
- `ON DELETE CASCADE` 保证删除文档时清理任务；
- 部分唯一索引保证同一文档最多只有一个活动向量任务；
- 独立 `domain/embedding` 领域包；
- PostgreSQL 任务仓储；
- Application 就绪状态检查；
- Gin 手动入队接口和 HTTP 错误映射；
- 模型名称与向量维度配置；
- 可替换的 `Embedder` 领域端口；
- 基于标准库 HTTP 客户端的 OpenAI 兼容协议适配器，以及 DashScope/OpenAI 提供方选择；
- Embedding Worker 的应用层编排、分批处理、向量与 chunk ID 对齐；
- 永久失败、临时失败、指数退避重试和 shutdown 取消规则；
- 单元测试、全量 Go 回归、`go vet` 和真实 PostgreSQL 仓储测试。
- pgvector PostgreSQL 15 镜像、第 8～9 号迁移和 `chunk_embeddings vector(1536)`；
- PostgreSQL 原子领取、延迟重试、永久失败与“全部向量 + succeeded”事务收尾；
- shutdown 遗留任务的单实例启动恢复；
- 配置化 API 超时、任务超时、批大小、轮询与指数退避；
- `main` 组合根中的可关闭 Embedding Worker 和多 goroutine 等待。
- Domain `SemanticChunkSearcher`、语义命中模型和模型隔离条件；
- Application “问题向量化 → 相似 chunks 查询”编排与输入校验；
- PostgreSQL 精确余弦检索、文档过滤、模型/维度过滤和真实集成测试；
- `POST /semantic-search` JSON Handler、DTO 转换和 HTTP 错误映射；
- Worker 与语义检索独立开关，以及共享 Embedder 的生产组合代码。

`EMBEDDING_WORKER_ENABLED` 与 `SEMANTIC_SEARCH_ENABLED` 默认都是 `false`，因此默认运行不会调用
远程 API 或产生费用。两者可以独立启用；只要任一能力开启就要求对应提供方的 API Key，二者同时
开启时复用同一个无状态 Embedder HTTP 客户端。真实 API Key 不写入源码、数据库、日志、HTTP
响应或 Git，只能通过本机私有环境注入。

2026-08-12 已完成 DashScope 真实纵向验收：文档 20 的任务 22 使用 `text-embedding-v4` 一次成功，
42 个文本块全部获得 1536 维非零向量并通过同一事务落库，任务进入 `succeeded`，不存在重复、遗漏或
活动任务残留。这证明 P4 的“入队—领取—远程生成—原子落库”主链路已经可运行。

2026-08-13 已完成 `POST /semantic-search` 真实 HTTP 验收：在
`EMBEDDING_WORKER_ENABLED=false`、`SEMANTIC_SEARCH_ENABLED=true` 的情况下路由正常注册，证明在线
检索与后台 Worker 已解耦。中文问题通过 DashScope 生成查询向量后，在文档 20 的 42 条向量中返回
5 条相似度降序结果，包含原始文件名和物理页码；默认 `top_k=5` 与非法 `top_k=0` 分支均符合契约。

## 六、后续实施顺序

1. ~~为远程 Embeddings 定义可替换的 `Embedder` 端口；~~
2. ~~开发远程 HTTP 适配器、批处理和安全错误分类；~~
3. ~~完成独立 Embedding Worker 的 PostgreSQL 领取、重试和原子收尾；~~
4. ~~把 PostgreSQL 镜像升级为支持 pgvector 的 PostgreSQL 15 镜像；~~
5. ~~创建 `chunk_embeddings` 表并验证向量落库；~~
6. ~~为真实 HTTP 客户端和 Worker 增加配置化超时并接入组合根；~~
7. ~~使用 DashScope 完成一份真实文档的完整向量生成与落库验收；~~
8. ~~开发 `POST /semantic-search` 的各层、生产组合与自动化/数据库测试；~~
9. ~~完成 `POST /semantic-search` 的真实远程 HTTP 验收；~~
10. 使用中英文真实问题建立检索测试集；
11. 根据关键词与语义检索对比结果决定是否开发混合检索。
