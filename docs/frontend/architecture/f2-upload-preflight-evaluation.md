# F2 上传前重复文件预检

> 状态：2026-08-20 正式 HTTP 契约已冻结，前后端均已实现；前端完整自动化门禁与简单真实批次验收通过，
> 大文件、故障降级和跨用户专项仍待完成。共享契约以
> [HTTP API 总览](../../shared/api/http-api-overview.md)“2.1 上传前重复文件预检正式契约”为准。

## 1. 目标与边界

同一用户内的重复文件原本要完整上传后，才能由后端计算 SHA-256 并查询已有记录。近期真实批次中有
20 次 `POST /documents`，其中 13 次为已有内容；预检用于省去这些重复正文的网络传输、服务端读取与哈希成本。

第一版只判断当前登录用户内二进制完全相同的文件：

- 文件名不同但字节完全相同可以命中；
- 可见内容相同但 PDF 元数据、压缩或字节不同不会命中；
- 身份只来自 `rag_session` Cookie，前端不发送 `user_id`；
- 前端不访问 PostgreSQL 或 Redis，第一版不引入 Redis；
- 浏览器摘要只用于预检，后端上传时仍重新哈希并由数据库唯一约束处理竞争；
- 该能力不会提升新 PDF 的 Python 解析速度。

## 2. 已实现数据流

```text
选择最多 20 份文件
  → 扩展名、空文件、200 MiB 上限校验
  → waiting
  → hashing（Dedicated Worker 分块 SHA-256）
  → checking（POST /documents/preflight）
     ├─ exists=true  → duplicate，保存已有文档，跳过正文上传
     ├─ exists=false → uploading → POST /documents
     ├─ 网络/超时/5xx → 显示降级提示并继续 POST /documents
     └─ 400/401/413 → check-failed，不允许绕过预检
  → queueing / queued / processing / ready / failed
```

预检命中 ready 文档时，队列显示“已有内容 · 可用”、已有文件名、文档 ID 和“查看文档”入口。
命中 uploaded/processing/failed 文档时，继续复用原有解析编排和集中轮询。预检未命中后，上传端若因并发竞争返回
`200 + duplicate:true`，仍按正常成功分支复用已有文档。

## 3. 正式 HTTP 契约

请求：

```http
POST /documents/preflight
Content-Type: application/json
Cookie: rag_session=...

{
  "sha256": "64位小写十六进制SHA-256",
  "size_bytes": 14
}
```

不发送 `original_name`，文件名不参与判重；不发送 `user_id`，所有者只来自 Session。`size_bytes` 必须为正整数且不得超过
200 MiB。

成功响应统一为 `200`：

```json
{ "exists": false, "document": null }
```

或：

```json
{
  "exists": true,
  "document": {
    "id": 3,
    "title": null,
    "original_name": "first-upload.pdf",
    "mime_type": "application/pdf",
    "size_bytes": 14,
    "sha256": "...",
    "status": "ready",
    "error_message": null,
    "created_at": "...",
    "updated_at": "..."
  }
}
```

稳定失败语义：

| 状态      | code                         | 前端处理                                      |
| --------- | ---------------------------- | --------------------------------------------- |
| 400       | `invalid_document_preflight` | 停在 `check-failed`，不上传                   |
| 401       | `authentication_required`    | 触发全局会话失效流程，不上传                  |
| 413       | `file_too_large`             | 提示超过服务端上限，不上传                    |
| 500       | `internal_error`             | fail-open，保留请求编号并让上传接口最终判重   |
| 网络/超时 | —                            | fail-open，提示预检暂不可用并继续现有上传流程 |

`exists=false` 不是预约或锁，两个标签页可能同时得到未命中；最终一致性仍由上传接口和数据库唯一约束保证。

## 4. 哈希实现与资源边界

前端已选择 `hash-wasm@4.12.0` 的增量 `createSHA256()`，没有使用必须整份读入内存的
`crypto.subtle.digest()`：

- 一个可复用的 Vite 模块化 Dedicated Worker；
- Worker 内按 4 MiB `File.slice().arrayBuffer()` 分块读取；
- 哈希并发固定为 1，上传并发继续为 2，形成单路哈希与双路上传流水线；
- 每块完成后回报 `processedBytes / totalBytes`，界面显示百分比；
- 取消在分块边界协作完成，排队任务可立即取消；
- 页面作用域销毁时终止 Worker，并中止活动预检/上传请求；
- 输出必须为 64 位小写十六进制；预检和上传响应还会核对摘要与文件大小。

分块方案使额外读取内存主要受 `hashConcurrency × chunkSize` 约束，而不是随所有待导入文件总大小线性增长。

## 5. 前端分层

```text
features/documents/
├─ api/document-preflight-api.ts
│    请求正式接口，运行时校验真假分支和命中文档
├─ model/file-hash-protocol.ts
│    主线程与 Worker 的命令/事件联合类型
├─ model/incremental-file-hash.ts
│    可独立测试的分块哈希循环
├─ model/file-hash.worker.ts
│    Worker 内 hash-wasm、进度与协作取消
├─ model/file-hash-worker-client.ts
│    单 Worker 顺序队列、AbortSignal 与生命周期
├─ model/use-document-import-queue.ts
│    hash/check/upload/queue/process 状态编排和降级
└─ ui/DocumentBatchImportPanel.vue
     进度、已有文档、警告、失败与重试入口
```

哈希只服务文档导入，因此保留在 feature 内，不进入 Pinia 或全局 `shared/`。API 层继续先把响应视为 `unknown`，
校验后再转换为前端 camelCase 模型。

## 6. 状态与重试

新增状态为 `hashing`、`checking`、`duplicate`、`hash-failed` 和 `check-failed`；队列项新增本地摘要、哈希进度和
可选降级警告。失败项重试会重新计算摘要并再次预检，停止项继续时也从本地哈希开始。已有后端文档的解析排队失败仍从
文档阶段重试，不重复上传正文。

`duplicate` 既是预检 ready 文档的终态标签，也是队列项上的持久维度；已有文档仍在处理时，主状态继续表达
`queued/processing`，汇总区同时统计“已有”。

## 7. 自动化覆盖与待验收

已覆盖：

- 标准 SHA-256 跨分块计算、单调进度和协作取消；
- 单 Worker 顺序调度、活动任务取消和非法摘要拒绝；
- 预检真假响应、矛盾响应、摘要/大小不匹配拒绝；
- 命中已有文档时不调用上传接口；
- 网络/5xx fail-open 并保留 `X-Request-ID`；
- 400 确定性拒绝不绕过预检；
- 原上传并发、解析轮询、上传端竞争兜底和单项失败隔离回归。

完整前端门禁结果：类型检查、ESLint、Prettier、生产构建以及 23 个测试文件、77 个测试全部通过；Vite 生产构建已把
哈希 Worker 独立输出为约 19 KiB 资源。

简单真实批次已验证：连续两批共预检 33 份文件，只上传 1 份新文件，32 份已有内容跳过正文上传；用户确认预检功能正常。
后端日志中 33 次预检平均 0.94 ms、最大 13 ms、累计 31 ms，第一批避免 16/17 次上传，第二批避免 16/16 次上传。
唯一新文件为 10.44 MiB，完整上传请求耗时 959 ms。该数据证明高重复批次收益明显，但 Gin 日志不包含第一次本地哈希开始时间，
不能把上述窗口当作完整的浏览器端到端基准。

仍需专项验收：

1. 使用固定的同一批文件，分别运行关闭/开启预检各 3 次，记录浏览器端哈希、网络和总耗时；
2. 使用第二用户上传相同内容，确认不会命中第一用户文档；
3. 接近 200 MiB 文件进度单调、页面可响应且停止有效；
4. 暂停后端或制造 5xx，确认界面展示降级提示且上传接口仍承担最终去重；
5. 验证 400、401、413 不会绕过预检继续上传。

## 8. 后续演进

- 是否增加同一批次内摘要合并，先用真实重复批次数据评估；
- 是否把哈希并发从 1 提高到 2，必须先测 CPU、内存和界面响应；
- 若需要严格性能对比，增加只在开发模式启用的本地哈希耗时观测，不记录文件内容或摘要；
- 批量预检接口、模糊重复和可见内容相似性不进入第一版；
- 日志和界面不得泄露文件内容，自动化也不上传真实私有文档。
