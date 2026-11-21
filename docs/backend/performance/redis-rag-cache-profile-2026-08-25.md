# Redis RAG 缓存零费用性能画像（2026-08-25）

## 1. 测试目的与边界

本测试使用真实本地 Redis 和 Fake Embedder/Fake Answerer，验证以下三件事：

1. 重复问题能否稳定复用缓存并减少 Provider 调用；
2. 多个并发相同问题能否避免缓存击穿；
3. 当前 Key/Value 协议在 Redis 中的实际逐 Key 内存量级。

测试不会连接 DashScope、OpenAI 或其他远程模型，不产生模型费用。测试数据位于随机 Redis 命名空间，结束后删除。
这不是端到端 HTTP 延迟压测，也不能替代真实用户问题分布下的命中率观测。

## 2. 执行命令

```powershell
cd E:\Web\Project_My\rag_reasoning_platform_individual\backend
$env:RUN_REDIS_TESTS = "1"
$env:REDIS_ADDRESS = "127.0.0.1:6380"

try {
    go test -v -count=1 `
        ./internal/integration `
        -run TestRedisRAGCacheProfileWithFakeProviders
}
finally {
    Remove-Item Env:RUN_REDIS_TESTS -ErrorAction SilentlyContinue
    Remove-Item Env:REDIS_ADDRESS -ErrorAction SilentlyContinue
}
```

## 3. 场景

每类缓存先执行 100 个不同问题，每个问题请求 10 次，共 1000 次；随后对一个全新问题同时发起 50 次调用。
因此每类缓存总计 1050 次请求、理论唯一结果 101 条。

- 查询向量使用 1536 维 Float32 二进制值；
- 问答结果包含一段代表性中文答案、5 条来源和 Token 使用量；
- 查询向量命中后仍调用 Fake PostgreSQL Repository，验证缓存没有绕过 OwnerScope 检索；
- Redis 内存使用 `MEMORY USAGE` 对测试命名空间内的每个 Key 求和。

## 4. 结果

| 指标 | 查询向量缓存 | 问答结果缓存 |
| --- | ---: | ---: |
| 总请求 | 1050 | 1050 |
| Fake Provider 实际调用 | 101 | 101 |
| 节省调用 | 949 | 949 |
| Provider 节省率 | 90.38% | 90.38% |
| 并发相同问题请求 | 50 | 50 |
| 并发场景 Provider 调用 | 1 | 1 |
| Redis Key 数 | 101 | 101 |
| `MEMORY USAGE` 合计 | 744168 B | 643976 B |
| 平均每 Key | 7368 B | 6376 B |

查询向量的 1050 次请求仍产生 1050 次 Fake PostgreSQL 搜索，证明查询向量缓存只省略 Embedding，
没有把搜索结果或 Owner 权限放进 Redis。

## 5. TTL 与容量决定

第一版保留：

- `QUERY_VECTOR_CACHE_TTL=12h`：模型、维度和规范化版本不变时查询向量稳定，可适度长缓存；
- `ANSWER_RESULT_CACHE_TTL=15m`：答案价值更依赖当前语料和生成配置，采用较短 TTL；
- `allkeys-lfu`：到达容量时优先保留高频问题；淘汰只降低命中率，不影响正确性。

100 用户容量假设：每个用户 12 小时内产生 100 个不同查询向量，并在 15 分钟窗口内保留 20 个答案：

```text
10000 × 7368 B + 2000 × 6376 B ≈ 86.4 MB
```

逐 Key `MEMORY USAGE` 不能完整覆盖 Redis 进程、连接和内存碎片。因此：

- 本地开发：128 MiB 数据上限、160 MiB 容器限制；
- 100 用户生产候选：256 MiB 数据上限、至少 320 MiB 容器限制；
- 监控达到 70% 或持续出现 eviction 时再扩容；
- 缓存 Redis 不与未来的限流/分布式准入 Redis 共用实例，避免淘汰策略影响控制面 Key。

以上是生产候选起点，不是永久容量承诺。上线后必须用真实 `hit/miss/wait/failure` 日志、Provider 调用数、
`used_memory`、`maxmemory` 和 `evicted_keys` 重新校准。
