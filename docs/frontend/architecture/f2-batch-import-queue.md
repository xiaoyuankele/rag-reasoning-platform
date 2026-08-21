# F2 批量导入与解析队列

> 状态：2026-08-20 已完成批量队列和上传前重复预检实现；自动化门禁与真实多文件专项验收状态见第 7、8 节。

## 1. 目标与边界

批量导入不新增后端批次 API，而是在浏览器中编排已经稳定的单文档接口：

```text
选择最多 20 份文件
  → 单路 Web Worker 分块计算 SHA-256
  → 后端预检当前用户是否已有内容
  → 未命中时前端有限并发上传
  → 后端重新哈希并承担最终判重
  → 按文档状态决定是否创建解析任务
  → 集中观察任务或文档状态
  → 单项进入 ready / failed
  → 批次结束后刷新文档列表
```

本切片支持 PDF、Markdown 和纯文本文件，默认上传并发为 2。单项失败不会中止其他文件，失败项可以独立重试。
它不提供后端任务取消、批次持久化、批次级审计或批量向量化。

## 2. 复用接口

| 接口                          | 批量队列中的职责                                    |
| ----------------------------- | --------------------------------------------------- |
| `POST /documents/preflight`   | 按本地摘要检查当前用户已有内容，命中时跳过正文上传  |
| `POST /documents`             | 逐文件上传，读取 `201/200` 与 `duplicate`，最终判重 |
| `POST /documents/:id/process` | 为需要解析的文档创建任务                            |
| `GET /processing-jobs/:id`    | 已知 job ID 时观察任务终态                          |
| `GET /documents/:id`          | 确认最终文档状态，或恢复未知 job 的任务             |
| `GET /documents`              | 批次变化后刷新文档列表                              |

每个请求仍独立成功或失败。前端没有把多个大文件封装进一个 multipart 请求，因此可以隔离失败并按单项重试。

## 3. 队列状态机

```text
waiting → hashing → checking → duplicate
             │          │
             │          ├→ uploading → queueing → queued → processing → ready
             │          │      │           │                    │
             │          │      └→ upload-failed                 └→ process-failed
             │          │                  └→ queue-failed
             │          └→ check-failed
             └→ hash-failed

waiting / hashing / checking / uploading / queueing → stopped（用户停止剩余操作）
failed / stopped → waiting（重试或继续）
```

- `waiting`：已选入本地队列，尚未占用上传并发；
- `hashing`：Worker 正在分块计算本地摘要并报告进度；
- `checking`：正在调用后端预检确认当前用户是否已有相同内容；
- `duplicate`：预检命中 ready 文档，跳过上传并提供已有记录入口；
- `uploading`：浏览器正在发送单个文件；
- `queueing`：文档已存在，正在请求解析任务；
- `queued / processing`：后端任务或文档仍在运行；
- `ready`：重新读取文档后确认解析完成；
- 哈希、预检、上传、排队和解析失败分别保留阶段、安全错误和可选请求编号；
- `stopped`：前端不再继续该项，但已经被后端领取的任务不会因此取消。

任务 `succeeded` 不直接把队列项改成 `ready`，前端仍请求文档详情确认状态，保持任务状态与文档状态分离。

## 4. 重复内容分支

内容是否重复只以 OwnerScope 下后端 SHA-256 判定为准，文件名和前端本地哈希都不是最终依据：

- `201 + duplicate:false`：新建记录，自动请求解析；
- `200 + duplicate:true + ready`：直接复用已有 ready 文档；
- `200 + duplicate:true + processing`：不重复创建任务，改为观察文档；
- `200 + duplicate:true + uploaded/failed`：创建或重试解析任务；
- 创建任务收到稳定 `409`：按“可能已有活动任务”恢复观察，不把整批判为失败。

## 5. 并发、轮询与停止

- 上传使用固定大小 worker pool，默认同时最多 2 个请求；
- 哈希使用一个可复用 Dedicated Worker，4 MiB 分块且并发为 1，避免多份大文件同时占用 CPU 和分块内存；
- 所有活动项共用一个轮询调度器，每轮最多检查 4 项并轮换游标，避免每份文件各自创建高频计时器；
- 轮询使用上一轮完成后再安排下一轮的方式，任务终态、队列清空和组件卸载都会停止；
- “停止剩余操作”会停止 waiting 项，协作取消本地哈希，并中止浏览器仍可取消的预检、上传和排队请求；
- 已保存文档或已进入后端的任务继续由后端执行，界面明确说明这一边界。

前端上传并发与后端 PDF 处理并发是两套独立容量：浏览器当前默认最多同时上传 2 份文件，后端解析 Worker 当前仍为单并发，后续会先增加固定大小的后端 Worker，再评估 Python 进程复用。前端不能根据自己的上传 worker 数量推断后端处理能力，也不能把 `queued` 直接显示为“处理中”。

后端并发改造第一阶段不新增批次 API，前端继续使用单文档上传、创建解析任务和任务查询接口。若后端资源受限返回 `429` 或 `503`，前端应保留单项失败隔离，并按 `Retry-After` 或退避策略重试；不应通过无限提高浏览器并发来补偿后端排队。

## 6. 代码分层

```text
DocumentsPage
├─ DocumentBatchImportPanel       选择、批次汇总、单项操作
│  └─ useDocumentImportQueue      并发、分支、轮询、停止与重试
│     ├─ file-hash-worker-client  单 Worker 哈希队列、进度与取消
│     ├─ document-preflight-api   预检 HTTP 与运行时 DTO 校验
│     └─ document-api             上传 HTTP 与运行时 DTO 校验
├─ DocumentLibraryPanel           只负责列表和选择
└─ DocumentDetailPanel            详情、chunks 与删除
```

上传编排从列表组件中拆出后，列表不再同时承担本地文件、批次状态和分页状态；单文件导入仍是批量入口中只有一项的特例。

## 7. 验证与待办

- 类型检查、ESLint、Prettier 和生产构建通过；
- 16 个测试文件、47 个测试通过；
- 自动化覆盖并发上限、成功解析、ready 重复复用、`409` 恢复、单项失败隔离、非法类型和失败重试入口；
- 已在真实登录态只读打开 `/documents`，确认多文件属性、类型限制、空态、1280px 布局和控制台正常；
- 尚未由自动化或 Codex 擅自写入真实文件；用户需用 2～3 份测试文件完成真实批量操作验收；
- 若未来要求刷新后恢复整个批次、批次级取消或大量文件长期追踪，再设计后端批次资源。

并发演进的跨端约束集中记录在[文档处理并发与 Python 进程复用交接](../../shared/architecture/document-processing-concurrency-review.md)，后端修改 Worker 或 Python 执行器前应先阅读该文档。

## 8. 已接入：上传前重复文件预检

2026-08-20 正式接入 `POST /documents/preflight`。队列在 `uploading` 之前增加单路 Web Worker 分块 SHA-256 和
`checking` 阶段；命中已有内容时跳过正文上传，并继续复用本文已有的文档状态分流与解析编排。

网络、超时和 5xx 会按正式契约 fail-open，保留提示后进入现有上传流程；400、401、413 则停止该项，不能绕过预检。
现有 `POST /documents` 的服务端哈希、`200 + duplicate:true` 和数据库唯一约束仍是最终一致性兜底。实现、状态、资源边界与
真实验收清单见 [F2 上传前重复文件预检](f2-upload-preflight-evaluation.md)。

接入后的完整前端门禁通过：23 个测试文件、77 个测试，以及类型检查、ESLint、Prettier 和生产构建均成功。
