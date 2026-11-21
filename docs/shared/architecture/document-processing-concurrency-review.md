# 文档处理并发与 Python 进程复用交接

> 状态：2026-08-20 第一版处理指标、固定大小 Go Worker Pool 和可降级 Python Process Pool 均已实现。
> 当前支持同一后端实例内 1～4 个 Go Worker；Python 默认使用 oneshot，也可显式切换为 1～4 个常驻槽位。
> 项目同时把该模块化单体作为高性能、高并发工程训练平台，统一目标见
> [高性能与高并发工程目标](performance-engineering-goal.md)。

## 1. 为什么现在记录这项设计

当前文档解析链路已经能够稳定完成：

```text
HTTP 上传
  → document_jobs 入队
  → Go Document Worker 领取
  → 启动 Python CLI
  → PDF 预检、提取、分块
  → Go 校验结果
  → PostgreSQL 原子替换 chunks
  → 文档进入 ready/failed
```

基线批次使用每任务独立 Python 进程：处理完成后退出。单并发时这种方式有利于故障隔离和内存回收；进入并发后，会重复承担解释器启动、依赖导入、进程创建、内存占用和调度切换成本。现在已保留该模式用于降级，并增加有界常驻进程池用于后续对照压测。

这不是立即拆微服务的理由。当前更适合先把“有界并发”和“处理器生命周期”抽象出来，再按指标逐步替换执行器。

## 2. 当前性能事实

2026-08-21 使用固定 8 篇 PDF（共 51.28 MiB）完成第一轮并发拐点测试。`2/2` 表示 2 个 Go Worker 和 2 个 Python Process Pool 槽位：

| 后端 CPU 配额 | Worker/Pool | 批次墙钟时间 | Processor P95 | 后端峰值内存 |
| ---: | ---: | ---: | ---: | ---: |
| 1.5 | 2/2 | 56.762 秒 | 30.260 秒 | 约 282 MiB |
| 1.5 | 3/3 | 59.038 秒 | 42.443 秒 | 约 255 MiB（采样值） |
| 1.5 | 4/4 | 66.485 秒 | 54.370 秒 | 约 319 MiB |
| 2.0 | 2/2 | 46.076 秒 | 25.282 秒 | 约 232 MiB |
| 2.0 | 3/3 | 50.267 秒 | 32.576 秒 | 约 323 MiB |

这说明当前机器并不是 Worker 越多越快：2.0 CPU 配额下 3/3 比 2/2 慢约 9.1%，尾延迟和内存也更高。当前本地推荐 2/2，并保留单 Worker 与 `oneshot` 降级模式。

现有日志只记录一次处理的总耗时，没有把 Python 启动、PDF 提取、文本分块和 chunks 写入拆开。因此暂时不能把某个固定比例归因于“进程启动”。可以确认的是：

- 大型 PDF 中，真正的解析和落库耗时占主导，常驻进程不会把 10～18 秒直接降为毫秒级；
- 小型 PDF 中，解释器启动和模块导入占比会更明显；
- 多个任务同时启动独立 Python 进程，会产生重复内存和 CPU 峰值，限制可扩展并发。

### 2.1 `extract_text()` 页级诊断

2026-08-21 对同一批 8 篇、515 页 PDF 使用真实 `PyPDFDocumentExtractor` 做只读页级分析。第一轮不增加操作符回调，只包裹每页 `extract_text()`：页面提取累计约 71.64 秒，Reader 构造、预检、标题和其他提取开销合计约 0.50 秒，即页面提取约占该阶段 99.3%。

最慢样本《轴箱振动与轨道不平顺对应关系研究》的第 86 个物理页单页约 9.68 秒，只产生 1083 个字符。进一步展开 PDF 内容流发现，该页直接内容只有 3180 个操作，但递归进入 Form XObject 后需要遍历约 887440 个操作；视觉上该页包含两幅密集时序曲线图。其他慢页也主要是频谱图、多条曲线和复杂公式图表，慢页排名与展开操作数量高度一致，而不是与文件字节数或输出字符数一致。

pypdf 的重复 `/MediaBox` 和 padding 警告没有集中出现在最慢文档，因此结构警告不能直接当作性能根因。另一个公式页虽然提取很快，却产生 74 个 Tamil、Telugu、Malayalam、Thai 等错误脚本字符；性能异常和文本质量异常必须分别建模。

候选解析器对照没有证明可以立即替换 pypdf：`pdfplumber` 处理上述 110 页最慢文档约需 85.33 秒，约为 pypdf 的 2.7 倍；对公式页虽然不再输出异常南亚脚本，却产生大量未解析的 `(cid:...)` 占位符。因此下一步不是直接切换默认解析器，而是先记录聚合的 PDF 提取耗时、慢页数量和最慢页，再使用冻结样本比较其他引擎的速度与质量。

## 3. 当前实现边界

后端提供两种实现相同 Application 端口的基础设施适配器：`PythonProcessor` 通过 `exec.CommandContext` 为一份文档启动一次进程；`ProcessPool` 通过固定槽位复用 stream CLI。Python CLI 在入口处只组装一次 `pypdf` 和文本分块器，stream 模式逐行读取请求并逐行刷新响应。

现有边界必须继续保留：

- Python 不访问 PostgreSQL，也不修改任务状态；
- Go Worker 负责超时、取消、任务状态和错误收尾；
- stdout 只承载协议 JSON，诊断写入 stderr；
- Python 进程崩溃、协议错误、资源超限和结构化文档失败要区分；
- PDF 解析结果仍通过 Go 侧校验后才写入 `text_chunks`；
- 当前 `document_jobs` 的数据库状态仍是真实任务状态来源。

## 4. 目标过渡架构

### 4.1 第一阶段：固定大小的 Go Worker Pool

> 已完成：`DOCUMENT_WORKER_CONCURRENCY` 默认 1、允许 1～4；并发 2 已通过双任务 PostgreSQL 收尾和双 Python 子进程测试。

```text
document_jobs
    ↓  PostgreSQL FOR UPDATE SKIP LOCKED
Go Document Worker Pool（先从 2 个开始）
    ├─ Worker 1 → Python 执行器
    └─ Worker 2 → Python 执行器
```

第一阶段可以先保持“每任务一个 Python 进程”，用于验证：

- 任务不会重复领取；
- 两个任务可以安全并行；
- PostgreSQL 连接池、CPU、内存和磁盘不会失控；
- API 延迟不会因解析并发明显恶化。

`FOR UPDATE SKIP LOCKED` 已为单库多消费者领取提供基础，但当前启动恢复仍带有单实例假设。第一阶段只做同一后端实例内的有界并发，不直接复制多个后端实例。

### 4.2 第二阶段：固定大小的 Python 进程池

> 已完成：默认池大小 2、允许 1～4；单进程默认处理 20 份文档后回收，oneshot 保持默认降级路径。

```text
Go Document Worker Pool
    ↓
PythonProcessPool（先配置 2 个常驻进程）
    ├─ Python Worker 1：处理一份 → 等待下一份
    └─ Python Worker 2：处理一份 → 等待下一份
```

每个 Python Worker 一次只处理一份文档，完成后复用。这样可以减少反复创建解释器和导入依赖的开销，同时保留进程级隔离。

当前进程池已经具备：

- 单任务超时和取消；
- 单进程异常退出后的定向重启；
- 处理达到上限后的主动回收，控制潜在内存增长；
- 有界槽位租借和进程惰性启动；
- stdout/stderr 协议边界保护；
- 进程崩溃后任务可安全重试或进入失败终态。

### 4.3 第三阶段：独立 Python 处理服务

只有当并发、扩缩容或故障隔离形成真实需求时，才考虑把 Python 拆为独立服务：

```text
API/任务服务 → 任务队列 → Python Processing Service → 共享对象存储
```

这一步会引入服务发现、健康检查、网络超时、鉴权、部署、共享文件或对象存储、服务级限流和独立监控，不能作为当前进程池改造的默认实现。

## 5. Go/Python 协议演进

当前 v1 消息契约支持两种传输模式：

```text
oneshot：启动进程 → 一条 JSON 请求 → 一条 JSON 响应 → 进程退出
stream：启动进程 → 一行请求 → 一行响应 → 等待下一行
```

现有 v1 已经包含常驻传输所需的关键字段：

- `request_id`：关联 Go 任务和 Python 响应；
- `document.id`：关联文档并辅助后端诊断；
- `contract_version`：避免不兼容消息结构混用；
- `status/code/retryable`：结构化失败；
- `chunks/metadata`：承载统一文本块和可选文档标题。

第一版选择 JSON Lines 作为 framing：每条请求和响应严格单行 UTF-8 JSON，正文换行由 JSON 转义，Python 每次响应后立即 flush。Go 逐行读取并继续使用 v1 严格解码、请求 ID 对齐和 stdout 上限。stream 模式不依赖 EOF 判断单条消息，EOF 只表示 Go 主动关闭常驻进程。由于消息字段没有改变，本阶段无需仅为传输方式改名为 v2；未来真正增加或改变字段语义时再升级契约版本。

协议升级时，Application 和 Domain 不应知道 stdin、进程池或 HTTP 细节；替换点应集中在基础设施层的文档处理器实现。

## 6. 后端开发交接

### 当前不变的接口和数据语义

- `POST /documents` 仍只负责上传和内容去重；
- `POST /documents/:id/process` 仍显式创建解析任务；
- `GET /processing-jobs/:id` 仍查询任务真实状态；
- `queued` 表示任务已具备领取条件，不表示已经开始处理；
- `succeeded` 的任务仍需要结合文档状态确认 `ready`；
- 解析失败不会由前端伪造成成功，也不会自动触发向量化。

### 并发与进程池配置

```dotenv
# 已实现，默认 1 可随时回退到稳定单并发模式。
DOCUMENT_WORKER_CONCURRENCY=2

# 默认 oneshot；验证时显式改为 pool。
PYTHON_PROCESS_MODE=oneshot
PYTHON_PROCESS_POOL_SIZE=2
PYTHON_PROCESS_MAX_DOCUMENTS=20
```

这些配置不是让用户直接控制的产品字段，而是部署和压测参数。实际并发必须受 CPU、内存、数据库连接池和单用户配额共同约束。

### 后端实现顺序

1. 已完成第一版必要指标：`queue_wait_ms`、`processor_ms`、`total_ms`、文件大小、chunk 数、状态和错误分类；结构化日志与 `document_jobs` 同时保留数据。
2. 第二版把重型处理器拆为 `source_open_ms`、`metadata_read_ms`、`text_extract_ms`、`text_split_ms` 和 `python_total_ms`，并记录 `page_count`、最慢页码与最慢页耗时；Go 另外记录 `chunk_write_ms`，`finalize_ms` 只进入结构化日志。
2. 已完成固定大小 Worker Pool：配置默认 1、上限 4，并发数 2 已通过领取、执行和 shutdown 测试。
3. 区分“任务已收尾的业务失败”和“领取/数据库等基础设施错误”，避免业务失败统一触发 2 秒轮询退避。
4. 已完成 Python Process Pool：JSON Lines、惰性启动、固定槽位、超时取消、崩溃替换、20 份文档回收和输出上限均有测试。
5. 已使用同一批真实文献完成 2/2、3/3、4/4 的第一轮容量对照，当前机器推荐 2.0 CPU 配额下的 2/2；长时间浸泡、失败样本混合批次和 API 延迟对照仍待执行。
6. 多实例部署前增加 `worker_id`、`lease_expires_at` 和心跳；不能直接复用当前单实例启动恢复逻辑。

### 并发验收重点

- 两个 Worker 不领取同一个 `queued` 任务；
- 同一文档重复点击不会产生多条活动解析任务；
- 一个损坏 PDF 不阻塞其他文档；
- 一个 Python 子进程超时或崩溃只影响对应任务；
- 解析期间上传、文档列表、任务查询和检索接口仍保持可接受延迟；
- 数据库连接池等待、CPU、内存和临时文件数量均有观测值；
- 正常关闭和异常重启后任务状态仍符合既有恢复规则。

## 7. 前端开发交接

前端上传并发和后端解析并发是两个不同概念：

```text
浏览器上传并发（当前默认 2）
    ≠
后端 PDF 处理并发（代码默认 1，本地开发当前 2）
```

前端需要遵守：

- 不根据上传完成时间推断 PDF 已经解析完成；
- 继续使用任务状态和文档详情确认终态；
- `queued` 状态显示为“排队中”，不要显示为“处理中”；
- 前端批次只编排单文档接口，不自行实现全局任务锁；
- 刷新后优先通过后端活动任务恢复能力获取真实 job，不猜测 job ID；
- 后续收到 `429` 时按 `Retry-After` 展示并延迟重试，收到 `503` 时提示系统繁忙；
- 不因为后端并发提高就无限扩大浏览器上传并发，上传和解析都需要独立限流。

当前不新增批次 API，也不要求前端暴露 Worker 数量。前端可以继续显示批次总数、等待、排队、处理中、成功和失败；若未来需要估算等待时间，必须标注为估计值，不能把 Worker 数量当作稳定 SLA。

## 8. 可观测性和性能指标

第一版已经落地并持久化以下字段：

```text
document_id
job_id
queue_wait_ms
processor_ms
total_ms
file_bytes
chunk_count
status/error_code
```

其中 `processor_ms` 先把 Python 启动、PDF 解析、分块和协议往返作为一个重型阶段整体测量。

第二版可以进一步使用以下关系定位瓶颈：

```text
processor_ms - python_total_ms
≈ Python 进程池等待 + 跨进程通信 + JSON 编解码等外围开销
```

阶段字段在 `document_jobs` 中保持可空。`NULL` 表示历史任务、旧处理器或
本次没有执行该阶段；`0` 表示阶段确实执行，但耗时不足 1ms。Python
只返回汇总和最慢页，不返回全部逐页耗时，避免大文档响应和数据库记录膨胀。
只有数据证明该阶段需要继续拆解时，才升级协议并增加：

```text
request_id
worker_id（进程池阶段）
python_startup_ms（进程池阶段可为 0 或单独记录）
parse_ms
split_ms
chunk_write_ms
page_count
finalize_ms
```

压测报告至少比较 Worker/进程池并发数 1、2、3 下的：

- 总吞吐量和单任务 p50/p95；
- 排队时间 p50/p95；
- CPU、内存和数据库连接池等待；
- API p95 延迟；
- 失败率、超时率和进程重启次数。

没有这些阶段数据前，不应直接断言“常驻进程一定能达到某个秒数”，也不应只用单个大 PDF 推导系统容量。

## 9. 明确暂不实现

- 不让每个 HTTP 请求直接启动 Python；
- 不把 Go 协程数量当作系统容量；
- 不无限制创建 Python 子进程；
- 不把解析和向量化重新耦合；
- 不用 Redis 作为文档任务最终状态来源；
- 不在单实例租约、共享存储和恢复规则未完成前直接做多实例 Worker；
- 不把进程池部署字段暴露为前端 HTTP 参数；
- 不在真实对照压测前把 `pool` 改成生产默认模式。

## 10. 相关文档

- [PDF 文献处理架构与分阶段路线](../../backend/architecture/pdf-processing-roadmap.md)
- [F2 批量导入与解析队列](../../frontend/architecture/f2-batch-import-queue.md)
- [文档向量化、在线编辑、并发与缓存设计复盘](document-vectorization-editing-concurrency-review.md)
- [HTTP API 总览](../api/http-api-overview.md)
