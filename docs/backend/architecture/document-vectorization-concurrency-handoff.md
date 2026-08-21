# 后端交接：向量任务并发、版本与 Redis 演进

> 状态：2026-08-21 部分完成。提交 `c4696bc` 已实现等待意图、幂等申请、批量申请、取消和解析成功原子激活；
> 后续已补充按最多 100 个文档 ID 一次发现各自最新任务的 OwnerScope 查询；
> 内容 revision、活动任务下的重新解析/删除约束、Worker 租约、配额和 Redis 仍未实现。跨端产品决策见
> [文档向量化、在线编辑、并发与缓存设计复盘](../../shared/architecture/document-vectorization-editing-concurrency-review.md)。

## 1. 当前可复用基础

后端已经具备：

- `document_jobs` 与 `embedding_jobs` 两张独立任务表；
- ready 文档的显式单文档向量任务创建和查询；
- `OwnerScope` 在 Handler、Application 和 SQL 三层的向量任务隔离；
- PostgreSQL `FOR UPDATE SKIP LOCKED` 原子领取；
- 活动向量任务唯一约束、有限重试、Token 和生命周期日志；
- PDF Worker 与 Embedding Worker 独立循环。
- `waiting_document` 持久意图和解析成功事务内激活；
- 最多 100 份文档的逐项批量申请；
- `waiting_document/queued` 取消和 `processing` 冲突保护。
- 按文档批量发现最新任务，供前端跨会话恢复状态并避免 N+1 查询。

本轮不把向量调用塞进 Python Processor 或 PDF Worker。新增的是“用户持久意图”和可证明正确的状态转换，
不是把两条链路重新耦合。

## 2. 建议实施批次

```text
[完成] V1 状态与约束
  → [完成] V2 单个/批量申请和取消
  → [完成] V3 解析成功原子激活 waiting 任务
  → [完成] V3.1 按文档批量发现最新任务
  → [计划] V4 revision 与重新解析一致性
  → [计划] V5 并发配额、压测和 Worker 池
  → [计划] V6 Worker 租约与 API/Worker 分进程
  → [计划] V7 可选 Redis 限流、Session 缓存和进度通知
```

每批先更新共享 API 契约和测试，再修改实现。迁移编号使用实现时仓库中的下一个可用编号，本文不提前占号。

## 3. 数据模型方向

### 3.1 向量任务状态

当前向量状态已经扩展为：

```text
waiting_document
queued
processing
succeeded
failed
canceled
```

后续字段方向：

```text
embedding_jobs
├─ document_id
├─ model_name
├─ dimensions
├─ document_revision / chunk_revision
├─ status
├─ attempt_count
├─ next_attempt_at
├─ worker_id                  多实例阶段
├─ lease_expires_at           多实例阶段
└─ started_at / completed_at
```

活动任务唯一性建议覆盖文档、模型、维度和 revision：

```sql
CREATE UNIQUE INDEX ...
ON embedding_jobs (
    document_id,
    model_name,
    dimensions,
    document_revision
)
WHERE status IN ('waiting_document', 'queued', 'processing');
```

具体是否允许同一文档同一 revision 同时生成多个模型由产品能力决定；第一版继续保持一个当前配置模型，
但数据约束不要通过 Go 代码的“先查再插”实现。

### 3.2 内容版本

计划增加 `documents.current_revision`，正式内容可以保存在 revision 表或版本化文件路径中。`text_chunks` 和
`chunk_embeddings` 必须能够判断自己属于哪个 revision。

第一版如果暂不实现完整版本表，至少冻结一个保守规则：存在活动向量任务时拒绝重新解析或修改正文，返回稳定 `409`。
完整版本实现后，Worker 只能写回仍等于任务快照 revision 的结果。

### 3.3 用户默认偏好

账户默认值可以后置为 `user_preferences.default_vectorization_mode`。创建文档或任务时需要把本次决定保存为独立事实；
以后修改偏好不能通过后台扫描隐式改变历史文档。

## 4. 申请向量化的事务规则

单个和批量接口应复用同一个 Application 用例，例如：

```go
type QueueRequestedDocument struct {
    DocumentID int64
}

type QueueBatchInput struct {
    Documents []QueueRequestedDocument
}
```

用例显式接收 `OwnerScope`，限制请求数量、去重输入 ID，并让 Repository 在 SQL 中再次限定 owner。不能把客户端
提交的 `user_id`、文档 DTO 或页面选中状态当成可信归属。

每份文档在事务内按统一顺序锁定：

```sql
SELECT id, status, current_revision
FROM documents
WHERE id = $1
  AND owner_user_id = $2
FOR UPDATE;
```

- `ready`：创建 `queued`；
- `uploaded/processing/failed`：创建 `waiting_document`；
- 已存在活动任务：幂等返回已有任务和真实状态；
- 已存在成功任务：当前允许创建新的重建任务；revision 和 `already_succeeded` 仍待后续设计；
- 不存在或不属于当前用户：沿用 404 隔离语义。

为什么需要锁 document 行：如果申请事务看到旧状态准备写 waiting，而解析成功事务已经执行完“激活等待任务”的查询，
就可能遗留永远等待的任务。两条路径都先锁同一 document 行后，无论谁先执行，后执行者都能看到正确状态并完成转换。

批量请求硬上限已经冻结为 100 个 ID；Application 先校验、去重并保持首次出现顺序，再逐份调用同一个申请用例。
每份文档使用独立短事务，因此一份不存在或内部失败不会回滚其他文档已经创建/复用的任务；批量 HTTP 请求本身不调用
远程模型或文件系统。实际向量生成仍由异步 Worker 按文件任务处理。

## 5. 解析成功与 waiting 任务激活

解析成功的数据库收尾需要在锁定文档后原子完成：

```text
文档/解析任务进入成功状态
  + 当前 chunks revision 生效
  + matching waiting_document 任务进入 queued
```

示意 SQL：

```sql
UPDATE embedding_jobs
SET
    status = 'queued',
    next_attempt_at = CURRENT_TIMESTAMP,
    updated_at = CURRENT_TIMESTAMP
WHERE document_id = $1
  AND document_revision = $2
  AND status = 'waiting_document';
```

如果现有 chunks 替换与 document/job 收尾仍分属不同事务，实施 revision 时需要重新审视崩溃窗口；不能在 chunks 尚未
成为正式版本前让 Embedding Worker 领取新任务。可以通过单事务提交、显式 revision 可见性或 outbox/reconciler 收口，
第一版优先使用同一个 PostgreSQL 事务，不引入外部消息系统。

## 6. 领取、取消和重试竞态

### 6.1 Worker 领取

只领取 `queued`，不再通过 JOIN ready 文档把 `waiting_document` 混入可执行队列。领取与状态更新保持同一事务，
继续使用 `FOR UPDATE SKIP LOCKED`。

### 6.2 用户取消

当前取消接口为 `POST /embedding-jobs/:id/cancel`。Repository 在同一事务中按统一顺序锁 document 和 job，再依据
锁内最新状态转换：

```text
BEGIN
  → 在 OwnerScope 内定位任务所属 document
  → FOR UPDATE 锁 document
  → FOR UPDATE 锁 embedding_job
  → 按锁内最新状态决定 UPDATE 或领域错误
COMMIT
```

`waiting_document/queued` 转为 `canceled`；已经 canceled 幂等返回 `200`；processing、succeeded、failed 返回稳定
`409`；不存在/越权返回 `404`。第一版不杀死已经进入远程供应商调用的任务。

### 6.3 重试

临时供应商错误继续有限退避；永久认证、配额和请求错误不盲目重试。用户手动重试失败任务时创建新尝试或把 failed
原子转回 queued 的语义必须二选一并冻结。无论采用哪一种，都不能绕过最大尝试次数和用户预算。

## 7. 重新解析、删除和版本一致性

V4 计划采用的保守策略（当前尚未实现）：

- `waiting_document` 可以继续解析；
- `queued/processing` 时拒绝重新解析正文；
- 活动 document/embedding job 存在时拒绝删除，返回稳定 `409`；
- 任务终态后允许删除并由数据库外键和文件删除流程收口。

完整 revision 支持后，可以放宽为：新 revision 使旧任务结果过期。Embedding 成功收尾事务必须再次检查
`documents.current_revision = embedding_jobs.document_revision`，不相等时不能替换当前向量。

PDF annotation 不修改文档正文 revision；如果未来 annotation 进入 RAG 证据，再单独定义 annotation revision 和
重建策略，不能暗中改变现有 embeddings。

## 8. 高并发保护与公平性

HTTP goroutine 数量不能代表系统容量。需要分别限制：

- 单用户和全局上传并发；
- 单用户活动解析/向量任务数量；
- PDF Python 子进程数量；
- Embedding 和 Generation 供应商并发、RPM、TPM；
- 批量 document ID 数量；
- PostgreSQL 连接池和连接获取等待时间。

达到用户预算返回 `429 + Retry-After`；系统资源暂时饱和返回 `503`。不要把新任务无限写入数据库后才依赖 Worker 慢慢
消化。全局 FIFO 还需要防止一个用户占满队列，第一版至少在入队时限制每用户活动数量，后续再按指标设计公平调度。

建议压测场景：重复批量提交、解析完成/申请同时发生、取消/领取同时发生、两个 Worker 并行、单用户洪峰、两个用户
公平性、供应商 429/500、进程崩溃恢复和 30～60 分钟稳定性。默认测试使用本地 Fake Provider，避免真实费用。

## 9. Worker 多实例前必须引入租约

当前 `SKIP LOCKED` 只保证领取瞬间不重复，不能证明长任务执行期间 Worker 仍存活。现有启动恢复带有单实例假设；
不能直接把同一 server 复制多份。

多实例阶段领取时写入：

```text
worker_id
lease_expires_at
heartbeat_at（可选）
```

恢复程序只处理 `status=processing AND lease_expires_at < now()` 的任务。Worker 长任务按约定续租；最终写回同时核对
job 状态、worker_id、lease 和 document revision。API 与 Worker 可以拆成两个命令/部署角色，但仍复用同一 Domain、
Application 和 PostgreSQL，不等于提前拆微服务。

本地文件只适合单机或共享卷。多主机 Worker 前要迁移到对象存储，Worker 下载到受控临时目录后再把可信绝对路径交给
Python，不能让 Python 接收客户端文件路径。

## 10. PDF/Markdown 内容接口后端边界

### 10.1 读取

计划中的 `GET /documents/:id/content` 必须先完成 Session 和 OwnerScope 校验，再打开可信 storage path。PDF 支持
`Range`、`206`、`Content-Range`、`Accept-Ranges`、正确 MIME 与 `Content-Disposition: inline`；ETag 使用内容哈希
或 revision，缓存只能是 `private`，高敏感配置可以 `no-store`。

### 10.2 保存 Markdown

计划中的保存接口接收 `base_revision` 或 `If-Match`。Repository 使用乐观更新和事务创建新 revision；版本不匹配返回
稳定 `409` 或 `412`，实现前由共享 API 契约二选一。服务端不能接受客户端直接提交 chunks 或 embedding 状态。

正式保存成功后再安排 chunks/embedding 失效或新任务。浏览器 IndexedDB 草稿不参与服务端数据真相。

### 10.3 PDF annotation

annotation 单独按 document、page、annotation version 和 owner 保护。它不直接重写 PDF 文件，不使用 Redis 作为唯一
存储，也不默认触发重新解析。

## 11. Redis 的端口与失败语义

Redis 后置实现必须通过最小端口接入，不能创建万能 `Cache`：

```go
type RateLimiter interface { /* Allow */ }
type SessionCache interface { /* Get/Set/Delete by token hash */ }
type JobProgressPublisher interface { /* Publish lightweight event */ }
```

实施顺序与降级：

| 能力 | Redis 角色 | Redis 不可用时 |
| --- | --- | --- |
| 多实例限流 | 原子共享窗口/计数 | 回退进程内应急限流，记录降级指标 |
| Session 缓存 | Cache-Aside 加速 | 回源 PostgreSQL；绝不跳过鉴权 |
| 任务进度 | Pub/Sub 即时提示 | 丢弃提示，前端轮询 PostgreSQL |

Session key 使用 token hash，不使用原始 Cookie；退出、密码重置和用户禁用必须先保证 PostgreSQL 真相正确，再删除相关
缓存。Pub/Sub 事件不携带文档正文、Prompt、Cookie 或 Token。Redis 不承担 PDF/MD 正式内容、权限、任务终态和计费
账本的唯一存储。

## 12. 后端必测矩阵

1. 同一用户并发创建单个和批量任务，最终只有一条活动任务；
2. 两个用户提交同一个 document ID，非 owner 不获得任务存在性信息；
3. 解析成功与申请同时发生，无 stranded `waiting_document`；
4. waiting 解析失败后保留意图，重试解析成功后只激活一次；
5. 取消先于领取、领取先于取消两个确定性测试；
6. 两个及以上 Worker 通过 `SKIP LOCKED` 各领不同任务；
7. 修改/重新解析与旧向量写回竞态不会覆盖新 revision；
8. 活动任务期间删除和重新解析按冻结的 409 契约执行；
9. 批量上限、重复 ID、部分无效、部分越权和逐项结果；
10. 单用户活动配额和全局背压返回稳定 `429/503`；
11. 租约过期可恢复，未过期任务不会被其他实例重置；
12. Redis 故障下限流降级、Session 回源和进度轮询保持正确；
13. PDF Range/ETag/OwnerScope 与 Markdown revision 冲突；
14. 默认测试不调用真实 Embedding、Generation、邮件、短信或外部对象存储。

## 13. 前端交付前的后端输出

每个能力交付时附带：

- 更新后的共享 API 契约、请求/响应示例和稳定错误 code；
- 数据库迁移、状态转换表和回滚说明；
- Handler/Application/Repository 并发测试；
- 双用户 OwnerScope 集成测试；
- Fake Provider 下的批量与多 Worker 压测基线；
- 当前不支持的取消、revision、Range 或 Redis 行为清单；
- 一组不含真实密码、Cookie、文档正文和远程费用的联调证据。
