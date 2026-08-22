# Embedding 与 Generation 调用成本基线

## 1. 核心目的

本基线用于回答以下工程问题：

1. 一批文档向量化实际调用了多少次远程 API、消耗多少 Token、生成和最终落库多少向量；
2. 一批在线问答有多少成功、失败或无证据跳过，远程生成耗时和 Token 是多少；
3. 更换模型、维度、分块、Top K 或 Prompt 后，成本和耗时相对旧批次发生了什么变化。

它不是新的业务 HTTP 接口，也不评价答案是否正确。答案质量继续使用
[带来源问答质量评估](../evaluation/rag-answer-quality-evaluation-plan.md)。

## 2. 数据流与安全边界

```text
冻结的文档/问题批次
        ↓（只有明确授权后才执行远程调用）
后端 JSONL 结构化日志
        ↓（本地、零远程费用）
go run ./cmd/observability-report
        ↓
统一 JSON 成本与耗时报告
```

汇总命令只读取本地日志，不会启动服务、访问数据库或调用远程 API。日志和报告不得包含 API Key、
用户问题、Prompt、证据正文、答案或完整论文内容；本机原始日志与报告放在 Git 忽略的 `chatgpt/` 目录。

## 3. 可比较批次必须冻结的条件

每次真实批次至少记录：

- 执行日期、语料版本、文档数量和 chunk 数量；
- Embedding 供应商、模型、维度和批大小；
- Generation 供应商、模型、最大输出 Token、温度和 thinking 开关；
- 问题集版本、`top_k`、是否限定 `document_id` 和回答语言；
- 单并发/多并发、机器环境和网络条件；
- 供应商价格来源、币种和查询日期。

上述任一关键条件变化后应建立新批次，不能覆盖旧结果。小样本 P50/P95 只描述该批次，不代表供应商 SLA。

## 4. Windows PowerShell 执行方式

### 4.1 捕获服务日志

在 `backend` 目录执行：

> 执行前必须确认 `LOG_FORMAT=json`；Text 日志只适合人工阅读，不能用于成本汇总。

```powershell
$batchDirectory = "..\chatgpt\后端\评估\模型成本\2026-08-15"
New-Item -ItemType Directory -Force -Path $batchDirectory
$env:LOG_FORMAT = "json"

go run ./cmd/server 2>&1 |
    Tee-Object -FilePath "$batchDirectory\server.jsonl"
```

Gin debug 文本和 JSON 日志可以共存。汇总器会计数并忽略普通文本行；如果某行以 `{` 开头却不是合法 JSON，
它会直接失败，避免静默遗漏损坏日志。

启动服务本身不会产生模型费用，但执行向量任务或 `/answers` 冻结问题集会调用远程 API，必须先获得明确授权。

### 4.2 生成汇总报告

结束服务后，在 `backend` 目录执行：

```powershell
go run ./cmd/observability-report `
    -input  "..\chatgpt\后端\评估\模型成本\2026-08-15\server.jsonl" `
    -output "..\chatgpt\后端\评估\模型成本\2026-08-15\cost-report.json"
```

也可以使用标准输入/输出：

```powershell
Get-Content .\server.jsonl |
    go run ./cmd/observability-report
```

## 5. 报告字段口径

### 5.1 通用字段

| 字段 | 含义 |
| --- | --- |
| `schema_version` | 报告结构版本 |
| `generated_at` | 报告生成 UTC 时间 |
| `scanned_line_count` | 扫描的全部日志行 |
| `json_line_count` | 可解析 JSON 行 |
| `aggregated_event_count` | 真正进入成本汇总的终结事件 |
| `ignored_non_json_line_count` | 被忽略的 Gin 等普通文本行 |
| `ignored_json_event_count` | HTTP、started 等不参与成本求和的 JSON 事件 |

### 5.2 Embedding

- `provider_call_count`、`prompt_tokens`、`total_tokens` 会累计成功、重试和失败尝试，因为已经发生的调用可能已经计费；
- `generated_vector_count` 包含后来因事务回滚而没有保留的部分向量；
- `persisted_vector_count` 只统计 `embedding_job_succeeded` 中已经原子落库的向量；
- `worker_duration` 是一次 Worker 尝试的总耗时，`provider_duration` 只累计远程 Embed 调用耗时。

### 5.3 Generation

- `provider_call_count` 只统计 `succeeded` 和 `failed`；`skipped` 表示无证据安全拒答，没有远程生成费用；
- Token 只累计供应商成功返回 Usage 的调用；失败请求是否产生额外账单应以供应商账单为准；
- `evidence_count_total` 和 `average_evidence_count` 分别表示终结事件的证据总量与每次请求平均证据数，
  用于解释不同 Top K 批次的 Prompt 成本差异；
- `provider_duration` 只计算 Generator 调用，不等于完整 HTTP 延迟。

### 5.4 Answer Admission

`answer_admission` 不统计模型费用，而是回答“问答请求是否正在等待、被拒绝或已经释放并发槽位”：

- `events`：`answer_request_admitted`、`answer_request_rejected`、`answer_request_released` 的数量；
- `outcomes`：终结事件中的 `succeeded`、`downstream_error`、`capacity_timeout`、`canceled` 数量；
- `capacity_timeout_count`：在配置等待时间内没有取得槽位的请求数；
- `canceled_wait_count`：等待期间被客户端或上游 context 取消的请求数；
- `max_observed_in_flight`：这批日志实际观察到的进程内最大并发问答数；
- `wait_duration`：每个终结请求等待槽位的耗时；
- `execution_duration`：已取得槽位的请求执行完整问答链路的耗时。

等待耗时只从 `released` 和 `rejected` 读取。`admitted` 事件也含同一份等待耗时，但只用于观察准入瞬间，
不能再次加入耗时样本。否则每个成功请求会被统计两遍，平均值和分位数都会失真。

耗时统计统一提供 `count`、`total_ms`、`average_ms`、`min_ms`、`p50_ms`、`p95_ms` 和 `max_ms`。
P50/P95 使用 nearest-rank：先从小到大排序，再取向上取整后的第 50%/95% 位置。

## 6. 金额换算

工具不内置供应商单价，因为价格会变化。批次报告应另行记录查询日期和官方价格，然后计算：

```text
Embedding 金额 = prompt_tokens / 1,000,000 × 当日 Embedding 输入单价
Generation 金额 = prompt_tokens / 1,000,000 × 当日输入单价
                + completion_tokens / 1,000,000 × 当日输出单价
```

不同币种、免费额度和阶梯价格不能直接混加；供应商账单是最终权威数据。

## 7. 当前状态

2026-08-15 已完成 P5.2.6 第一部分：事件字段、严格 JSONL 汇总器、命令行入口、P50/P95 统计和自动化测试均已就绪。
汇总器测试只使用合成日志，没有调用真实模型。P4 的 14 次生成、39,982 Token 和端到端耗时继续作为历史质量
参考，但由于当时记录的是 HTTP 总耗时而不是当前的 provider duration，不能伪装成新口径的第一批结果。

2026-08-22 报告结构升级为 `schema_version=2`，同一命令能够额外汇总问答并发准入、容量超时、取消等待、
等待/执行耗时和最大观测并发。该扩展同样只读取日志，不会发起远程问答。

下一次真实成本批次需要冻结语料和问题集，并在用户明确同意产生远程费用后执行。
