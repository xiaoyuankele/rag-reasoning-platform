# 100 人在线并发演进：后端开发交接

> 状态：2026-08-24。单实例文档任务准入、上传并发闸门、Owner 公平领取和数据库连接池配置已经完成；多实例改造尚未开始。
>
> 本文只描述 Go、Python、PostgreSQL、对象存储、Redis 和部署角色的后端工作；跨端 HTTP 语义以 shared/architecture/100-user-concurrency-plan.md 为准。

## 1. 当前后端可复用能力

- Gin API、Request ID、结构化访问日志、Recovery、Session 与 OwnerScope；
- PostgreSQL documents、document_jobs、text_chunks、embedding_jobs、chunk_embeddings；
- 文档任务使用 SKIP LOCKED 领取，文档 Worker 与 Python Process Pool 都是固定大小；
- Python stream 协议支持惰性启动、超时取消、崩溃替换与按任务数回收；
- 向量任务已经有活动任务幂等、用户/全局准入、Embedding Worker Pool 与 Provider 并发闸门；
- Answer Service 已有进程内并发闸门，容量不足返回 503 与 Retry-After；
- 文件存储、文档处理器、Embedding Provider 都已经位于 Infrastructure Port 后面，具备替换基础。

当前边界：API、文档 Worker、Embedding Worker 仍由同一 server 进程启动；文件存储为本地路径；恢复逻辑只适用于确认旧进程已退出的单实例环境。

## 2. B100-1：文档解析任务准入与容量错误（已完成）

目标是在创建 document_jobs 前用数据库保证用户公平性和全局背压。

### 2.1 已落地配置

当前代码已经冻结下列配置名和单实例默认值：

    PROCESSING_MAX_ACTIVE_JOBS_PER_USER=5
    PROCESSING_MAX_ACTIVE_JOBS_GLOBAL=40
    PROCESSING_MAX_IN_FLIGHT_PER_OWNER=1
    PROCESSING_MAX_BORROWED_IN_FLIGHT_PER_OWNER=2
    PROCESSING_STARVATION_THRESHOLD=2m
    UPLOAD_MAX_CONCURRENCY_PER_USER=2
    UPLOAD_MAX_CONCURRENCY_GLOBAL=16
    UPLOAD_QUEUE_WAIT_TIMEOUT=2s

活动文档任务定义为 queued 与 processing。创建解析任务时同时执行用户级和全局数据库准入；上传请求则先经过进程内用户级/全局并发闸门。默认值是首测起点，不是 100 人容量结论，后续只能依据固定场景压测逐项调整。

### 2.2 已落地事务语义

创建解析任务必须在一个短 PostgreSQL 事务中完成：

    1. OwnerScope 内锁定 documents 行；
    2. 若已有该文档活动任务，保持现有幂等/冲突语义；
    3. 取得事务级 advisory lock；
    4. 统计当前 owner 与全局活动 document_jobs；
    5. 超过 owner 上限返回领域限流错误；
    6. 超过全局上限返回领域容量错误；
    7. INSERT document_jobs；
    8. 提交事务。

advisory lock 只覆盖“计数 + 插入”的短临界区，绝不能覆盖 PDF 解析、文件读取或远程模型调用。实现模式应复用 ScopedEmbeddingJobRepository 的准入逻辑，而不是新增进程内 mutex。

### 2.3 已落地 HTTP 映射

在 shared 契约冻结后，DocumentProcessingHandler 应映射：

| 领域结果 | HTTP | code | 响应要求 |
| --- | --- | --- | --- |
| owner active limit | 429 | processing_owner_active_job_limit | Retry-After、X-Request-ID |
| global queue full | 503 | processing_queue_capacity_exhausted | Retry-After、X-Request-ID |
| 同文档已有活动任务 | 保持当前语义 | 不把幂等点击误报为容量不足 | 返回真实任务状态或稳定冲突 |
| owner upload concurrency full | 429 | upload_owner_concurrency_exhausted | Retry-After、X-Request-ID |
| global upload concurrency full | 503 | upload_capacity_exhausted | Retry-After、X-Request-ID |

批量上传中的每一项必须独立处理，不能因一份文档限流回滚其他已接受项。

### 2.4 已落地 Owner 公平领取

准入上限回答“一个用户最多能排多少任务”，公平领取回答“Worker 下一次先服务谁”，二者不能混为同一个限制。当前单实例调度规则是：

1. Worker 第一遍只选择 `processing` 数量低于 `PROCESSING_MAX_IN_FLIGHT_PER_OWNER` 的 Owner；
2. 防饥饿 Owner 优先，其余按 `last_dispatched_at`、最老 queued 时间和 Owner ID 稳定排序；
3. 锁定 Owner 调度行后，再锁定该 Owner 最早的 queued 任务；
4. 只有第一遍没有可服务 Owner 时，第二遍才允许同一 Owner 借用到 `PROCESSING_MAX_BORROWED_IN_FLIGHT_PER_OWNER`；
5. 领取任务、更新文档状态和更新 Owner 调度游标在同一个 PostgreSQL 事务中提交；
6. 活动数仍实时来自 `document_jobs`，`document_processing_owner_schedules` 只保存调度游标，不复制任务计数。

默认 `1/2/2m` 的含义是：竞争时先做到“一人一个”，无人竞争时允许繁忙用户使用第二个 Worker；等待超过两分钟的队列进入防饥饿优先级。该规则是单实例第一版，不等于已经解决多实例租约问题。

真实 PostgreSQL 集成测试已经覆盖：不同 Owner 先获得基础槽位、同一 Owner 借用空闲容量、达到借用上限后继续排队、任务结束后释放槽位、防饥饿优先，以及两个并发 Worker 不重复领取且优先选择不同 Owner。

下一项工作不是继续猜测调高限额，而是使用固定账号、固定文件和分阶段并发压测，记录吞吐、P95/P99、容量拒绝、队列等待、资源和数据库连接池指标，寻找当前单机拐点。

## 3. B100-2：API 与 Worker 角色拆分

目标是使重型 PDF 任务不再占用 API 实例的 CPU、内存和关闭窗口。

候选环境变量：

    APP_ROLE=all | api | document-worker | embedding-worker | reaper
    INSTANCE_ID=<deployment-generated-unique-id>

第一版允许保留 APP_ROLE=all 作为本地开发兼容模式。生产/压测环境建议：

| 角色 | 启动内容 | 不启动内容 |
| --- | --- | --- |
| api | HTTP、认证、文档/任务 API、在线搜索与问答 | 文档与向量后台循环 |
| document-worker | 文档领取、Python Pool、文档任务收尾 | HTTP 监听、Embedding Worker |
| embedding-worker | 向量领取、远程调用、向量收尾 | HTTP 监听、Python Pool |
| reaper | 已过期租约回收、可选维护任务 | 不处理活跃任务 |

角色拆分只调整组合根和部署方式；Handler、Application、Domain、Repository 和迁移保持同一代码库。API 与 Worker 使用同一 PostgreSQL 连接配置，但必须分别配置连接池上限和超时。

## 4. B100-3：对象存储迁移

多 Worker 实例必须读取同一份原始 PDF，本地 storage/ 不能继续作为唯一共享介质。

### 4.1 迁移原则

- 继续使用现有 FileStorage / Open / Delete 等领域端口；
- documents.storage_path 保持内部字段，不对 HTTP 暴露；
- 迁移后 storage_path 表示不透明对象 key，而不是宿主机绝对路径；
- API 先继续代理上传，由后端计算 SHA-256、校验 MIME 和写入元数据；
- 预签名直传不进入首轮，避免把对象上传完成、哈希校验和数据库建档拆成不一致流程；
- 先提供双读或一次性迁移工具，再删除本地存储依赖；
- 对象存储权限按服务端凭据管理，浏览器不会获得其他用户对象列表权限。

### 4.2 验收

- 任意 API 实例上传的文件可由任意文档 Worker 读取；
- 删除文档可清理对象，失败记录在可恢复的补偿流程中；
- 备份与恢复覆盖 PostgreSQL 元数据和对象清单；
- 公开 HTTP 响应仍不包含 storage_path 或对象 key。

## 5. B100-4：多实例任务租约

在启动第二个 Worker 实例前，document_jobs 与 embedding_jobs 都需要受控迁移，新增：

    worker_id
    lease_expires_at
    heartbeat_at

建议语义：

1. 领取 queued 任务时，在同一事务内写入 processing、worker_id 和 lease_expires_at；
2. 长任务按固定间隔续租；续租条件必须同时匹配 job_id、processing、worker_id；
3. Reaper 只处理 processing 且 lease_expires_at 小于当前时间的任务；
4. 成功、失败和重试收尾都必须匹配 worker_id 与当前租约；
5. 租约丢失的旧 Worker 不得写回 chunks、vectors 或终态；
6. 文档解析收尾仍需要保持 chunks 替换与 document/job 状态的一致事务边界；
7. 向量写回继续在同一事务中替换向量并更新 embedding_jobs。

现有 InterruptedJobRecoveryService 必须在多实例模式下被“仅恢复过期租约”的实现替代。不能在 API 或 Worker 启动时无条件重置所有 processing 任务。

## 6. B100-5：Redis 的精确职责

Redis 在本阶段只用于跨实例短期协调：

| 用途 | 数据语义 | Redis 故障降级 |
| --- | --- | --- |
| 登录/验证码/上传 API 限流 | 短期计数或令牌桶 | 保守进程内限流或拒绝，不绕过认证 |
| Embedding / Generation 并发闸门 | 带 TTL 的短租约槽位 | 快速失败并返回 503，不无限制直连 Provider |
| 任务进度通知 | 可丢失事件 | 前端退回 GET 轮询，PostgreSQL 仍是终态来源 |
| Session Cache-Aside | 可回源缓存 | 回源 PostgreSQL，撤销仍以数据库为准 |

不使用 Redis 作为 document_jobs、embedding_jobs、OwnerScope、PDF、chunks、vectors 或审计数据的唯一来源。

当前进程内 EmbeddingProviderGate 和 Answer ConcurrentService 在单实例时继续有效；多实例模式下应替换其底层槽位实现或增加 Redis-backed adapter，使全部实例竞争同一组供应商容量。

## 7. 可观测性与容量配置

新增或拆分以下指标：

    api_request_duration_ms
    document_admission_rejected_total
    document_queue_depth
    document_lease_recovered_total
    worker_lease_renewal_failure_total
    object_storage_read_ms
    object_storage_write_ms
    database_pool_acquire_ms
    embedding_provider_wait_ms
    answer_admission_wait_ms

所有指标必须含 role、instance_id、任务类别和安全的错误分类；不得记录 PDF 正文、Session、Prompt 或完整用户输入。

配置值分为三类：

- 部署级：APP_ROLE、INSTANCE_ID、数据库连接池、对象存储、Redis；
- 资源级：Worker 数、Python Pool、文档/向量活动任务上限、Provider 并发；
- 产品级：文件大小、批次大小、Retry-After、问答 TopK。

资源级配置要通过启动校验检查相互关系，例如 Worker 并发不能大于本实例 Python Pool，分配给后台/在线的 Provider 并发不能超过全局 Provider 容量。

## 8. 后端验收清单

- 两个并发请求不会绕过同一用户或全局 document_jobs 上限；
- 超限请求只返回 429/503，不写入半成品任务；
- 两个 Worker 实例不会领取同一任务；
- Worker 失联后只在租约过期时恢复，健康 Worker 不被误中断；
- 旧 Worker 在租约丢失后无法覆盖新 Worker 的结果；
- API 实例重启不会停止独立 Worker 已领取的任务；
- 对象存储切换后上传、解析、删除、备份、恢复与 OwnerScope 仍正确；
- Redis 故障时权限和正式任务状态仍正确，容量保护采用明确降级；
- 固定样本、损坏 PDF、供应商 429/500、取消、删除和 30～60 分钟浸泡均有自动化或人工验收记录。

## 9. 交接依赖

- 共享目标与阶段：[100 人在线并发架构与交付计划](../../shared/architecture/100-user-concurrency-plan.md)
- 前端行为：[100 人在线前端交接](../../frontend/architecture/100-user-concurrency-handoff.md)
- 当前向量任务实现：[向量任务并发、版本与 Redis 演进交接](document-vectorization-concurrency-handoff.md)
- 当前文档任务实现：[文档处理并发与 Python 进程复用交接](../../shared/architecture/document-processing-concurrency-review.md)
