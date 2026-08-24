# 100 人在线并发架构与交付计划

> 状态：2026-08-22 已完成架构设计与前后端交接基线；本文不表示系统已经具备 100 人生产容量。
>
> 定位：在保持个人版 RAG 产品范围的前提下，把现有单实例模块化单体演进为“100 名同时在线用户下可控、可观测、可扩展”的系统。具体容量必须由固定样本和压测结果验证。

## 1. 目标与非目标

本计划的目标不是让 100 名用户同时占用 100 个 PDF 解析进程，而是在正常读请求、上传洪峰、文档解析、向量化和问答竞争同时存在时：

- API 继续可用，读请求不被重型任务拖垮；
- 每个用户只能消耗被分配的任务与模型资源；
- 超过容量时有明确的排队、429 或 503 语义，而不是无限积压；
- 任务不重复领取、不跨用户泄露、实例异常后可恢复；
- 后续可以通过增加 API 或 Worker 实例扩容，而不重写领域逻辑。

当前不承诺公网生产 SLA，不提前引入 Kafka、Kubernetes 或真正的微服务拆分；团队工作区、成员角色、计费和复杂多租户仍不在本阶段范围。

## 2. 首轮业务负载模型

“100 人在线”必须转换成可重复的测试场景。以下是首轮设计假设，不是已经测得的容量承诺：

| 维度 | 首轮目标 | 说明 |
| --- | ---: | --- |
| 认证在线用户 | 100 | 已登录、可同时浏览与发起请求 |
| 常规 API 峰值 | 20 RPS | 文档列表、任务查询、关键词检索和状态轮询的混合请求 |
| 上传洪峰 | 10 位用户同时上传 | 上传并发与后台解析并发必须分开限制 |
| PDF 解析 | 有界执行、允许排队 | 排队时间必须可观测，并受用户与全局配额限制 |
| 向量化申请 | 最多 100 篇/批请求 | 只创建任务意图，不等于同时调用远程模型 |
| 问答请求 | 少量实时并发 | 满载时短暂等待，随后返回 503 与 Retry-After |

当前本机固定 8 份、共 51.28 MiB PDF 的基线中，2 个 Go 文档 Worker 和 2 个 Python 槽位在 2.0 CPU 配额下完成批次约需 46.076 秒。这个样本大约对应每分钟 10 份文档的墙钟吞吐；若 100 份同类文档同时到达，粗略线性外推的尾部等待会以分钟计。该数字仅用于说明排队问题，不能用于发布承诺。

## 3. 已有基础与关键缺口

| 层面 | 当前已实现 | 100 人目标仍需补充 |
| --- | --- | --- |
| 文档任务 | PostgreSQL 任务表、SKIP LOCKED、1～4 Go Worker、1～4 Python Process Pool、超时与进程回收、用户/全局活动任务准入 | 多实例租约和共享存储 |
| 向量任务 | 独立状态机、用户/全局活动任务准入、1～4 Worker、批量 Provider 调用 | 跨实例 Provider 并发控制与供应商 RPM/TPM 配额 |
| 问答 | 进程内并发闸门、容量耗尽时 503 与 Retry-After | 跨 API 实例的全局闸门和用户级频率限制 |
| 鉴权 | Session、OwnerScope、SQL 隔离、验证码/认证进程内限流 | 多实例共享限流；Session 缓存只在有实测压力时引入 |
| 存储 | 本地私有文件存储、备份恢复 | 对象存储或受控共享卷、跨实例访问和生命周期管理 |
| 观测 | Request ID、访问日志、任务与模型调用指标 | API/Worker/数据库/对象存储的统一看板和告警阈值 |

PostgreSQL 仍是用户权限、文档归属、最终任务状态和向量版本的唯一事实来源。Redis 不能替代这些业务事实。

## 4. 目标部署形态

~~~mermaid
flowchart TB
    User["100 名在线用户"] --> Edge["HTTPS 反向代理 / 边缘限流"]

    Edge --> APIA["API 实例 A"]
    Edge --> APIB["API 实例 B"]

    APIA --> PG[("PostgreSQL<br/>权限、文档、任务、租约")]
    APIB --> PG
    APIA --> Object["共享对象存储"]
    APIB --> Object
    APIA --> Redis["Redis<br/>共享限流、短期并发闸门、进度通知"]
    APIB --> Redis

    PG --> DocA["文档 Worker 实例 A"]
    PG --> DocB["文档 Worker 实例 B"]
    DocA --> Object
    DocB --> Object
    DocA --> PyA["本机 Python Process Pool"]
    DocB --> PyB["本机 Python Process Pool"]

    PG --> Embed["Embedding Worker 实例"]
    Embed --> Redis
    Embed --> EmbedProvider["Embedding Provider"]

    APIA --> Answer["问答服务"]
    APIB --> Answer
    Answer --> Redis
    Answer --> EmbedProvider
    Answer --> GenerationProvider["Generation Provider"]
~~~

这仍然是一个代码仓库、一个 Domain/Application 层和一个 PostgreSQL 业务库。API、文档 Worker、Embedding Worker 是同一程序的不同部署角色，不等于过早拆微服务。

## 5. 不可破坏的系统边界

1. 浏览器不提交 user_id，身份只来自后端 Session 和 Auth Middleware。
2. documents、document_jobs、text_chunks、embedding_jobs 和 chunk_embeddings 的正式状态仍由 PostgreSQL 事务决定。
3. 文档解析与向量化保持独立状态机；用户的向量化意图不能被 PDF Worker 隐式替代。
4. Worker 并发必须是有界配置，不能按 HTTP 请求数创建 goroutine、Python 进程或远程模型调用。
5. 多实例前必须增加 worker_id、lease_expires_at 和续租/过期恢复语义；现有“启动即恢复 processing”的单实例规则不能直接复制。
6. Redis 只保存可丢失、可回源或短生命周期的数据；Redis 不可用时不得绕过鉴权或篡改最终任务状态。
7. 前端显示的排队、重试和预计时间是体验状态，不能替代服务端任务状态。

## 6. 分阶段进度计划

| 阶段 | 状态 | 后端交付 | 前端交付 | 验收出口 |
| --- | --- | --- | --- | --- |
| P100-0 单实例基础 | 已完成 | Worker Pool、Python Pool、向量/问答局部闸门、基础指标 | 有界上传、轮询、任务状态展示 | 固定 PDF 样本已确认当前机器 2/2 为推荐点 |
| P100-1 容量基线与任务准入 | 进行中 | 文档/向量活动任务配额、上传并发闸门、两类 Owner 公平领取和统一 429/503 已完成；付费调参后置 | Retry-After 退避、容量提示、无重复轮询 | 超额请求可预测拒绝，文档与向量 Worker 均不会被单一批量用户长期挤占 |
| P100-2 部署角色拆分 | 计划中 | API、document-worker、embedding-worker 角色配置 | 保持现有 API 契约，不暴露内部 Worker 数量 | API 与 Worker 可独立重启，API 延迟不受解析阻塞 |
| P100-3 多实例安全 | 计划中 | 对象存储、任务租约、心跳、条件收尾、过期回收 | 继续轮询；准备消费进度通知 | 两个 Worker 实例不重复执行，异常实例只恢复过期租约 |
| P100-4 跨实例协调 | 计划中 | Redis 共享限流、模型并发闸门、可选进度通知 | 429/503 统一退避；可选 SSE/WebSocket 适配 | 多 API 实例不会放大验证码、模型或上传压力 |
| P100-5 100 用户验收 | 计划中 | 混合压测、长时间浸泡、故障注入、容量报告 | 真实前端流程与 API 压测对照 | 100 在线用户模型下达到约定 SLO，无重复与跨用户泄露 |

阶段不得跳过：P100-3 的租约和共享存储完成前，不允许为了扩容直接复制多个后端实例。

## 7. 共享 HTTP 与交互契约

P100-1 已冻结以下文档任务和模型容量语义：

文档 Worker 的内部领取采用 Owner 公平规则，不改变 HTTP 契约：多用户竞争时默认每个 Owner 先获得 1 个 processing 槽位；没有其他 Owner 可获得基础槽位时，单一 Owner 最多借用到 2 个；等待超过 2 分钟的 queued 任务进入防饥饿优先级。前端仍只轮询任务真实状态，不感知 Worker 数量或调度游标。

| 场景 | HTTP | 稳定 code 候选 | 前端行为 |
| --- | --- | --- | --- |
| 当前用户活动解析任务达到上限 | 429 | processing_owner_active_job_limit | 保留操作上下文，按 Retry-After 后允许重试 |
| 全局解析队列达到上限 | 503 | processing_queue_capacity_exhausted | 不自动高频重放，提示系统繁忙 |
| 当前用户上传并发达到上限 | 429 | upload_owner_concurrency_exhausted | 保留待上传项，按 Retry-After 后允许重试 |
| 全局上传并发达到上限 | 503 | upload_capacity_exhausted | 停止立即重放，提示系统繁忙 |
| 全局向量任务达到上限 | 503 | embedding_queue_capacity_exhausted | 当前代码已具备，保持语义 |
| 当前用户向量任务达到上限 | 429 | embedding_owner_active_job_limit | 当前代码已具备，保持语义 |
| 在线模型并发已满 | 503 | embedding_provider_capacity_exhausted / answer_capacity_exhausted | 当前代码已具备，读取 Retry-After |

所有容量响应都必须包含安全消息、稳定 code、X-Request-ID 和正整数 Retry-After。前端不得依据英文错误文本判断分支。

## 8. 压测与验收指标

每轮只调整一个变量：Worker 数、实例数、存储类型或限额不能混在同一次结论中。至少记录：

| 维度 | 指标 |
| --- | --- |
| API | RPS、P50/P95/P99、4xx/5xx、429/503、请求体大小 |
| 文档任务 | 每分钟完成量、queue_wait、processor、total、超时、崩溃替换数 |
| 向量/问答 | Provider 调用量、Token、等待时间、Provider 429、容量拒绝数 |
| 资源 | API/Worker CPU、内存、Python 数、磁盘/对象存储 I/O、数据库连接池等待 |
| 正确性 | 重复领取、重复写回、越权读取、任务状态遗漏、租约恢复正确性 |
| 用户体验 | 上传确认延迟、终态显示延迟、重试成功率、单用户公平性 |

压测分为两条线：

1. k6 API 场景：登录、列表、上传、创建任务、轮询、检索和问答；
2. 后台任务场景：固定 PDF、损坏 PDF、重复文件和供应商失败混合提交。

异步任务的成功必须从 job 创建到 succeeded/failed 的完整时间计算，不能把 POST 返回 202 当作任务完成。

## 9. 明确不提前实现的内容

- 不直接引入 Kafka、RabbitMQ 或 Redis Streams 作为任务最终状态来源；
- 不因为 100 人目标就把 Python 解析器拆成独立网络微服务；
- 不把 Session、权限、PDF 正文、chunks 或 vectors 放入 Redis 作为唯一数据；
- 不向前端暴露部署 Worker 数量、Python Pool 大小或数据库连接池；
- 不把 100 在线用户错误表述成 100 份 PDF 必须同时完成。

## 10. 关联交接

- 后端执行清单：[100 人在线后端交接](../../backend/architecture/100-user-concurrency-handoff.md)
- 前端执行清单：[100 人在线前端交接](../../frontend/architecture/100-user-concurrency-handoff.md)
- 当前文档处理事实：[文档处理并发与 Python 进程复用交接](document-processing-concurrency-review.md)
- 当前向量并发事实：[向量任务并发、版本与 Redis 演进交接](../../backend/architecture/document-vectorization-concurrency-handoff.md)
- 统一性能口径：[高性能与高并发工程目标](performance-engineering-goal.md)
