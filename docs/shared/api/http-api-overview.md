# HTTP API 总览

> 更新时间：2026-08-15。本文件是当前前后端协作的人工可读契约总览；具体字段以 Go Handler、
> Handler 测试和后续 OpenAPI 文件为最终校验依据。

## 1. 当前访问边界

当前 API 是个人版、单工作区接口，没有注册、登录、Session/JWT、成员权限或多租户隔离。所有能访问
服务端口的调用者都能操作同一组文档、任务、chunks、向量和问答能力。因此开发环境应只监听受信地址，
不能把当前服务直接暴露为公开互联网多人服务。

未来 P6 引入认证和工作区后，调用者身份必须来自后端验证的认证上下文，不能依赖前端传入 `user_id`；
文档、任务、检索和问答的数据范围也必须由后端工作区权限约束。

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

## 3. 参数来源

- `:id` 是路径参数，由 Gin 的 `Context.Param` 读取；
- `GET` 检索和分页字段是查询参数，由 `Context.Query` 或 `Context.GetQuery` 读取；
- 上传文件来自 `multipart/form-data`；
- 语义检索和回答请求来自 JSON 请求体，由 `Context.ShouldBindJSON` 绑定。

## 4. 通用响应约定

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

## 5. 变更流程

当请求字段、响应 DTO、状态码或错误语义变化时：

1. 后端先更新 Handler 测试和实现；
2. 同步更新本文件；
3. 前端更新 API 类型、转换层和界面状态；
4. 用真实 HTTP 请求做一次联调验收；
5. 若属于破坏性变更，必须先讨论版本兼容方案。
