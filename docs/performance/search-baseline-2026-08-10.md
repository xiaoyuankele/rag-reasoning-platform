# P3 关键词检索性能基线（2026-08-10）

## 测量目的

在增加文本索引前，先记录当前真实开发数据库的数据规模、物理空间和查询
计划。本轮仅执行 `SELECT` 和 `EXPLAIN (ANALYZE, BUFFERS)`，没有创建扩展、索引或测试数据。

## 当前数据规模

| 项目 | 结果 |
|---|---:|
| `ready` 文档 | 2 |
| `uploaded` 文档 | 1 |
| `text_chunks` 总数 | 170 |
| `text_chunks` 表本体 | 192 kB |
| `text_chunks` 索引总量 | 32 kB |
| `text_chunks` 含附属数据总量 | 280 kB |

已有索引：

- 主键 B-tree：`text_chunks_pkey (id)`；
- 唯一 B-tree：`uq_text_chunks_document_index (document_id, chunk_index)`；
- 采集本节基线时，`pg_trgm` 1.6 在 PostgreSQL 镜像中可用，但尚未安装到开发数据库。

## 查询计划对照

### 跨文档搜索常见英文词 `the`

- 命中 137 个 chunk，过滤 33 个；
- `text_chunks` 选择 `Seq Scan`；
- 全部数据来自 PostgreSQL 共享缓存，没有报告磁盘 `read`；
- 计数 SQL 执行约 2.318 ms；
- 返回前 20 条的 SQL 执行约 2.060 ms，排序使用 `top-N heapsort`，约 60 kB 内存。

该词命中了约 81% 的 chunk，即使存在索引也需要读取大量结果，因此顺序
扫描并不异常。

### 跨文档搜索中文词 `协同控制`

- 命中 23 个 chunk，过滤 147 个；
- 查询仍选择 `Seq Scan`；
- 执行约 2.547 ms；
- 排序约使用 26 kB 内存。

### 指定 `document_id=20` 搜索中文词 `控制`

- 命中 30 个 chunk，过滤 140 个；
- 执行约 0.813 ms；
- 虽然已有 `(document_id, chunk_index)` B-tree，规划器仍选择扫描只占 24 个
  缓存页的小表，避免额外索引访问成本。

## 当前结论

1. 170 个 chunk 的开发库不足以证明文本索引的必要性；
2. 当前约 1～3 ms 的数据库执行时间不是性能瓶颈；
3. 不应基于小表的 `Seq Scan` 直接引入 `pg_trgm` 或 GIN；
4. 下一步应在隔离的临时 schema 中建立中等规模数据，比较现有字面检索在
   不同数据量下的时间、扫描行数和缓冲区访问；
5. 只有在大样本对照中证明存在实际收益后，才设计正式迁移和索引。

## 隔离临时数据对照

为了观察增长趋势，在单次 PostgreSQL 连接的临时表中分别生成 1,000、10,000 和
50,000 个约 900 字符的模拟 chunk。约 1% 的行包含 `rare-concept`，其余行不命中。
连接结束后临时表自动删除，未修改开发库业务表。

| chunk 数 | 临时表总量 | 实际命中 | 过滤行 | 执行时间 |
|---:|---:|---:|---:|---:|
| 1,000 | 1,120 kB | 10 | 990 | 7.430 ms |
| 10,000 | 10 MB | 100 | 9,900 | 74.570 ms |
| 50,000 | 51 MB | 500 | 49,500 | 377.396 ms |

无文本索引的 `STRPOS(LOWER(content), ...)` 在该样本上接近随扫描行数线性增长。
规划器对该函数条件的命中估计也较粗：50,000 行时预计输出 16,667 行，实际只有
500 行。

在同样的 50,000 行样本中增加 `document_id=50`后：

- 已有 `(document_id, chunk_index)` B-tree 被选为 `Index Scan`；
- 候选范围从 50,000 个 chunk 缩小到该文档的 500 个 chunk；
- 文本条件命中 5 行，过滤 495 行；
- 执行时间从跨文档的约 377 ms 降为约 3.8 ms。

这证明文档过滤不只改变功能范围，在大数据集上也能先借助 B-tree 大幅减少需要
检查正文的 chunk 数量。

## 下一个测量问题

当前已经证明跨文档子字符串扫描在数万 chunk 时会产生数百毫秒延迟，而
`document_id` B-tree 过滤能显著缩小候选集。下一步需要共同确认是否在隔离环境中临时
启用 `pg_trgm`，对比 GIN 索引的查询时间、索引空间和写入成本，再决定是否进入
正式迁移。

## `pg_trgm + GIN` 隔离对照

用户确认继续实验后，在一个最终 `ROLLBACK` 的事务中临时执行：

```sql
CREATE EXTENSION pg_trgm;
CREATE INDEX ... USING GIN (content gin_trgm_ops);
```

样本为 50,000 个约 900 字符且包含变化字符串的 chunk。英文词 `rare-concept` 命中
1%，中文词 `协同控制` 命中 0.5%。

### 英文查询

| 方式 | 计划 | 执行时间 |
|---|---|---:|
| `STRPOS(LOWER(...))` | `Seq Scan` | 392.2 ms |
| `ILIKE '%rare-concept%'` 无 GIN | `Seq Scan` | 448.7 ms |
| `ILIKE '%rare-concept%'` 有 GIN | `Bitmap Index Scan -> Bitmap Heap Scan` | 8.0 ms |

与同样的无 GIN `ILIKE` 相比，该模拟数据上的查询约快 56 倍。GIN 先从索引定位
500 个候选 TID，再从 Heap 中读取对应的 500 个数据块并复核完整条件。

### 中文查询

首次中文实验因 PowerShell 管道把词语破坏为 `????` 而作废。问题登记后，使用
PostgreSQL `U&'\\534F\\540C\\63A7\\5236'` Unicode 转义重试：

| 方式 | 实际命中 | 执行时间 |
|---|---:|---:|
| `ILIKE '%协同控制%'` 无 GIN | 250 | 419.0 ms |
| `ILIKE '%协同控制%'` 有 GIN | 250 | 2.9 ms |

有 GIN 时计划同样为 `Bitmap Index Scan -> Bitmap Heap Scan`，证明该 trigram 索引
方案在当前 PostgreSQL 15 环境中可以加速该中文子字符串样本。

### 空间和写入代价

| 项目 | 结果 |
|---|---:|
| Heap 表本体 | 56 MB |
| GIN 之前的索引总量 | 2,664 kB |
| trigram GIN 索引 | 10,088 kB |
| GIN 之后的索引总量 | 12 MB |
| GIN 之后表与索引合计 | 68 MB |
| 创建 GIN 索引 | 约 6.1 s |
| 无 GIN 插入 50,000 条 | 约 0.24 s |
| 已有 GIN 时插入 50,000 条 | 约 4.93 s |

该写入对照受一次性批量数据、缓存状态和合成文本影响，不应把“约 20 倍”直接
当作真实上传链路的固定倍数；它只证明 GIN 会产生明显的写放大和维护成本。真实
项目的 chunk 通过 Worker 异步批量入库，需要在正式方案中继续观察处理耗时。

### 回滚验证

事务结束后查询确认：

- `pg_trgm.installed_version` 仍为空；
- `public.benchmark_chunks` 不存在；
- 开发库业务数据和持久化结构未改变。

## 阶段决策

对照已经证明 `pg_trgm + GIN` 在数万个长文本 chunk 上能大幅改善中英文稀有
子字符串检索，但会增加索引空间、建立时间和 chunk 入库成本。项目确认采用该方案，
并通过第 6 号迁移把 Repository 从 `LOWER + STRPOS` 切换为带字面转义的 `ILIKE`，
同时为 `text_chunks.content` 建立 trigram GIN 索引。

## 正式迁移与开发库验证（2026-08-11）

正式迁移完成后核对：

| 项目 | 结果 |
|---|---|
| `schema_migrations` | version 6，`add_text_chunk_trigram_index` |
| 扩展 | `public.pg_trgm` 1.6 |
| 索引 | `idx_text_chunks_content_trgm` |
| 索引定义 | `USING gin (content gin_trgm_ops)` |
| 170 chunk 开发库索引大小 | 720 kB |

迁移集成测试除索引名称外，还核对 GIN 访问方法、`content` 列、
`public.gin_trgm_ops` 操作类以及 valid/ready 状态。Repository 真实数据库测试覆盖中文、
英文大小写、`document_id` 过滤、分页、空结果以及 `%`、`_`、反斜杠的字面匹配。

全量真实 PostgreSQL 回归前后均为 3 个文档、170 个 chunk 和 2 个任务，临时测试 schema
为 0，说明测试没有污染业务数据。

开发库只有 170 个 chunk 时，中文“协同控制”的默认计划仍选择 `Seq Scan`，执行约
1.7 ms；这是规划器对小表成本的正常判断。诊断性关闭顺序扫描后，同一查询能够使用：

```text
Bitmap Index Scan on idx_text_chunks_content_trgm
→ Bitmap Heap Scan on text_chunks
```

执行约 0.56 ms。该强制计划只用于确认索引和查询表达式兼容，不作为生产配置；正式采用
GIN 的主要依据仍是 50,000 chunk 隔离样本中数百毫秒降至个位数毫秒的对照结果。
