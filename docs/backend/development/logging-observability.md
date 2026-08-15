# 日志与请求追踪规范

## 1. 当前目标

P5.2 的目标不是“多打印几行文字”，而是让开发者能够回答：

1. 哪一次请求出了问题；
2. 请求经过哪个接口、返回什么状态、耗时多久；
3. 前端看到的安全错误如何与后端内部诊断关联；
4. 后台任务和远程模型调用将来如何沿用同一套字段规则。

P5.2.1 先完成 HTTP 请求 ID 与结构化访问日志。错误分类、后台任务事件和模型成本指标后续分步实现。

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

## 8. 后续计划

1. 定义用户安全错误、内部诊断错误和供应商错误的映射规范；
2. 为关键 Handler 内部错误记录 `request_id` 和稳定 `error_code`；
3. 为解析、Embedding 和问答记录任务 ID、文档 ID、阶段、耗时和结果；
4. 增加日志级别与输出格式配置；
5. 建立远程模型延迟、Token、重试和失败分类基线。
