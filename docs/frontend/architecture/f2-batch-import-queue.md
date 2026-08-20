# F2 批量导入与解析队列

> 状态：2026-08-18 已完成前端实现、自动化门禁和只读页面验收；真实多文件批次操作仍待用户验收。

## 1. 目标与边界

批量导入不新增后端批次 API，而是在浏览器中编排已经稳定的单文档接口：

```text
选择最多 20 份文件
  → 前端有限并发上传
  → 后端按内容哈希判断是否重复
  → 按文档状态决定是否创建解析任务
  → 集中观察任务或文档状态
  → 单项进入 ready / failed
  → 批次结束后刷新文档列表
```

本切片支持 PDF、Markdown 和纯文本文件，默认上传并发为 2。单项失败不会中止其他文件，失败项可以独立重试。
它不提供后端任务取消、批次持久化、批次级审计或批量向量化。

## 2. 复用接口

| 接口 | 批量队列中的职责 |
| --- | --- |
| `POST /documents` | 逐文件上传，读取 `201/200` 与 `duplicate` |
| `POST /documents/:id/process` | 为需要解析的文档创建任务 |
| `GET /processing-jobs/:id` | 已知 job ID 时观察任务终态 |
| `GET /documents/:id` | 确认最终文档状态，或恢复未知 job 的任务 |
| `GET /documents` | 批次变化后刷新文档列表 |

每个请求仍独立成功或失败。前端没有把多个大文件封装进一个 multipart 请求，因此可以隔离失败并按单项重试。

## 3. 队列状态机

```text
waiting → uploading → queueing → queued → processing → ready
             │            │                    │
             └→ upload-failed                 └→ process-failed
                          └→ queue-failed

waiting / uploading / queueing → stopped（用户停止剩余操作）
failed / stopped → waiting（重试或继续）
```

- `waiting`：已选入本地队列，尚未占用上传并发；
- `uploading`：浏览器正在发送单个文件；
- `queueing`：文档已存在，正在请求解析任务；
- `queued / processing`：后端任务或文档仍在运行；
- `ready`：重新读取文档后确认解析完成；
- 三种失败状态分别保留失败阶段、安全错误和可选请求编号；
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
- 所有活动项共用一个轮询调度器，每轮最多检查 4 项并轮换游标，避免每份文件各自创建高频计时器；
- 轮询使用上一轮完成后再安排下一轮的方式，任务终态、队列清空和组件卸载都会停止；
- “停止剩余操作”会停止 waiting 项并中止浏览器仍可取消的上传/排队请求；
- 已保存文档或已进入后端的任务继续由后端执行，界面明确说明这一边界。

前端上传并发与后端 PDF 处理并发是两套独立容量：浏览器当前默认最多同时上传 2 份文件，后端解析 Worker 当前仍为单并发，后续会先增加固定大小的后端 Worker，再评估 Python 进程复用。前端不能根据自己的上传 worker 数量推断后端处理能力，也不能把 `queued` 直接显示为“处理中”。

后端并发改造第一阶段不新增批次 API，前端继续使用单文档上传、创建解析任务和任务查询接口。若后端资源受限返回 `429` 或 `503`，前端应保留单项失败隔离，并按 `Retry-After` 或退避策略重试；不应通过无限提高浏览器并发来补偿后端排队。

## 6. 代码分层

```text
DocumentsPage
├─ DocumentBatchImportPanel       选择、批次汇总、单项操作
│  └─ useDocumentImportQueue      并发、分支、轮询、停止与重试
│     └─ document-api             HTTP 与运行时 DTO 校验
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
