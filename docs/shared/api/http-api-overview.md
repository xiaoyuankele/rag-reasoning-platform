# HTTP API 总览

> 更新时间：2026-08-18。本文件是当前前后端协作的人工可读契约总览；具体字段以 Go Handler、
> Handler 测试和后续 OpenAPI 文件为最终校验依据。

## 1. 当前访问边界

当前 API 是个人版、单工作区接口。验证码、注册、登录、密码重置、Session、当前用户和退出已经实现；文档增删查、
解析任务创建、processing job 查询、chunks 浏览、向量任务创建/查询、关键词检索、语义检索和问答均已接入
Session 保护与 `owner_user_id` SQL 隔离。历史无归属数据已经完成显式认领，数据库也已通过 `NOT NULL`
禁止再次产生无主文档；后端双用户数据隔离发布验收已经通过。公开互联网部署仍需配套 HTTPS、真实邮件渠道、
反向代理与生产环境安全配置。

身份只来自后端验证的 Session，不能依赖前端传入 `user_id`。团队工作区和成员权限属于 P7。

## 2. 当前接口

| 方法 | 路径 | 主要输入 | 成功状态 | 用途 | 前端定位 |
|---|---|---|---|---|---|
| `GET` | `/health` | 无 | `200` | 检查后端是否存活 | 系统状态/开发验收 |
| `POST` | `/documents` | Session Cookie；`multipart/form-data` 的 `file` | 新建 `201`；同用户重复 `200` | 上传、绑定当前用户并按内容去重 | 用户功能；已隔离 |
| `GET` | `/documents` | Session Cookie；`page`、`page_size` | `200` | 分页获取当前用户文档 | 用户功能；已隔离 |
| `GET` | `/documents/:id` | Session Cookie；路径参数 `id` | `200` | 获取当前用户文档详情 | 用户功能；已隔离 |
| `GET` | `/documents/:id/chunks` | Session Cookie；路径参数 `id`，可选 `page`、`page_size` | `200` | 查看当前用户 ready 文档的文本块 | 用户功能；已隔离 |
| `DELETE` | `/documents/:id` | Session Cookie；路径参数 `id` | `204` | 删除当前用户文档及其关联数据 | 用户功能；已隔离，需二次确认 |
| `POST` | `/documents/:id/process` | Session Cookie；路径参数 `id` | `202` | 为当前用户文档创建异步解析任务 | 用户功能；已隔离 |
| `GET` | `/processing-jobs/:id` | Session Cookie；路径参数 `id` | `200` | 查询当前用户文档的解析任务状态 | 用户功能；已隔离/轮询 |
| `GET` | `/search` | Session Cookie；`q`、可选 `document_id`、`page`、`page_size` | `200` | 检索当前用户文档的文本块 | 用户功能；已隔离 |
| `POST` | `/documents/:id/embeddings` | Session Cookie；路径参数 `id` | `202` | 为当前用户文档手动创建向量任务 | 已隔离；首版 UI 不直接暴露 |
| `GET` | `/embedding-jobs/:id` | Session Cookie；路径参数 `id` | `200` | 查询当前用户文档的向量任务状态、重试和 Token 信息 | 用户功能；已隔离/轮询 |
| `POST` | `/semantic-search` | Session Cookie；JSON：`query`、可选 `document_id`、`top_k` | `200` | 在当前用户文档中进行语义检索 | 用户功能；已隔离，受功能开关控制 |
| `POST` | `/answers` | Session Cookie；JSON：`query`、可选 `document_id`、`top_k`、`response_language` | `200` | 基于当前用户来源生成回答 | 用户功能；已隔离，受功能开关控制 |
| `POST` | `/auth/verification-codes` | JSON：`channel`、`destination`、`purpose` | `202` | 申请注册或密码重置验证码挑战 | 认证功能；`purpose` 为 `register` 或 `password_reset` |
| `POST` | `/auth/register` | JSON：`verification_id`、`verification_code`、`display_name`、`password` | `201` | 创建用户和 Session | 认证功能；设置 `rag_session` Cookie |
| `POST` | `/auth/login` | JSON：`identifier`、`password` | `200` | 核对凭据并创建新 Session | 认证功能；设置 `rag_session` Cookie |
| `POST` | `/auth/password-reset` | JSON：`verification_id`、`verification_code`、`new_password` | `204` | 更新密码并撤销全部旧 Session | 认证功能；清除当前 `rag_session` Cookie |
| `POST` | `/auth/logout` | `rag_session` Cookie（可选） | `204` | 幂等撤销 Session 并清除 Cookie | 认证功能 |
| `GET` | `/users/me` | `rag_session` Cookie | `200` | 恢复当前用户公开资料 | 认证功能；已受 Session 中间件保护 |

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
- `semantic-search` 和 `answers` 提供 `document_id` 时，文档不存在返回 `404`；文档存在但状态、
  chunks 或当前模型向量尚未完整就绪时返回 `409` 和
  `{"error":"document embeddings are not ready"}`。这两种情况都不会调用远程模型。
- 只有确认向量完整后仍没有检索命中，才返回正常 `200` 空结果（问答接口返回安全降级答案）。

## 6. 变更流程

F2-B 当前接口已经可以支持页面内详情、解析轮询、chunks 和删除；刷新后按文档恢复 queued 任务、处理中删除语义和
部分稳定错误 code 仍需对齐，见 [F2-B 前后端待对齐事项](f2b-frontend-backend-alignment.md)。

当请求字段、响应 DTO、状态码或错误语义变化时：

1. 后端先更新 Handler 测试和实现；
2. 同步更新本文件；
3. 前端更新 API 类型、转换层和界面状态；
4. 用真实 HTTP 请求做一次联调验收；
5. 若属于破坏性变更，必须先讨论版本兼容方案。
