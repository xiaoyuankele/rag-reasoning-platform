# HTTP API 总览

> 更新时间：2026-08-22。本文件是当前前后端协作的人工可读契约总览；具体字段以 Go Handler、
> Handler 测试和后续 OpenAPI 文件为最终校验依据。

## 1. 当前访问边界

当前 API 是个人版、单工作区接口。验证码、注册、登录、密码重置、Session、当前用户和退出已经实现；文档增删查、
解析任务创建、processing job 查询、chunks 浏览、向量任务申请/批量/查询/取消、关键词检索、语义检索和问答均已接入
Session 保护与 `owner_user_id` SQL 隔离。历史无归属数据已经完成显式认领，数据库也已通过 `NOT NULL`
禁止再次产生无主文档；后端双用户数据隔离发布验收已经通过。公开互联网部署仍需配套 HTTPS、真实邮件渠道、
反向代理与生产环境安全配置。

身份只来自后端验证的 Session，不能依赖前端传入 `user_id`。团队工作区和成员权限属于 P7。

## 2. 当前接口

| 方法 | 路径 | 主要输入 | 成功状态 | 用途 | 前端定位 |
|---|---|---|---|---|---|
| `GET` | `/health` | 无 | `200` | 检查后端是否存活 | 系统状态/开发验收 |
| `POST` | `/documents` | Session Cookie；`multipart/form-data` 的 `file` | 新建 `201`；同用户重复 `200` | 上传、绑定当前用户并按内容去重 | 用户功能；已隔离 |
| `POST` | `/documents/preflight` | Session Cookie；JSON：`sha256`、`size_bytes` | `200` | 上传文件正文前检查当前用户是否已有相同二进制内容 | 用户功能；已隔离/性能优化 |
| `GET` | `/documents` | Session Cookie；`page`、`page_size` | `200` | 分页获取当前用户文档 | 用户功能；已隔离 |
| `GET` | `/documents/:id` | Session Cookie；路径参数 `id` | `200` | 获取当前用户文档详情 | 用户功能；已隔离 |
| `GET` | `/documents/:id/chunks` | Session Cookie；路径参数 `id`，可选 `page`、`page_size` | `200` | 查看当前用户 ready 文档的文本块 | 用户功能；已隔离 |
| `DELETE` | `/documents/:id` | Session Cookie；路径参数 `id` | `204` | 删除当前用户文档及其关联数据 | 用户功能；已隔离，需二次确认 |
| `POST` | `/documents/:id/process` | Session Cookie；路径参数 `id` | `202` | 为当前用户文档创建异步解析任务 | 用户功能；已隔离 |
| `GET` | `/processing-jobs/:id` | Session Cookie；路径参数 `id` | `200` | 查询当前用户文档的解析任务状态 | 用户功能；已隔离/轮询 |
| `GET` | `/search` | Session Cookie；完整短语 `q`，或重复 `term` + 可选 `operator`、`within`；另可选 `document_id`、`page`、`page_size` | `200` | 在当前用户文档的同一文本块内执行短语或多关键词检索 | 用户功能；已隔离 |
| `POST` | `/documents/:id/embeddings` | Session Cookie；路径参数 `id` | 新建 `202`；活动任务已存在 `200`；用户满额 `429`；全局满额 `503` | 保存当前用户的向量化意图 | 用户功能；已隔离/幂等/背压 |
| `POST` | `/embedding-jobs/batch` | Session Cookie；JSON：`document_ids`，最多 100 项 | `200` | 对多份文档逐项创建或复用向量任务 | 用户功能；已隔离/逐项结果/背压 |
| `POST` | `/embedding-jobs/latest` | Session Cookie；JSON：`document_ids`，最多 100 项 | `200` | 按文档批量发现当前用户可见的最新向量任务 | 用户功能；已隔离/状态恢复 |
| `GET` | `/embedding-jobs/:id` | Session Cookie；路径参数 `id` | `200` | 查询当前用户文档的向量任务状态、重试和 Token 信息 | 用户功能；已隔离/轮询 |
| `POST` | `/embedding-jobs/:id/cancel` | Session Cookie；路径参数 `id` | `200` | 取消 waiting/queued 向量任务 | 用户功能；processing/终态返回 `409` |
| `POST` | `/semantic-search` | Session Cookie；JSON：`query`、可选 `document_id`、`top_k` | `200`；远程 Embedding 容量等待超时 `503` | 在当前用户文档中进行语义检索 | 用户功能；已隔离，受功能开关和共享闸门控制 |
| `POST` | `/answers` | Session Cookie；JSON：`query`、可选 `document_id`、`top_k`、`response_language` | `200`；问答或远程 Embedding 容量等待超时 `503` | 基于当前用户来源生成回答 | 用户功能；已隔离，受功能开关和并发闸门控制 |
| `POST` | `/auth/verification-codes` | JSON：`channel`、`destination`、`purpose` | `202` | 申请注册或密码重置验证码挑战 | 认证功能；`purpose` 为 `register` 或 `password_reset` |
| `POST` | `/auth/register` | JSON：`verification_id`、`verification_code`、`display_name`、`password` | `201` | 创建用户和 Session | 认证功能；设置 `rag_session` Cookie |
| `POST` | `/auth/login` | JSON：`identifier`、`password` | `200` | 核对凭据并创建新 Session | 认证功能；设置 `rag_session` Cookie |
| `POST` | `/auth/password-reset` | JSON：`verification_id`、`verification_code`、`new_password` | `204` | 更新密码并撤销全部旧 Session | 认证功能；清除当前 `rag_session` Cookie |
| `POST` | `/auth/logout` | `rag_session` Cookie（可选） | `204` | 幂等撤销 Session 并清除 Cookie | 认证功能 |
| `GET` | `/users/me` | `rag_session` Cookie | `200` | 恢复当前用户公开资料 | 认证功能；已受 Session 中间件保护 |

### 2.1 上传前重复文件预检正式契约

`POST /documents/preflight` 是受 Session 保护的只读预检接口。它只减少重复文件的上传流量和服务端读取成本，
不能替代 `POST /documents` 的可信哈希计算、文件类型校验和数据库唯一约束。

请求：

```http
POST /documents/preflight
Content-Type: application/json
Cookie: rag_session=...
```

```json
{
  "sha256": "d5db70fbccdd8ccc6a553604b79a09cd33083b401340d546efa08a52142c972e",
  "size_bytes": 14
}
```

- `sha256` 必须是恰好 64 位的小写十六进制 SHA-256；
- `size_bytes` 必须是正整数，且不能超过后端当前文件大小上限；
- `original_name` 不属于预检契约，因为文件名不参与内容去重；
- 身份只来自 Session，禁止发送 `user_id`。

当前用户没有相同内容时返回 `200`：

```json
{
  "exists": false,
  "document": null
}
```

当前用户已有同时匹配 `sha256` 和 `size_bytes` 的文档时返回 `200`：

```json
{
  "exists": true,
  "document": {
    "id": 3,
    "title": null,
    "original_name": "first-upload.pdf",
    "mime_type": "application/pdf",
    "size_bytes": 14,
    "sha256": "d5db70fbccdd8ccc6a553604b79a09cd33083b401340d546efa08a52142c972e",
    "status": "ready",
    "error_message": null,
    "created_at": "2026-08-20T16:00:00+08:00",
    "updated_at": "2026-08-20T16:01:00+08:00"
  }
}
```

正式错误契约：

| 状态码 | 稳定 `code` | 含义 | 前端行为 |
| --- | --- | --- | --- |
| `400` | `invalid_document_preflight` | JSON、SHA-256 或文件大小格式不合法 | 阻止上传并修正本地输入，不降级 |
| `401` | `authentication_required` | Session 缺失、过期或已撤销 | 进入重新认证流程，不降级 |
| `413` | `file_too_large` | 文件超过当前上传上限 | 阻止上传，不降级 |
| `500` | `internal_error` | 后端查询异常 | 可以降级到原 `POST /documents`，同时保留请求 ID |

网络错误、超时或 `5xx` 时允许前端“失效开放”：继续调用原上传接口。这样只会失去预检带来的性能优化，
不会破坏正确性，因为后端仍会根据真实文件字节重新计算 SHA-256。`400`、`401` 和 `413` 属于确定性拒绝，
不得绕过预检继续上传。

预检结果不是上传预约，也不锁定文档。两个标签页可能同时得到 `exists:false`；随后并发上传时，
`POST /documents` 仍依靠 `(owner_user_id, sha256)` 唯一约束确保同一用户只保留一份内容。
不同用户之间不会通过预检互相看到文档。第一版直接查询 PostgreSQL，不引入 Redis。

### 2.2 关键词检索模式

`GET /search` 保留原有完整短语模式：

```http
GET /search?q=磁悬浮振动&page=1&page_size=20
```

同一文本块多关键词模式使用重复 `term` 参数：

```http
GET /search?term=磁悬浮&term=振动&operator=all&within=chunk&page=1&page_size=20
```

- 规范化后必须包含 2～8 个不同关键词；单项最多 100 个 Unicode 字符，合计最多 200 个字符；
- `operator` 为 `all` 或 `any`，省略时默认 `all`；
- 第一版 `within` 只接受 `chunk`，省略时也默认为 `chunk`；
- `q` 与 `term` 不能同时提供；
- `all` 要求同一条 `text_chunks` 记录包含全部词，`any` 要求至少包含一个词；
- 响应在原 `query` 和分页字段之外，为多词请求返回规范化 `terms`、`operator`、`within`；
- `document_id`、OwnerScope、`ready` 状态和分页规则继续沿用原接口。

当前 chunk 不等于自然句或自然段。`within=sentence/paragraph` 尚未实现，不能由前端在分页结果上二次过滤冒充。

### 2.3 按文档批量发现最新向量任务

`POST /embedding-jobs/latest` 用于页面首次进入、刷新或换设备后，从已知文档 ID 恢复服务端真实任务状态：

```json
{
  "document_ids": [600, 595, 593]
}
```

成功统一返回 `200`，去重后仍按第一次出现的文档 ID 顺序逐项响应：

```json
{
  "items": [
    {"document_id": 600, "job": {"id": 931, "document_id": 600, "status": "succeeded"}},
    {"document_id": 595, "job": {"id": 932, "document_id": 595, "status": "processing"}},
    {"document_id": 593, "job": null}
  ]
}
```

- 请求必须包含 1～100 个正整数文档 ID；格式或边界错误返回 `400`、`invalid_embedding_job_lookup`；
- 查询始终使用 Session OwnerScope。文档不存在、属于其他用户或从未创建任务都返回 `job: null`，避免泄露资源存在性；
- “最新”第一版按 `embedding_jobs.id DESC` 定义；接口返回的是调用时刻快照，不是锁；
- 页面初始化时调用一次，随后只对 `waiting_document/queued/processing` 任务复用 `GET /embedding-jobs/:id` 有界轮询；
- 最新任务 `succeeded` 目前只表示最近任务成功，不等同于已经证明其向量匹配当前文档 revision。版本和 stale 语义后续单独设计。

### 2.4 向量任务入队背压

活动任务定义为 `waiting_document`、`queued` 或 `processing`。默认每个用户最多同时保留 100 条活动任务，
整个系统最多 500 条；部署时可以通过 `EMBEDDING_MAX_ACTIVE_JOBS_PER_USER` 和
`EMBEDDING_MAX_ACTIVE_JOBS_GLOBAL` 调整，但全局值不能小于单用户值。

`POST /documents/:id/embeddings` 在创建新任务前原子检查容量：

| 状态码 | 稳定 `code` | 含义 | 建议行为 |
| --- | --- | --- | --- |
| `429` | `embedding_owner_active_job_limit` | 当前用户活动任务达到上限 | 按 `Retry-After` 等待并刷新已有任务 |
| `503` | `embedding_queue_capacity_exhausted` | 系统全局活动任务达到上限 | 按 `Retry-After` 等待，避免立即重试洪峰 |

两种响应都包含 `Retry-After: 5`。同一文档已经有活动任务时仍然幂等返回原任务，不占用新名额，也不会因为
当前队列已满而把正常的状态查询误报为限流。

批量接口继续返回 `200` 和逐项结果：用户满额项使用 `outcome=rate_limited`，全局满额项使用
`outcome=capacity_exhausted`，对应 `error.code` 与单任务接口一致。已经创建或复用的其他项不会回滚。
容量检查和任务插入位于同一 PostgreSQL 事务，并由事务级 advisory lock 串行化最后一个名额的竞争；
Handler 中的预检查不能替代这一数据库原子边界。

### 2.5 远程 Embedding 分类隔离与全局闸门

后台向量 Worker 与在线语义能力先经过独立分类闸门，再经过进程内全局闸门：Worker 默认最多占用 2 个槽位，
`POST /semantic-search` 和 `POST /answers` 内部语义检索共享另外 2 个在线槽位，所有入口合计默认不超过 4。
配置必须满足 `EMBEDDING_WORKER_PROVIDER_CONCURRENCY + EMBEDDING_ONLINE_PROVIDER_CONCURRENCY <=
EMBEDDING_PROVIDER_MAX_CONCURRENCY`。该设计保证后台任务即使持续堆积，也不能占用在线预留容量。

这些配置限制同时在途的 Embedding HTTP 调用，而不是任务数、每分钟请求数或 Token 额度。后台 Worker 没有本地
等待超时，只随任务 context 取消；在线请求默认最多等待 `EMBEDDING_ONLINE_QUEUE_WAIT_TIMEOUT=2s`。

在线等待超时统一返回：

```http
HTTP/1.1 503 Service Unavailable
Retry-After: 2
Content-Type: application/json

{"error":"embedding service is busy; try again later","code":"embedding_provider_capacity_exhausted"}
```

这是可重试的本地容量错误，不等同于远程服务自己的限流、额度不足或不可用。前端应按 `Retry-After` 退避，
不能立即循环重试。当前分类与全局闸门都只约束单个后端进程；多副本部署后的全局容量需要单独引入分布式准入机制。

## 3. P6 认证接口状态

以下契约已经冻结用于前后端开发交接；只有标记“已实现”的路由可以当作当前可调用接口：

| 方法 | 路径 | 主要输入 | 成功状态 | Cookie 行为 | 状态 |
| --- | --- | --- | --- | --- | --- |
| `POST` | `/auth/verification-codes` | JSON：`channel`、`destination`、`purpose` | `202` | 不创建 Session | 已实现 |
| `POST` | `/auth/register` | JSON：`verification_id`、`verification_code`、`display_name`、`password` | `201` | 创建 Session 并设置 `rag_session` | 已实现 |
| `POST` | `/auth/login` | JSON：`identifier`、`password` | `200` | 创建 Session 并设置 `rag_session` | 已实现 |
| `POST` | `/auth/password-reset` | JSON：`verification_id`、`verification_code`、`new_password` | `204` | 撤销全部旧 Session 并清除 `rag_session` | 已实现 |
| `POST` | `/auth/logout` | 当前 Session Cookie | `204` | 撤销 Session 并清除 Cookie | 已实现 |
| `GET` | `/users/me` | 当前 Session Cookie | `200` | 不修改 Cookie | 已实现 |

当前验证码接口已经实现联系方式 60 秒冷却、远端 IP 限流和进程全局预算，并按 `register`、
`password_reset` 隔离用途。注册接口会原子创建用户与
PostgreSQL Session，并设置 HttpOnly Cookie；登录接口会核对 Argon2id 并创建独立 Session。默认 Fake Sender 不访问远程渠道。
密码重置接口会原子更新密码、消费挑战并撤销全部旧 Session，成功后要求重新登录。
`/users/me` 已通过 Session 中间件消费该 Cookie，退出后旧 Cookie 统一失效。文档、解析任务、chunks、
向量任务、关键词检索、语义检索和问答接口也已完成 OwnerScope 隔离。P6 后端 B1～B7 已完成，后续进入
前端认证壳、受保护页面和个人用户产品闭环联调。

P6 路由保护边界：

- `GET /health`、验证码发送、注册、登录和密码重置无需 Session，但必须受 Origin 与限流保护；
- `POST /auth/logout` 可选读取并撤销 Session，始终清除 Cookie、返回 `204`，但仍须通过同源 Origin 校验；
- `GET /users/me`、文档、解析任务、chunks、向量任务、关键词检索、语义检索和问答接口均已受保护；
- 业务请求 DTO 不增加客户端可填写的 `user_id`；
- Session 缺失、过期或撤销统一返回 `401`、`authentication_required`；
- 资源不存在或不属于当前用户统一返回 `404`，避免 ID 枚举；
- 登录失败统一返回 `401`、`invalid_credentials`，不区分邮箱/手机号不存在、密码错误或账户不可登录；
- Cookie 使用 `HttpOnly`、`SameSite=Lax`、`Path=/`，生产环境启用 `Secure`；
- 改变状态的 Cookie 请求必须通过同源 `Origin` 校验。

完整响应 DTO、错误码和主线图见
[P6 个人用户域与私有数据闭环](../architecture/personal-user-domain.md)；后端实施细节见
[P6 个人用户域后端交接](../../backend/architecture/personal-user-backend-handoff.md)；前端状态机、表单流程和
验收清单见 [P6 个人用户认证前端交接](../../frontend/architecture/p6-authentication-handoff.md)。

## 4. 参数来源

- `:id` 是路径参数，由 Gin 的 `Context.Param` 读取；
- `GET` 检索和分页字段是查询参数，由 `Context.Query` 或 `Context.GetQuery` 读取；
- 上传文件来自 `multipart/form-data`；
- 语义检索和回答请求来自 JSON 请求体，由 `Context.ShouldBindJSON` 绑定。

## 5. 通用响应约定

- 所有经过后端路由的响应都包含 `X-Request-ID`；前端可以传入由字母、数字、点、下划线或连字符组成、
  最长 128 字符的同名请求头，也可以省略并由后端生成；
- 前端展示“请求失败”时可以同时保留响应中的 `X-Request-ID`，供后端在结构化日志中定位同一次请求；
- 成功响应使用稳定的 JSON 字段；`204 No Content` 没有响应体；
- `POST /documents` 使用文件内容的 SHA-256 在当前用户范围内查重。首次上传返回 `201` 和
  `duplicate: false`；同一用户再次上传完全相同的字节内容时返回原有文档的 `200` 响应和
  `duplicate: true`。此时 `id`、`original_name` 和状态均来自第一次上传，前端应提示“该内容已存在”
  并导航或刷新原记录，而不是当作失败；不同用户上传相同内容仍各自得到 `201` 和独立文档；
- 可预期的客户端输入错误返回 `4xx`，响应一般为 `{"error":"安全提示"}`；
- 内部错误详情只记录在后端，不直接暴露给前端；
- 前端必须依据 HTTP 状态码处理分支，不能依赖后端日志文本；
- `/documents/:id/chunks` 只允许读取 `ready` 文档；文档存在但仍处于 `uploaded`、`processing`
  或 `failed` 时返回 `409`，避免把旧 chunks 当成当前正式结果；
- `semantic-search` 和 `answers` 可能调用远程模型并产生费用，前端应提供加载态、超时提示和重试入口。
- `POST /answers` 的完整“问题向量化 → 检索 → 生成”链路受进程内并发闸门保护。并发槽位已满时，
  后端最多等待 `ANSWER_QUEUE_WAIT_TIMEOUT`；仍未获得槽位则返回 `503 Service Unavailable`、
  `Retry-After: 2` 和
  `{"error":"answer service is busy; try again later","code":"answer_capacity_exhausted"}`。
  前端应保留用户输入并按 `Retry-After` 提供稍后重试，不应在循环中立即重放请求。
- `semantic-search` 和 `answers` 提供 `document_id` 时，文档不存在返回 `404`；文档存在但状态、
  chunks 或当前模型向量尚未完整就绪时返回 `409` 和
  `{"error":"document embeddings are not ready"}`。这两种情况都不会调用远程模型。
- 只有确认向量完整后仍没有检索命中，才返回正常 `200` 空结果（问答接口返回安全降级答案）。

## 6. 变更流程

F2-B 当前接口已经可以支持页面内详情、解析轮询、chunks 和删除；刷新后按文档恢复 queued 任务、处理中删除语义和
部分稳定错误 code 仍需对齐，见 [F2-B 前后端待对齐事项](f2b-frontend-backend-alignment.md)。

用户可选向量化的第一批后端契约已经在提交 `c4696bc` 交付：

- `POST /documents/:id/embeddings`：首次创建返回 `202`；存在活动任务时幂等返回原任务和真实状态，状态码为 `200`；
- `POST /embedding-jobs/batch`：请求体为 `{"document_ids":[1,2]}`，上限 100 个 ID，去重后逐项返回
  `created`、`already_active`、`not_found` 或 `failed`；
- `GET /embedding-jobs/:id`：查询当前用户文档关联的向量任务；
- `POST /embedding-jobs/:id/cancel`：允许取消 `waiting_document` 和 `queued`；重复取消幂等返回 `200`；
  `processing`、`succeeded` 和 `failed` 返回 `409`；
- 正式向量状态包含 `waiting_document`、`queued`、`processing`、`succeeded`、`failed` 和 `canceled`。

PDF/Markdown 原内容读取、Markdown revision 保存、任务按 document 恢复、用户配额和 Redis 演进仍属于计划能力，
当前不能按候选路径调用。跨端决策见
[文档向量化、在线编辑、并发与缓存设计复盘](../architecture/document-vectorization-editing-concurrency-review.md)。

当请求字段、响应 DTO、状态码或错误语义变化时：

1. 后端先更新 Handler 测试和实现；
2. 同步更新本文件；
3. 前端更新 API 类型、转换层和界面状态；
4. 用真实 HTTP 请求做一次联调验收；
5. 若属于破坏性变更，必须先讨论版本兼容方案。
