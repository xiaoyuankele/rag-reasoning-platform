# F2-B 文档操作闭环

> 状态：2026-08-29 已接入解析任务最近状态恢复、queued 取消、canceled/cancelable 和容量冷却；
> 自动化验证已完成，真实刷新/取消竞态与容量专项仍待执行。

## 1. 范围

F2-B 把 F2-A 的“列表与上传”扩展为一条可观察、可重试、可安全删除的文档操作链：

```text
选择列表记录
  → 获取详情
  → 用户显式创建解析任务
  → 轮询 processing job
  → 文档进入 ready / failed
  → ready 时分页浏览 chunks
  → 非活动任务期间可二次确认删除
```

`ready` 只表示文本解析和 chunks 已完成，不表示当前 Embedding 模型已经覆盖该文档。F2-B 不暴露手动向量化，
也不进入语义检索或问答费用链路。

## 2. 接口与前端输出

| 接口                               | 成功结果                 | 前端职责                                              |
| ---------------------------------- | ------------------------ | ----------------------------------------------------- |
| `GET /documents/:id`               | `200` 文档详情           | 校验 DTO，显示元数据和文档状态                        |
| `POST /documents/:id/process`      | `202` processing job     | 保留 job ID，进入任务状态机                           |
| `GET /processing-jobs/:id`         | `200` processing job     | 轮询 queued / processing，终态停止                    |
| `POST /processing-jobs/latest`     | `200` 文档到最近任务快照 | 页面进入、刷新或 409 后恢复真实 job ID 与状态         |
| `POST /processing-jobs/:id/cancel` | `200` canceled job       | 只对 cancelable queued 任务提供取消；冲突后重新取状态 |
| `GET /documents/:id/chunks`        | `200` chunks 分页        | 只在 ready 时请求，保留原文顺序和页码                 |
| `DELETE /documents/:id`            | `204` 无正文             | 二次确认后删除，关闭详情并刷新列表                    |

所有响应先以 `unknown` 进入 API 层，经过字段、状态、时间和分页约束校验后才成为前端实体。
API 层同时校验 job/document ID 和 chunk/document ID 的关联，防止界面消费错配响应。

## 3. 代码分层

```text
DocumentsPage
├─ DocumentBatchImportPanel   单文件/批量上传与解析编排
├─ DocumentLibraryPanel       列表、选中记录
└─ DocumentDetailPanel        详情、操作和状态展示
   └─ useDocumentDetail       详情/chunks/删除编排与刷新恢复
      └─ useProcessingJob     创建任务、轮询、终态停止、卸载清理
         └─ api/*             HTTP、DTO 校验和 camelCase 转换
            └─ Go OwnerScope 接口
```

- `pages` 只保存当前选中的文档 ID 和列表刷新令牌；
- 批量上传与列表分页状态分离，单文件上传是批量入口只有一项的情况；
- `ui` 不直接调用 Axios，也不解释后端 snake_case；
- `model` 使用递归 `setTimeout`，只有上一次请求完成后才安排下一次轮询；
- 切换文档、关闭组件或删除成功时会取消请求和计时器；
- `entities/document` 与 `entities/processing-job` 分开，因为文档状态和任务状态不是同一个状态机。

## 4. 两套状态机

### 4.1 文档状态

```text
uploaded → processing → ready
                     ↘ failed
failed   → processing（显式重试）
```

### 4.2 解析任务状态

```text
queued → processing → succeeded
   │                ↘ failed
   └→ canceled
```

任务 `succeeded` 后重新读取文档详情；只有详情实际为 `ready` 才请求 chunks。界面不根据任务结果自行伪造文档状态。

## 5. 刷新恢复与取消

页面读取文档详情后调用 `POST /processing-jobs/latest`，按当前文档 ID 恢复最近任务。返回 queued/processing 时保存
真实 job ID 并只轮询活动状态；succeeded/failed/canceled 均作为终态停止。`job:null` 表示当前用户不可见或没有任务，
不允许前端推断资源是否存在。若发现接口暂时失败而文档已经 processing，仍保留轮询文档详情的降级路径。

只有后端返回 `cancelable:true` 的 queued 任务显示“取消排队”。取消与 Worker 领取存在原子竞态：收到 409 时前端不会
伪装取消成功，而是立即 `GET /processing-jobs/:id` 读取真实 processing/终态。canceled 任务不改变文档状态，用户可在
uploaded/failed 文档上重新创建新的解析任务。

## 6. 删除边界

后端当前没有禁止删除 processing 文档的显式状态约束。前端在已知任务 `queued/processing`、文档
`processing` 或正在恢复未知任务时禁用删除，避免 Worker 与文件删除并发。该限制是前端安全策略，不冒充后端契约。

删除采用页面内二次确认，不使用第一次点击立即删除。`204` 后详情关闭、列表刷新；`404` 表示记录已经不存在或不属于
当前账户，其他错误继续通过统一 `ApiError` 显示安全提示和可选请求编号。

## 7. 验证边界

- 已通过 API 映射、最近任务恢复、取消竞态、容量冷却、组件操作和轮询终止自动化测试；
- 已通过类型检查、Lint、格式检查和生产构建；
- 已在真实登录会话下确认 `/documents`、Vite `/api` 代理、空态和控制台无错误；
- 已由用户在真实数据上确认上传、解析和删除正常；chunks 分页、取消竞态、容量和跨设备恢复仍需专项验收；
- F2-C 的共享文档选择器和双用户产品隔离验收仍未进入本阶段。
