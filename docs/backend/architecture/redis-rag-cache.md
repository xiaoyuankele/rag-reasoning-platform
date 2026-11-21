# Redis 查询向量与问答结果缓存

## 1. 核心目的

这一能力解决两个不同层次的重复计算问题：

1. 同一用户重复提交相同语义检索问题时，复用已经生成的查询向量，减少 Embedding API 调用、费用和在线槽位占用；
2. 同一用户的语料和问答配置都没有变化时，复用已经生成的带来源答案，直接绕过完整的检索与 Generation 链路。

Redis 只保存可丢弃的加速副本。PostgreSQL 继续保存用户权限、文档、文本块、向量、任务和语料版本，
因此 Redis 故障、数据丢失或淘汰只能降低性能，不能改变业务结果和 Owner 隔离。

## 2. 两条执行链路

```text
语义检索
  Handler
    -> SemanticSearchService
      -> 查询向量缓存
         命中：读取 Float32 向量
         未命中：Embedding 闸门 -> 远程 Provider -> 回填 Redis
      -> PostgreSQL Owner-scoped 向量检索
      -> 返回命中结果

同步/异步问答
  Handler 或 Answer Job Worker
    -> 问答结果缓存
       命中：核对 PostgreSQL corpus_revision 后直接返回
       未命中：问答并发闸门 -> 语义检索 -> Generation -> 回填 Redis
```

缓存装饰器位于问答并发闸门外层。因此缓存命中不会占用 Generation 并发槽位；未命中才进入原有容量治理。
异步问答 Worker 与同步接口使用同一个缓存装饰器，不会形成两套缓存语义。

## 3. 查询向量缓存

### 3.1 Key

```text
{namespace}:qvec:v1:{owner_id}:{provider}:{model}:{dimensions}:n1:{query_hmac}
```

- `owner_id` 隔离不同用户，第一版不跨用户共享问题向量；
- Provider、模型和维度进入 Key，切换模型不会误用旧向量；
- `n1` 是问题规范化协议版本；
- `query_hmac` 是服务端 HMAC-SHA256，不在 Redis Key 或日志中保存问题明文。

`n1` 规则依次执行 Unicode NFC、删除首尾空白、把连续 Unicode 空白折叠成一个 ASCII 空格；保留大小写，
避免擅自改变缩写语义。

### 3.2 Value

值使用带协议头和维度的 Float32 小端二进制，不使用 JSON 浮点数组。1536 维向量的主体约为 6 KiB。
解码时会核对协议、维度和长度；无效值按缓存未命中处理并回源。

### 3.3 正确性边界

缓存命中只省略“问题转向量”，之后仍必须执行 PostgreSQL Owner-scoped 检索。因此命中缓存不会绕过鉴权，
也不会返回其他用户的文档或旧搜索结果。

## 4. 问答结果缓存与版本化失效

### 4.1 Key

```text
{namespace}:answer:v1:{owner_id}:{corpus_revision}:{request_config_hash}:{question_hmac}
```

`request_config_hash` 覆盖以下会影响答案的配置：

- Generation Provider、模型、最大输出 Token、temperature；
- Prompt 协议版本与检索编排版本；
- Embedding 模型与维度；
- `document_id` 过滤、`top_k` 和回答语言。

任一配置变化都会自然产生新 Key，无需扫描删除旧缓存。

### 4.2 corpus revision

`users.corpus_revision` 是 PostgreSQL 中的正整数版本，默认从 1 开始。以下事件必须与正式业务写入处于同一事务，
成功后把对应 Owner 的版本加 1：

- 删除文档以及级联删除其 chunks、向量和任务；
- 重新解析领取任务、文档从旧可用状态进入 `processing`；
- 一份文档的全部新向量与 Embedding 成功状态原子落库。

这样发生失败或事务回滚时，语料版本也会一起回滚；成功变更后旧 Key 因版本不匹配自然失效。
第一版不扫描删除旧 Redis Key，旧值由 TTL 或淘汰策略回收。

### 4.3 命中校验

读取缓存前后都要从 PostgreSQL 核对 `corpus_revision`。如果读取期间语料发生变化，缓存结果不会返回，
而是执行新的问答链路。每次请求仍先建立 Session OwnerScope，Redis 不能充当权限来源。

只有成功且包含合法来源证据的答案才缓存。无证据降级回答、远程错误、超时、429/503 和部分结果均不缓存。
缓存命中没有发生本次模型调用，因此响应中的本次 `usage` Token 统一为 0，避免成本统计重复计费。

## 5. 防击穿、降级与观测

- 单进程使用 `singleflight` 合并相同查询向量的并发未命中；
- 跨进程使用 Redis `SET NX` 短租约协调填充；释放租约用“核对 token 后删除”的 Lua 原子操作；
- 等待者只等待有限时间，超时后进入已有并发闸门并正常回源；
- Redis 的连接、读取、解码、写入或租约失败都只记录结构化事件，业务继续走 PostgreSQL 与远程 Provider；
- 日志事件包括 hit、miss、wait、read/write failure、revision failure/change 和 skipped；
- 日志不记录问题、答案、Owner ID、HMAC 密钥或 API Key。

当前是单 Redis 实例、Cache-Aside 第一版，不提供 Redis 高可用、跨地域复制或结果预热。正式容量调优应观察命中率、
Provider 节省次数、Redis 内存、淘汰数和填充等待时间后再调整 TTL 与内存上限。

## 6. 配置和本地运行

缓存默认关闭，不会因为启动后端而产生新的远程调用。启用时至少设置：

```dotenv
RAG_CACHE_ENABLED=true
CACHE_HMAC_SECRET=请使用至少32字节的本机随机密钥
```

Compose 提供 `redis:7-alpine` 开发服务，宿主机默认映射 `127.0.0.1:6380`，使用 128 MiB 上限和
`allkeys-lfu` 淘汰策略，不挂载持久化卷。后端只等待 PostgreSQL 健康，不把 Redis 健康作为启动前置条件；
Redis 与 PostgreSQL 的职责不同，清空或停止 Redis 不会丢失正式业务数据。

零费用性能画像表明，查询向量平均约 7368 字节/条，代表性问答结果平均约 6376 字节/条。在“每个用户 12 小时内
100 个不同问题、15 分钟内 20 个热点答案”的 100 用户估算下，约有 10000 条查询向量和 2000 条答案，
仅按逐 Key `MEMORY USAGE` 约占 86.4 MB。考虑 Redis 进程、连接、分配器碎片及增长余量：

- 本地开发保留 `REDIS_MAXMEMORY=128mb`、容器限制 `160m`；
- 100 用户生产候选从 `REDIS_MAXMEMORY=256mb`、容器限制 `320m` 开始；
- 查询向量 TTL 保留 12 小时，问答结果 TTL 保留 15 分钟；
- `used_memory/maxmemory` 达到 70% 或出现持续 eviction 时再扩容，而不是依赖缓存保证正确性。

完整数据、执行命令和估算边界见
[Redis RAG 缓存零费用性能画像](../performance/redis-rag-cache-profile-2026-08-25.md)。

## 7. 第一版不包含

- 不缓存“语义相近但文本不同”的问题；
- 不跨用户共享查询向量或答案；
- 不把缓存用作限流、Session、任务队列或权限事实；
- 不提供人工清理旧版本 Key 的管理接口；
- 不用 Redis 替代 pgvector 的正式向量存储。
