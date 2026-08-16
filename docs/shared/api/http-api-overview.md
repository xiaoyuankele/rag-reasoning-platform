# HTTP API 总览

> 更新时间：2026-08-16。本文件是当前前后端协作的人工可读契约总览；具体字段以 Go Handler、
> Handler 测试和后续 OpenAPI 文件为最终校验依据。

## 1. 当前访问边界

当前 API 是个人版、单工作区接口，没有注册、登录、Session/JWT、成员权限或多租户隔离。所有能访问
服务端口的调用者都能操作同一组文档、任务、chunks、向量和问答能力。因此开发环境应只监听受信地址，
不能把当前服务直接暴露为公开互联网多人服务。

P6 个人用户域已经完成设计，B1 身份数据模型和迁移已经编码，但认证 HTTP 路由与用户隔离尚未实现。后续调用者
身份必须来自后端验证的 Session，不能依赖前端传入 `user_id`；文档、任务、检索和问答的数据范围必须由后端
`owner_user_id` 约束。团队工作区留到 P7。

## 2. 当前接口

| 方法 | 路径 | 主要输入 | 成功状态 | 用途 | 前端定位 |
|---|---|---|---|---|---|
| `GET` | `/health` | 无 | `200` | 检查后端是否存活 | 系统状态/开发验收 |
| `POST` | `/documents` | `multipart/form-data` 的 `file` | `201` | 上传文档 | 用户功能 |
| `GET` | `/documents` | `page`、`page_size` | `200` | 分页获取文档列表 | 用户功能 |
| `GET` | `/documents/:id` | 路径参数 `id` | `200` | 获取文档详情 | 用户功能 |
| `GET` | `/documents/:id/chunks` | 路径参数 `id`，可选 `page`、`page_size` | `200` | 按原文顺序分页查看 ready 文档的文本块 | 用户功能/解析质量检查 |
| `DELETE` | `/documents/:id` | 路径参数 `id` | `204` | 删除文档及其关联数据 | 用户功能，需二次确认 |
| `POST` | `/documents/:id/process` | 路径参数 `id` | `202` | 创建异步解析任务 | 用户功能 |
| `GET` | `/processing-jobs/:id` | 路径参数 `id` | `200` | 查询解析任务状态 | 用户功能/轮询 |
| `GET` | `/search` | `q`、可选 `document_id`、`page`、`page_size` | `200` | 关键词检索文本块 | 用户功能 |
| `POST` | `/documents/:id/embeddings` | 路径参数 `id` | `202` | 手动创建文档向量任务 | 运维/开发功能，首版 UI 不直接暴露 |
| `GET` | `/embedding-jobs/:id` | 路径参数 `id` | `200` | 查询向量任务状态、重试和 Token 信息 | 用户功能/轮询 |
| `POST` | `/semantic-search` | JSON：`query`、可选 `document_id`、`top_k` | `200` | 语义检索 | 用户功能，受功能开关控制 |
| `POST` | `/answers` | JSON：`query`、可选 `document_id`、`top_k`、`response_language` | `200` | 基于来源生成回答 | 用户功能，受功能开关控制 |

## 3. P6 计划认证接口（尚未实现）

以下契约已经冻结用于前后端开发交接，但在对应 Handler 和测试合入前不能当作当前可调用接口：

| 方法 | 路径 | 主要输入 | 成功状态 | Cookie 行为 |
| --- | --- | --- | --- | --- |
| `POST` | `/auth/verification-codes` | JSON：`channel`、`destination`、`purpose` | `202` | 不创建 Session |
| `POST` | `/auth/register` | JSON：`verification_id`、`verification_code`、`display_name`、`password` | `201` | 创建 Session 并设置 `rag_session` |
| `POST` | `/auth/login` | JSON：`identifier`、`password` | `200` | 创建 Session 并设置 `rag_session` |
| `POST` | `/auth/logout` | 当前 Session Cookie | `204` | 撤销 Session 并清除 Cookie |
| `GET` | `/users/me` | 当前 Session Cookie | `200` | 不修改 Cookie |

P6 路由保护边界：

- `GET /health`、验证码发送、注册和登录无需 Session，但必须受 Origin 与限流保护；
- `POST /auth/logout` 可选读取并撤销 Session，始终清除 Cookie、返回 `204`，但仍须通过同源 Origin 校验；
- `GET /users/me` 和第 2 节全部业务接口受保护；
- 业务请求 DTO 不增加客户端可填写的 `user_id`；
- Session 缺失、过期或撤销统一返回 `401`、`authentication_required`；
- 资源不存在或不属于当前用户统一返回 `404`，避免 ID 枚举；
- 登录失败统一返回 `401`、`invalid_credentials`，不区分邮箱/手机号不存在、密码错误或账户不可登录；
- Cookie 使用 `HttpOnly`、`SameSite=Lax`、`Path=/`，生产环境启用 `Secure`；
- 改变状态的 Cookie 请求必须通过同源 `Origin` 校验。

完整响应 DTO、错误码和主线图见
[P6 个人用户域与私有数据闭环](../architecture/personal-user-domain.md)；后端实施细节见
[P6 个人用户域后端交接](../../backend/architecture/personal-user-backend-handoff.md)。

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

当请求字段、响应 DTO、状态码或错误语义变化时：

1. 后端先更新 Handler 测试和实现；
2. 同步更新本文件；
3. 前端更新 API 类型、转换层和界面状态；
4. 用真实 HTTP 请求做一次联调验收；
5. 若属于破坏性变更，必须先讨论版本兼容方案。
