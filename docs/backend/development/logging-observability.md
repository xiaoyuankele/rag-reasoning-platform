# 日志与请求追踪规范

## 1. 当前目标

P5.2 的目标不是“多打印几行文字”，而是让开发者能够回答：

1. 哪一次请求出了问题；
2. 请求经过哪个接口、返回什么状态、耗时多久；
3. 前端看到的安全错误如何与后端内部诊断关联；
4. 后台任务和远程模型调用将来如何沿用同一套字段规则。

P5.2 已完成 HTTP 请求 ID、安全错误双通道、后台任务事件、外部供应商错误分类、模型成本指标、
成本汇总工具以及日志级别/格式配置。

## 2. 请求链路

```text
调用方可选 X-Request-ID
        ↓
RequestIDMiddleware
        ├── 校验或生成 request_id
        ├── 写入响应头 X-Request-ID
        └── 写入标准 context.Context
        ↓
Handler → Application → Infrastructure
        ↓
AccessLogMiddleware 在请求结束后记录最终状态和耗时
```

Application 不导入 Gin。请求 ID 通过已有的 `context.Context` 传递，因此不会破坏业务层的框架独立性。

## 3. 请求 ID 契约

- HTTP 头名称：`X-Request-ID`；
- 允许字符：英文字母、数字、`.`、`_`、`-`；
- 最大长度：128 字符；
- 合法调用方值会被沿用；
- 缺失、空白、含非法字符或过长时由后端生成；
- 请求 ID 只用于追踪，不是身份凭证、权限令牌或幂等键。

## 4. HTTP 访问日志字段

### 4.1 日志级别与输出格式

P5.2.7 在创建正式 Logger 前读取以下配置：

| 环境变量 | 默认值 | 可选值 | 含义 |
| --- | --- | --- | --- |
| `LOG_LEVEL` | `info` | `debug`、`info`、`warn`、`error` | 只输出该级别及更严重的日志 |
| `LOG_FORMAT` | `json` | `json`、`text` | JSON 适合文件/容器/工具，Text 适合本地终端阅读 |

输入会去除首尾空白并忽略大小写。非法值会在数据库连接和 Worker 启动前被拒绝；此时正式 Logger 尚未创建，
程序使用最小 bootstrap JSON Logger 把 `application_stopped` 写到 `stderr` 后退出。

正常应用日志写到 `stdout`。`observability-report` 只能解析 `LOG_FORMAT=json` 的日志，真实成本批次不得使用 Text。

### 4.2 访问日志字段

访问日志使用 Go `log/slog`，固定事件名为 `http_request_completed`。

| 字段 | 含义 |
| --- | --- |
| `time` | 日志时间 |
| `level` | `INFO`、`WARN` 或 `ERROR` |
| `event` | 稳定事件类型 |
| `request_id` | 本次请求的追踪编号 |
| `method` | HTTP 方法 |
| `route` | Gin 路由模板，例如 `/documents/:id` |
| `status_code` | 最终 HTTP 状态码 |
| `duration_ms` | 从进入中间件到响应完成的毫秒数 |
| `response_bytes` | 响应体字节数 |
| `error_count` | Gin 请求上下文中登记的错误数量 |

日志级别规则：

- `100～399`：`INFO`；
- `400～499`：`WARN`；
- `500` 及以上：`ERROR`。

## 5. 安全边界

HTTP 访问日志不得记录：

- `Authorization`、Cookie、API Key 或数据库密码；
- 完整查询字符串和问答正文；
- 上传文件内容；
- 可直接返回给前端的内部堆栈和数据库错误。

访问日志只记录路由模板。例如真实请求 `/documents/42` 记录为 `/documents/:id`，便于统计并减少不必要的数据暴露。

### 5.1 HTTP 错误响应契约

第一版错误响应包含两个字段：

```json
{
  "error": "document not found",
  "code": "document_not_found"
}
```

- `error`：适合调用方阅读的安全消息，措辞以后可以调整；
- `code`：稳定的程序判断字段，前端不应通过匹配英文消息判断错误类型；
- 400、404、409 等可预期错误由访问日志记录最终状态，不重复写内部 ERROR 日志；
- 未知 500 错误只向调用方返回 `internal_error`，不得返回数据库错误、路径、密钥或堆栈。

### 5.2 内部诊断日志契约

关键 Handler 遇到未知内部错误时，通过统一辅助函数记录：

| 字段 | 含义 |
| --- | --- |
| `event` | 固定为 `http_request_failed` |
| `request_id` | 与响应头对应的请求编号 |
| `public_error_code` | 调用方收到的稳定错误码 |
| `diagnostic_code` | 后端定位失败位置使用的稳定诊断码 |
| `error` | 仅写入后端日志的原始错误 |
| 业务标识 | 例如 `document_id` 或 `processing_job_id` |

当前已覆盖 `GET /documents/:id` 和 `GET /processing-jobs/:id`，形成后续 Handler 迁移时可复用的代表性实现。

## 6. 分层位置

- `internal/observability`：请求 ID 在标准 context 中的读写契约；
- `internal/api`：请求 ID 与访问日志 Gin 中间件；
- `cmd/server/main.go`：创建 JSON Logger 并注入 Router、Worker 错误报告器；
- Domain：不依赖日志框架；
- Application：继续传递 context，不负责 HTTP 访问日志。

## 7. 当前验收结果

- 请求 ID context、HTTP 中间件和日志字段自动化测试通过；
- 后端全量 `go test -count=1 ./...` 和 `go vet ./...` 通过；
- 真实 `GET /health` 请求携带 `frontend-smoke-001`，响应头和 JSON 日志返回相同 ID；
- 真实日志包含 `/health`、`200`、耗时、响应字节数和零错误计数；
- 远程 Embedding、语义检索与问答开关关闭，没有产生远程费用。
- 文档查询和解析任务查询已经返回稳定错误码；
- 两个代表性 Handler 的测试验证了可预期错误不写内部 ERROR 日志；
- 未知错误测试验证了前端响应不泄漏原始错误，同时后端日志保留 `request_id`、诊断码、业务 ID 和原始错误；
- P5.2.2 第一版完成后，后端全量 `go test -count=1 ./...` 与 `go vet ./...` 再次通过。

### 7.1 文档解析任务生命周期

P5.2.3 在 Application 定义 `ProcessingJobEventObserver`，由 `internal/observability` 的 slog 适配器实现。
Application 只报告任务事实，不依赖具体日志框架；`main.go` 负责把两者组装起来。

| 事件 | 级别 | 含义 |
| --- | --- | --- |
| `processing_job_started` | `INFO` | Worker 已领取任务，数据库状态为 `processing` |
| `processing_job_succeeded` | `INFO` | chunks 和任务成功结果都已落库 |
| `processing_job_failed` | `ERROR` | 处理失败，而且任务已经安全写入 `failed` |
| `processing_job_unfinished` | `ERROR` | Worker 未能写入终态，任务可能仍停留在 `processing` |

所有任务事件包含 `processing_job_id`、`document_id`、`attempt_count`、`status` 和
`queue_wait_ms`。终结事件还包含 `processor_ms`、`total_ms`、`file_bytes` 和
`chunk_count`。失败事件使用稳定 `error_code` 供数据库统计，同时仅在后端日志保留原始
`error`。日志和指标表都不记录正文、存储路径、密钥或上传内容。

第一版先测量排队、整个文档处理器调用和 Worker 总执行。第二版继续拆分
`source_open_ms`、`metadata_read_ms`、`text_extract_ms`、`text_split_ms`、
`python_total_ms` 和 `chunk_write_ms`，并记录页数、最慢页码和最慢页耗时。
这些字段持久化在对应 `document_jobs` 记录，历史任务、旧处理器或没有执行的
阶段保持 `NULL`；真实执行但不足 1ms 的阶段记录为 `0`。

`processor_ms` 仍表示 Go 看到的整个处理器调用，包含 Python 进程池等待、
协议往返和 JSON 编解码；`python_total_ms` 只表示 Python 应用服务内部的提取、
规范化和分块。两者的差值可用于定位外围开销。`finalize_ms` 在数据库提交完成后
才能准确得到，因此只写终结事件日志，不为了持久化它再额外更新一次任务记录。

固定大小 Worker Pool 启动时额外记录 `document_worker_pool_configured`，字段
`concurrency` 表示同一后端实例内运行的文档 WorkerLoop 数量。Python 处理器组装时记录
`python_document_processor_configured`，包含 `mode`、`pool_size` 和
`max_documents_per_process`。默认 `oneshot` 下并发为 2 意味着最多同时启动两个一次性
Python 子进程；`pool` 模式下实际复杂文档并发受 Go Worker 数和 Python 槽位数共同约束。
第一版要求池大小不小于 Go Worker 数，避免任务已经变成 `processing` 却只在池入口等待。

当前中断恢复策略是服务启动后将遗留 `processing` 任务标记为失败，并不存在重新排队行为，
因此这一版没有记录与实际业务不一致的 `requeued` 事件。

### 7.2 Embedding 任务生命周期与调用成本

P5.2.4 为 Embedding Worker 建立独立的 `JobEventObserver`。它与文档解析观察器采用相同依赖方向：
Application 定义事件，`internal/observability` 使用 slog 实现，`main.go` 完成注入。

| 事件 | 级别 | 含义 |
| --- | --- | --- |
| `embedding_job_started` | `INFO` | 已领取任务并进入 `processing` |
| `embedding_job_succeeded` | `INFO` | 全部向量与 Token 使用量已经原子落库 |
| `embedding_job_requeued` | `WARN` | 临时错误，已经设置下一次执行时间 |
| `embedding_job_failed` | `ERROR` | 永久错误或重试耗尽，已经写入 `failed` |
| `embedding_job_interrupted` | `WARN` | 服务 shutdown 中断，留给启动恢复重新排队 |
| `embedding_job_unfinished` | `ERROR` | 数据库收尾失败，任务可能仍停留在 `processing` |

终结事件记录 `model_name`、`dimensions`、`provider_call_count`、`provider_duration_ms`、
`prompt_tokens`、`total_tokens` 和 `generated_vector_count`。重试事件额外记录 `next_attempt_at`；失败事件使用
`provider_authentication`、`provider_quota`、`provider_rate_limit`、`provider_request_rejected`、
`provider_invalid_response`、`timeout`、`canceled`、`document_has_no_chunks` 或 `internal` 分类。

成本统计遵循“调用已经发生就必须记录”的原则：如果第一批向量成功而第二批失败，第一批产生的 Token、
远程耗时和调用次数仍然保留在日志中；这些部分向量不会被持久化，只有 `succeeded` 才表示全部向量已经落库。

2026-08-24 在既有重试机制上补充恢复观测，不增加业务表或重复状态字段：

- `retry_count=max(attempt_count-1, 0)`，表示当前任务已经经历的重试次数；
- `recovered=true` 只出现在“至少重试一次后最终成功”的 `embedding_job_succeeded`；
- `finalization_duration_ms` 记录数据库收尾耗时。成功时覆盖“替换该文档全部向量 + 更新 Job 为
  `succeeded`”的原子事务；失败或重排队时覆盖对应的 Job 状态写入；
- `attempt_count` 继续由 PostgreSQL 在原子领取任务时递增，是恢复判定的事实来源。日志只是投影，不能反向改变任务状态。

因此，一次提供方超时后恢复会形成两条可关联事实：第一次尝试写
`embedding_job_requeued`，第二次尝试写 `embedding_job_succeeded` 且 `retry_count=1`、
`recovered=true`。如果数据库收尾本身失败，则事件仍是 `embedding_job_unfinished`，不能误报恢复成功。

### 7.3 Generation 在线调用与拒答

P5.2.5 在 Answer Application 定义 `GenerationEventObserver`，由 `internal/observability` 的 slog 适配器实现。
它与后台 Worker 的主要区别是：在线问答没有任务 ID，而是使用 HTTP context 中的 `request_id` 关联访问日志和生成日志。

| 事件 | 级别 | 含义 |
| --- | --- | --- |
| `answer_generation_skipped` | `INFO` | 没有检索证据，返回稳定拒答且没有调用远程模型 |
| `answer_generation_started` | `INFO` | 已完成证据选择和 Prompt 构造，即将调用 Generator |
| `answer_generation_succeeded` | `INFO` | 远程生成成功并取得答案与 Token 用量 |
| `answer_generation_failed` | `ERROR` | 远程生成失败，原始错误只保留在后端日志 |

所有事件记录 `model_name`、`response_language`、`requested_top_k` 和 `evidence_count`；指定文档问答额外记录
`document_id`。成功事件记录 `provider_duration_ms`、`prompt_tokens`、`completion_tokens` 和 `total_tokens`；
失败事件使用 `provider_authentication`、`provider_quota`、`provider_rate_limit`、
`provider_request_rejected`、`provider_unavailable`、`provider_invalid_response`、`timeout`、`canceled`
或 `internal` 分类。无证据事件记录 `skip_reason=insufficient_evidence`。

Generation 日志严格禁止记录用户问题、System Instruction、User Prompt、证据正文、生成答案和 API Key。
HTTP 访问日志负责整次请求的状态与总耗时，Generation 事件只负责远程生成阶段，二者不能混为同一个指标。

### 7.4 Answer 并发准入

在线问答在原有 Answer Service 外增加进程内并发包装器。一个槽位覆盖问题向量化、数据库语义检索和远程答案
生成的完整链路，防止大量并发请求同时占满数据库连接、远程 API 配额和本机资源。

| 事件 | 级别 | 含义 |
| --- | --- | --- |
| `answer_request_admitted` | `INFO` | 请求取得并发槽位，开始执行完整问答链路 |
| `answer_request_rejected` | `WARN`/`INFO` | 等待容量超时，或客户端在等待期间取消 |
| `answer_request_released` | `INFO` | 问答成功或失败，槽位已经通过 `defer` 归还 |

事件记录 `request_id`、`wait_duration_ms`、`in_flight`、`max_concurrency` 和 `outcome`；释放事件额外记录
`execution_duration_ms`。`outcome` 使用 `succeeded`、`downstream_error`、`capacity_timeout` 或 `canceled`。
容量超时使用 `WARN`，客户端取消使用 `INFO`。这些日志同样禁止记录问题、Prompt、答案、证据和 API Key。

当前闸门是**单后端进程级**限制：如果未来水平扩容为多个后端副本，总并发约等于“副本数 × 每副本上限”，
届时若要形成全局配额，需要增加 Redis/数据库等分布式准入能力。

### 7.5 远程 Embedding 分类隔离与全局准入

后台向量 Worker、在线语义检索和问答内部语义检索使用两个不同等待策略的 `GatedEmbedder`，并持有同一个
包含 Worker、Online、Global 三组槽位的 `EmbeddingProviderGate`。分类槽位隔离后台和在线容量，全局槽位负责
保证所有远程 Embedding HTTP 调用合计不会超额。

| 事件 | 级别 | 含义 |
| --- | --- | --- |
| `embedding_provider_request_admitted` | `INFO` | Worker 或在线请求取得共享槽位 |
| `embedding_provider_request_rejected` | `WARN`/`INFO` | 在线等待容量超时，或等待中的 context 被取消 |
| `embedding_provider_request_released` | `INFO` | 远程调用成功、失败或取消后已经归还槽位 |

事件记录 `origin=worker/online`、`request_id`（在线链路存在时）、`wait_duration_ms`、分类容量字段
`origin_in_flight/origin_max_concurrency`、全局容量字段 `in_flight/max_concurrency` 和 `outcome`；释放事件增加
`execution_duration_ms`。日志禁止记录输入文本、向量、远程响应正文和 API Key。默认 Worker/Online/Global
分别为 2/2/4，在线等待 2 秒；后台 Worker 只随任务 context 或 shutdown 取消。

该闸门控制并发数，不控制提供方的 RPM、TPM 或账户配额。分类槽位解决 Worker 饿死在线请求的问题，但在线
类别内部仍可能出现跨用户饥饿；届时需要再设计用户级公平调度，而不是盲目提高并发数。

### 7.6 可重复成本与问答并发汇总

P5.2.6 第一部分提供 `go run ./cmd/observability-report`。该命令只消费已有 JSONL 日志，不启动服务、
不访问数据库，也不调用远程模型。它排除 started 事件，按终结事件汇总 Embedding/Generation 的调用次数、
Token、成功/失败/跳过状态以及平均、P50、P95 耗时。重试前已经发生的 Embedding 调用继续计入成本，
无证据 Generation `skipped` 不计远程调用。

2026-08-22 报告结构升级为 `schema_version=2`，在原有 `embedding` 和 `generation` 外增加
`answer_admission`。它汇总 `admitted`、`rejected`、`released` 事件数，按 outcome 区分成功、下游失败、
容量等待超时和客户端取消，并输出等待耗时、执行耗时和 `max_observed_in_flight`。同一条成功请求会同时写
`admitted` 与 `released`，所以等待耗时只采用 `released` 或 `rejected` 终结事件，避免重复计算；执行耗时
只采用 `released`。

2026-08-24 报告结构升级为 `schema_version=3`，Embedding 汇总增加：

- `retried_job_count`：进入 `succeeded` 或 `failed` 终态且至少重试过一次的任务数；
- `recovered_job_count`：重试后成功的任务数；
- `retry_exhausted_count`：重试后仍进入 `failed` 的任务数；
- `recovery_rate=recovered_job_count/retried_job_count`，只使用已经确定的成功/失败终态；
- `finalization_duration`：数据库原子收尾阶段的 Count、平均值、P50、P95 和最大值。

汇总器能够从旧日志已有的 `attempt_count` 推导重试次数，因此历史结构化日志仍可使用；旧日志没有
`finalization_duration_ms` 时，该耗时样本为空，不会伪造为 0 毫秒。

完整冻结条件、PowerShell 命令、字段口径和金额换算方法见
[模型调用成本基线](../performance/model-call-cost-baseline.md)。真实付费批次必须单独获得授权。

### 7.6 应用进程生命周期与启动恢复

P5.3.3 增加三个进程级事件：`application_started`、`application_shutdown_started` 和
`application_stopped`。它们由 `main.go` 记录，因为信号、HTTP Server、Worker goroutine 和数据库连接池都在
组合根中组装；Application 不应感知 Docker 信号。

异常重启恢复继续使用既有 `processing_jobs_recovered` 与 `embedding_jobs_requeued`。前者表示遗留文档解析
任务已转为 `failed`，后者表示遗留向量任务已回到 `queued`，两个名字不能当成同一种恢复动作。

完整信号顺序、恢复规则和真实退出码见
[容器优雅关闭与异常恢复](../deployment/container-lifecycle-and-recovery.md)。

## 8. 后续计划

1. 按实际联调需要把统一错误响应逐步迁移到其余 Handler；
2. 在明确授权后执行第一批新口径真实成本基线；
3. 在 P5.4 固化默认零远程费用回归命令。

### 8.1 P6 个人用户域日志交接（计划中）

P6 身份接入后，访问日志中可以增加可空的数值 `user_id` 和 `session_id`。Access Log Middleware 位于
Auth Middleware 外层时，应在请求返回阶段读取后者写入 Gin Context 的 Actor；未认证请求保持字段缺失，
不能使用 `0` 冒充用户。

认证事件只记录稳定事件、结果和原因分类：

```text
auth_registration_succeeded
auth_registration_failed
auth_login_succeeded
auth_login_failed
auth_logout_completed
```

成功事件可以记录数值 `user_id`、`session_id`；失败事件不能记录明文邮箱、密码、Cookie、原始 Session Token
或密码哈希。若未来确实需要关联重复攻击，应单独评审不可逆标识和保留周期，不能直接把用户输入写进日志。

文档、解析、Embedding 和 Generation 事件可以增加 `owner_user_id`，但不得记录用户邮箱或显示名。当前
`request_id` 仍然只是追踪标识，不得被当作 Session、CSRF Token 或幂等凭证。详细身份边界见
[P6 个人用户域后端交接](../architecture/personal-user-backend-handoff.md)。
