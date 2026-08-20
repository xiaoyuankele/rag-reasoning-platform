# 文档处理并发与 Python 进程复用交接

> 状态：2026-08-20 第一版处理指标已实现，尚未实现 Worker Pool 和 Python 进程池。
> 当前仍是单 Go Worker、每份文档启动一次 Python CLI；本文冻结演进边界，供后端和前端后续开发使用。

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

但目前每份任务都会独立启动一个 Python 进程，处理完后退出。单并发时这种方式有利于故障隔离和内存回收；进入并发后，会重复承担解释器启动、依赖导入、进程创建、内存占用和调度切换成本。

这不是立即拆微服务的理由。当前更适合先把“有界并发”和“处理器生命周期”抽象出来，再按指标逐步替换执行器。

## 2. 当前性能事实

2026-08-19 对当前数据库任务和 Docker 后端日志做了只读检查。最近任务跨越了多次批次，以下使用连续任务 `268～275` 作为诊断样本，不把它当成固定性能基准：

| 指标 | 观察结果 |
| --- | ---: |
| 连续成功文档数 | 8 |
| Python/处理阶段累计耗时 | 约 51.64 秒 |
| 首个任务创建到最后完成 | 约 52.42 秒 |
| 最长单任务排队时间 | 约 36.63 秒 |
| `/process` 创建任务接口 | 约 2～4 毫秒 |
| 任务查询接口 | 约 0～2 毫秒 |

这说明当前批次的主要瓶颈是单 Worker 串行执行，而不是 HTTP 查询。单文档耗时还会随 PDF 大小和内容复杂度变化：样本中 17.9 MiB 文档处理约 18.57 秒，23.0 MiB、291 个文本块的文档约 12.08 秒。

现有日志只记录一次处理的总耗时，没有把 Python 启动、PDF 提取、文本分块和 chunks 写入拆开。因此暂时不能把某个固定比例归因于“进程启动”。可以确认的是：

- 大型 PDF 中，真正的解析和落库耗时占主导，常驻进程不会把 10～18 秒直接降为毫秒级；
- 小型 PDF 中，解释器启动和模块导入占比会更明显；
- 多个任务同时启动独立 Python 进程，会产生重复内存和 CPU 峰值，限制可扩展并发。

## 3. 当前实现边界

后端当前通过 `PythonProcessor` 创建 `exec.CommandContext`，一次进程只处理一条 JSON 请求，随后退出。Python CLI 在入口处组装 `pypdf` 和文本分块器，读取一条 stdin 请求并输出一条响应。

现有边界必须继续保留：

- Python 不访问 PostgreSQL，也不修改任务状态；
- Go Worker 负责超时、取消、任务状态和错误收尾；
- stdout 只承载协议 JSON，诊断写入 stderr；
- Python 进程崩溃、协议错误、资源超限和结构化文档失败要区分；
- PDF 解析结果仍通过 Go 侧校验后才写入 `text_chunks`；
- 当前 `document_jobs` 的数据库状态仍是真实任务状态来源。

## 4. 目标过渡架构

### 4.1 第一阶段：固定大小的 Go Worker Pool

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

```text
Go Document Worker Pool
    ↓
PythonProcessPool（先配置 2 个长驻进程）
    ├─ Python Worker 1：处理一份 → 等待下一份
    └─ Python Worker 2：处理一份 → 等待下一份
```

每个 Python Worker 一次只处理一份文档，完成后复用。这样可以减少反复创建解释器和导入依赖的开销，同时保留进程级隔离。

进程池必须具备：

- 单任务超时和取消；
- 单进程异常退出后的定向重启；
- 处理达到上限后的主动回收，控制潜在内存增长；
- 忙闲状态和进程健康状态；
- stdout/stderr 协议边界保护；
- 进程崩溃后任务可安全重试或进入失败终态。

### 4.3 第三阶段：独立 Python 处理服务

只有当并发、扩缩容或故障隔离形成真实需求时，才考虑把 Python 拆为独立服务：

```text
API/任务服务 → 任务队列 → Python Processing Service → 共享对象存储
```

这一步会引入服务发现、健康检查、网络超时、鉴权、部署、共享文件或对象存储、服务级限流和独立监控，不能作为当前进程池改造的默认实现。

## 5. Go/Python 协议演进

当前 v1 协议是：

```text
启动进程 → 一条 JSON 请求 → 一条 JSON 响应 → 进程退出
```

常驻进程需要协议 v2，至少包含：

- `request_id`：关联 Go 任务和 Python 响应；
- `document_id`：日志和错误定位；
- `protocol_version`：避免新旧进程混用；
- `status/code/retryable`：结构化失败；
- 处理阶段或耗时字段：支持性能诊断。

请求边界必须使用明确的 framing。首选长度前缀；如果采用 JSON Lines，必须保证每条响应严格单行序列化并增加协议错位测试。不能依赖“读取到 EOF”判断一条请求结束，因为常驻进程不会关闭 stdin。

协议升级时，Application 和 Domain 不应知道 stdin、进程池或 HTTP 细节；替换点应集中在基础设施层的文档处理器实现。

## 6. 后端开发交接

### 当前不变的接口和数据语义

- `POST /documents` 仍只负责上传和内容去重；
- `POST /documents/:id/process` 仍显式创建解析任务；
- `GET /processing-jobs/:id` 仍查询任务真实状态；
- `queued` 表示任务已具备领取条件，不表示已经开始处理；
- `succeeded` 的任务仍需要结合文档状态确认 `ready`；
- 解析失败不会由前端伪造成成功，也不会自动触发向量化。

### 建议增加的配置（计划字段，尚未实现）

```dotenv
DOCUMENT_WORKER_CONCURRENCY=2
PYTHON_PROCESS_POOL_SIZE=2
PYTHON_PROCESS_MAX_DOCUMENTS=100
```

这些配置不是让用户直接控制的产品字段，而是部署和压测参数。实际并发必须受 CPU、内存、数据库连接池和单用户配额共同约束。

### 后端实现顺序

1. 已完成第一版必要指标：`queue_wait_ms`、`processor_ms`、`total_ms`、文件大小、chunk 数、状态和错误分类；结构化日志与 `document_jobs` 同时保留数据。
2. 将当前单 Worker 改为固定大小 Worker Pool，先验证并发数 2。
3. 区分“任务已收尾的业务失败”和“领取/数据库等基础设施错误”，避免业务失败统一触发 2 秒轮询退避。
4. 在阶段耗时明确后，再决定是否使用批量 chunk 写入或 Python 进程池。
5. Python 进程池稳定后，再加入进程回收、崩溃替换和长时间运行测试。
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
后端 PDF 处理并发（当前 1，计划先 2）
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
- 不把进程池计划字段当作当前已存在的 HTTP 接口字段。

## 10. 相关文档

- [PDF 文献处理架构与分阶段路线](../../backend/architecture/pdf-processing-roadmap.md)
- [F2 批量导入与解析队列](../../frontend/architecture/f2-batch-import-queue.md)
- [文档向量化、在线编辑、并发与缓存设计复盘](document-vectorization-editing-concurrency-review.md)
- [HTTP API 总览](../api/http-api-overview.md)
