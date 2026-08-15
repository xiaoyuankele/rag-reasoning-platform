# 日志与请求追踪规范

## 1. 当前目标

P5.2 的目标不是“多打印几行文字”，而是让开发者能够回答：

1. 哪一次请求出了问题；
2. 请求经过哪个接口、返回什么状态、耗时多久；
3. 前端看到的安全错误如何与后端内部诊断关联；
4. 后台任务和远程模型调用将来如何沿用同一套字段规则。

P5.2.1 已完成 HTTP 请求 ID 与结构化访问日志。P5.2.2 第一版继续建立用户安全错误与内部诊断错误的双通道；
后台任务事件、外部供应商错误分类和模型成本指标后续分步实现。

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

访问日志使用 Go `log/slog` 的 JSON Handler，固定事件名为 `http_request_completed`。

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

所有任务事件包含 `processing_job_id`、`document_id`、`attempt_count` 和 `status`；终结事件还包含
`duration_ms`，失败事件在后端日志保留原始 `error`。日志不记录正文、存储路径、密钥或上传内容。

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

## 8. 后续计划

1. 按实际联调需要把统一错误响应逐步迁移到其余 Handler；
2. 为 Generation/问答调用建立延迟、Token 和供应商错误分类；
3. 将 Embedding 与 Generation 指标整理成可重复执行的成本基线；
4. 增加日志级别与输出格式配置；
5. 继续部署、备份和恢复流程。
