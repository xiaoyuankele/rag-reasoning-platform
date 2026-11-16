# Agent v0 HTTP Contract

> 状态：2026-11-16 冻结目标契约。B2-B0 先补齐服务端一致性边界，B2-B 再实现 HTTP；
> Frontend F0 只依赖本文公开语义。
>
> Agent 默认关闭，采用 polling；SSE、多 Agent、写工具、浏览器工具与长期人格记忆不属于 v0。

## 1. 安全与职责边界

- 所有路由位于 Session 保护组，身份只来自后端 `OwnerScope`；浏览器不得提交 `user_id`、工具名、权限、Worker 参数或执行预算。
- 同一 conversation 同时最多一个活动 Answer Job 或 Agent Run。Agent 创建冲突返回 `409 conversation_execution_active`；既有 Answer 创建及 conversation archive/delete 为保持兼容，继续返回 `409 conversation_answer_active`，即使阻塞方是 Agent。客户端把两个 code 都视为“存在活动执行”，随后同时刷新 Answer active 与 Agent active，不从错误消息猜测活动类型。若两类 active 都返回 `204`，表示执行在竞态窗口内已终止：客户端再刷新 messages、Answer latest 与 Agent latest 一次，禁止自动重放创建请求。
- Agent Run、Step、最终消息和来源以 PostgreSQL 为事实源；轮询和页面状态不能替代服务端状态。
- 所有包含 `agent_run` 的成功响应——create `202/200`、get `200`、active/latest `200`、cancel `200`——必须使用统一的 Owner-scoped `RunSnapshot`，在一个 PostgreSQL 一致性边界内同时读取 Run 与有序 Steps；不得由 Handler 先查 Run、再查 Steps 后拼接可能撕裂的快照。create 的消息与 Run 关联也必须由同一创建/重放事务验证；cancel 的快照必须反映该取消事务提交的终态。
- DTO 不暴露 `owner_user_id`、attempt、Worker、lease/fencing、deadline、Prompt、工具 arguments/result、Provider ID、`reasoning_content` 或原始响应。
- v0 仅允许 `search_documents`、`read_document_evidence`；Step DTO 只公开工具名和安全状态，不公开原始载荷。

## 2. 路由

```text
POST /conversations/:id/agent-runs
GET  /agent-runs/:id
GET  /conversations/:id/agent-runs/active
GET  /conversations/:id/agent-runs/latest
POST /agent-runs/:id/cancel
```

当 `AGENT_ENABLED=false` 时不注册这些路由。此时返回通用路由 `404`，不得伪装成 `agent_run_not_found`；前端据此隐藏 Agent 模式，但不影响消息与 Answer Job。

## 3. 创建 Agent Run

请求：

```json
{
  "content": "比较两份文档的恢复策略。",
  "client_run_id": "run-018f54cc",
  "document_id": 123,
  "response_language": "auto"
}
```

字段：

- `content`：必填、有效 UTF-8，trim 后非空；100000 bytes 上限按原始字符串计算。正文按原始字符串存储和比较，不 trim、不执行 Unicode normalization。
- `client_run_id`：必填，trim 后 1～128 Unicode 字符；存储和幂等比较都使用 trim 后值，不执行 Unicode normalization。
- `document_id`：可省略或 `null`；提供时必须是正整数并属于当前 Owner。
- `response_language`：可省略、`null` 或空字符串，规范化为 `auto`；其他值 trim、转小写后必须为 `auto | zh | en`。
- 缺省 `document_id` 与显式 `null` 视为同一幂等请求。

新建返回 `202`；相同 `client_run_id`、相同规范化请求返回原 Run 和消息，状态 `200`；相同 ID、不同请求返回 `409 agent_run_idempotency_conflict`。

响应：

```json
{
  "conversation_id": 10,
  "user_message": {},
  "assistant_message": {},
  "agent_run": {}
}
```

`user_message` 与 `assistant_message` 复用现有 Conversation Message DTO；角色必须分别为 `user` 与 `assistant`。

## 4. Agent Run DTO

```json
{
  "id": 9001,
  "conversation_id": 10,
  "user_message_id": 201,
  "assistant_message_id": 202,
  "client_run_id": "run-018f54cc",
  "requested_document_id": 123,
  "requested_response_language": "auto",
  "status": "processing",
  "step_count": 0,
  "max_steps": 5,
  "max_total_tokens": 48000,
  "cancelable": false,
  "steps": [],
  "result": null,
  "error": null,
  "token_usage": {
    "prompt_tokens": 0,
    "completion_tokens": 0,
    "total_tokens": 0
  },
  "created_at": "2026-11-16T10:00:00Z",
  "updated_at": "2026-11-16T10:00:03Z",
  "started_at": "2026-11-16T10:00:01Z",
  "completed_at": null
}
```

规则：

- `status`：`queued | processing | succeeded | failed | canceled`。
- `0 <= step_count <= max_steps`，且 `step_count === steps.length`。
- `cancelable` 只在 `queued` 为 `true`；客户端只读取该字段，不自行推导。
- `steps` 始终存在，空列表为 `[]`，按 `sequence_no` 升序。
- `result` 仅 `succeeded` 非 `null`。
- `error` 仅 `failed` 非 `null`；`canceled` 的 `error` 为 `null`。
- `token_usage` 始终存在，未调用模型时三个字段均为 `0`；必须是非负安全整数、分项之和等于 `total_tokens`，且 Run 三项分别等于全部 Step 对应 Token 字段之和。
- `started_at` 在首次进入 processing 后保留；租约恢复回 queued 时不清空。`completed_at` 仅终态非 `null`。
- terminal Run 不得包含 pending/running Step；succeeded 必须包含一个 succeeded final Step，且其后不得有 Step。

## 5. Agent Step DTO

```json
{
  "id": 3001,
  "agent_run_id": 9001,
  "sequence_no": 1,
  "kind": "tool_call",
  "status": "succeeded",
  "tool_name": "search_documents",
  "error": null,
  "token_usage": {
    "prompt_tokens": 820,
    "completion_tokens": 44,
    "total_tokens": 864
  },
  "created_at": "2026-11-16T10:00:02Z",
  "started_at": "2026-11-16T10:00:02Z",
  "completed_at": "2026-11-16T10:00:03Z"
}
```

规则：

- `kind`：`tool_call | final`。
- `status`：`pending | running | succeeded | failed`。
- `tool_name`：tool_call 时只能是 `search_documents | read_document_evidence`；final 时必须为 `null`。
- 不返回 `arguments_json`、`result_json`、excerpt 或 Provider tool call ID。
- v0 不返回 `summary` 字段；前端只能依据 `kind` 与冻结 `tool_name` 映射本地安全标签。
- `error` 仅 failed 非 `null`。
- `token_usage` 始终存在，是该次 Model decision 的完整非负 Token 三元组；恢复后的 pending Tool Step 保留原值。
- v0 Step DTO 不返回 `updated_at`；`created_at/started_at/completed_at` 足以表达公开生命周期，内部更新时间不由 Handler 推算。
- pending：`started_at=null`、`completed_at=null`；running：`started_at!=null`、`completed_at=null`；succeeded/failed：两者都非 `null`。running 恢复为 pending 时清空 `started_at`，保留 decision Token。
- sequence 从 1 开始、连续、唯一、严格递增。

## 6. Result、Source 与 Error

成功 result：

```json
{
  "answer": "方案 A 更强调低延迟，方案 B 更强调故障恢复。[1][2]",
  "response_language": "zh",
  "sources": [
    {
      "citation": 1,
      "evidence_ref": "step-2:item-1",
      "document_id": 123,
      "chunk_id": 456,
      "title": "系统设计",
      "page_start": 12,
      "page_end": 13,
      "content_hash": "0123456789abcdef0123456789abcdef0123456789abcdef0123456789abcdef"
    }
  ]
}
```

- `citation` 从 1 连续递增，顺序与 final evidence refs 一致。
- `evidence_ref`、document/chunk、标题、页码和 hash 只来自受信 Tool Registry 结果，不接受模型自报元数据。
- `content_hash` 是完整 chunk 内容的 64 位小写十六进制 SHA-256，不带算法前缀。
- `answer` trim 后非空且为有效 UTF-8；`response_language` 只能是 resolved `zh | en`。
- `sources` 必须包含 1～10 项；document/chunk ID 为正整数，`evidence_ref` 必须匹配同 Run 的 `step-N:item-M`。
- `page_start/page_end` 要么同时为 `null`，要么都是正整数且 `page_start <= page_end`。
- `title` 可以为空字符串；`page_start/page_end` 可以为 `null`。
- v0 不返回 excerpt、文件路径、owner、score 或 Provider 原始来源。

安全 error：

```json
{
  "code": "agent_tool_failed",
  "message": "Agent tool execution failed"
}
```

错误只允许冻结安全码和安全消息，不包含 SQL、Prompt、Provider body、凭据、文件路径或堆栈。

允许的 Run 安全错误码：

- `agent_execution_failed`
- `agent_temporarily_unavailable`
- `agent_step_limit_exceeded`
- `agent_token_budget_exceeded`
- `agent_deadline_exceeded`
- `agent_invalid_tool_call`
- `agent_tool_failed`
- `agent_worker_interrupted`

## 7. 查询与恢复

- `GET /agent-runs/:id`：找到返回 `200 {"agent_run": ...}`；Run 不存在或越权返回 `404 agent_run_not_found`。
- `GET /conversations/:id/agent-runs/active`：唯一 queued/processing Run 返回 `200 {"agent_run": ...}`；conversation 存在但无活动 Run 返回空 body `204`。
- `GET /conversations/:id/agent-runs/latest`：最新保留 Run 返回 `200 {"agent_run": ...}`；conversation 存在但从未创建 Run 返回空 body `204`。
- active/latest 的 conversation 不存在或越权返回 `404 conversation_not_found`。归档 conversation 仍可读取已保留的 latest；OwnerScope 继续沿用 Conversation 契约。
- 上述 `200` 的 Run、Steps、聚合 Token 和终态必须来自同一个一致性快照，并满足第 4、5 节全部不变量。

## 8. 取消

- `POST /agent-runs/:id/cancel` 成功返回 `200 {"agent_run": ...}`。
- queued 可取消。
- processing 返回 `409 agent_run_processing`。
- succeeded/failed/canceled 返回 `409 agent_run_terminal`。
- 不存在或越权返回 `404 agent_run_not_found`。

## 9. 容量、冲突与错误码

| 场景 | HTTP | code | 备注 |
|---|---:|---|---|
| 未登录 | 401 | `authentication_required` | 所有 Agent 路由 |
| conversation 路径 ID 非正整数 | 400 | `invalid_conversation_id` | create/active/latest |
| Run 路径 ID 非正整数 | 400 | `invalid_agent_run_id` | get/cancel |
| 请求字段无效 | 400 | `invalid_agent_run_request` | 不调用模型 |
| conversation 不存在或越权 | 404 | `conversation_not_found` | create/active/latest |
| requested document 不存在或越权 | 404 | `document_not_found` | create |
| Run 不存在或越权 | 404 | `agent_run_not_found` | 不泄露存在性 |
| conversation 已归档 | 409 | `conversation_archived` | create；查询仍可读 |
| 同 ID 不同请求 | 409 | `agent_run_idempotency_conflict` | 不静默复用 |
| Agent 创建时 conversation 已有活动 Answer/Agent | 409 | `conversation_execution_active` | 客户端刷新两类 active |
| processing 取消 | 409 | `agent_run_processing` | v0 不强制中断调用 |
| succeeded/failed/canceled 取消 | 409 | `agent_run_terminal` | 保持终态 |
| Owner 队列满 | 429 | `agent_run_owner_queue_capacity` | 正整数 Retry-After |
| 全局队列满 | 503 | `agent_run_queue_capacity` | 正整数 Retry-After |
| 未分类服务端失败 | 500 | `internal_error` | 只返回安全消息与 X-Request-ID |

所有错误响应沿用仓库统一 `{"error":"安全消息","code":"稳定码"}`；Request ID 只通过 `X-Request-ID` 响应头返回。容量响应同时包含正整数 `Retry-After`。

## 10. Feature flags 与非目标

- `AGENT_ENABLED=false`：不注册 API。
- `AGENT_WORKER_ENABLED=false`：不启动 Agent Worker。
- 两者默认均为 false；前端不能开启服务端能力。
- B2-B0 先完成三项服务端契约对齐：`auto` 解析为最终 `zh | en` 并供 Model 与 Final 共用；active/latest 区分 conversation 不存在与“存在但无 Run”；create/get/active/latest/cancel 全部返回统一的一致性 `RunSnapshot`。B2-B0 不增加路由或配置。
- B2-B 只实现 HTTP DTO、Handler 与默认关闭配置，消费 B2-B0 的一致性快照；不启用真实 Provider，不修改 Compose。
- `agent-worker` 角色、Provider wiring、观测与 Compose 属于 B2-C。
- SSE、逐 Token 持久化、processing 强制取消、多 Agent 与写工具不属于 v0。
