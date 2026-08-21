# 前端交接：用户可选向量化与文档编辑

> 状态：2026-08-21 基础向量化页面已实现。后端提交 `c4696bc` 已提供单份/批量向量申请、任务查询和取消；
> 前端已经接入独立向量化页面、文本/向量双状态筛选、逐项状态、集中轮询、取消、当前浏览器会话恢复和
> “全部文档”顺序拆批提交。
> 原文件读取、按 document 永久恢复任务和 Markdown 保存接口仍未实现。跨端决策以
> [文档向量化、在线编辑、并发与缓存设计复盘](../../shared/architecture/document-vectorization-editing-concurrency-review.md)
> 为准。

## 1. 前端目标与非目标

本阶段在现有上传、解析、chunks 和删除闭环上增加：

- [x] 在独立页面选择单篇或多篇文档申请向量化；
- [x] 在独立页面将当前可操作的全部文档申请向量化，并按后端 100 项上限自动拆批；
- [x] 文档解析状态和向量状态的独立展示；
- [x] 按当前浏览器会话已知的向量任务状态计数和筛选；
- [x] 当前浏览器会话刷新后按已知 job ID 恢复向量任务；
- [ ] 批量导入结束后提供“不向量化 / 选中 / 本批次全部”入口；
- [ ] 后端提供按 document 查询或列表接口后完成跨设备永久恢复；
- PDF 受保护打开和可选 annotation；
- Markdown 内容查看、编辑、本地草稿和版本冲突处理。

第一版不实现多人实时协同编辑，不引入 CRDT/OT，不把 Redis 状态当作页面真相，也不在后端契约冻结前通过
前端轮询猜测任务 ID。

## 2. 与当前 F2 的衔接

当前 [F2 批量导入与解析队列](f2-batch-import-queue.md) 明确不包含批量向量化。本设计作为后续增量：

```text
DocumentBatchImportPanel
  → 继续负责有限并发上传与解析
  → 记录本批次成功得到的 document IDs
  → 根据用户选择调用 POST /embedding-jobs/batch
  → 单项向量失败不回滚已经成功的上传和解析
```

当前实现保持两个 feature 解耦：`features/documents` 不导入向量模块，用户通过 `/embeddings` 独立管理文档选择与任务。
后续若在批量导入完成后增加快捷入口，由页面层只传递成功 document IDs，不把两套状态机合并。

前端不能把“全部向量化”解释成同时在浏览器直接调用多个模型。它只提交持久任务意图，实际并发由后端 Worker、
用户配额和全局背压控制。

## 3. 交互与状态展示

批量上传区建议提供本批次选项：

```text
向量化方式
○ 暂不向量化
○ 只向量化选中文档
○ 本批次成功文档全部向量化
```

文档列表或详情需要同时展示两套状态：

| 解析状态         | 向量状态         | 用户看到的说明                 | 可用操作                     |
| ---------------- | ---------------- | ------------------------------ | ---------------------------- |
| uploaded         | none             | 文本未解析，尚未申请向量化     | 开始解析、申请向量化         |
| processing       | waiting_document | 文本解析中，成功后进入向量队列 | 取消向量意图                 |
| ready            | none             | 文本可浏览，尚未向量化         | 向量化                       |
| ready            | queued           | 已进入向量队列                 | 取消                         |
| ready            | processing       | 正在向量化                     | 查看进度；第一版不可强制取消 |
| ready            | succeeded        | 当前版本向量已就绪             | 进入语义检索/问答            |
| ready            | failed           | 向量化失败                     | 查看安全错误、重试           |
| failed           | waiting_document | 解析失败，向量任务等待前置条件 | 重试解析或取消向量意图       |
| 任意现存解析状态 | canceled         | 用户已经取消当前向量任务       | 需要时重新申请向量化         |

页面不得仅凭解析 `ready` 开放语义问答；必须由后端提供的当前 revision 向量就绪事实决定。UI 使用“文本未解析 / 文本已解析”
和“向量：……”两套独立徽标，并提供两个独立筛选器。向量筛选在发现接口交付前必须标注“当前会话”；`untracked`
只表示当前浏览器没有恢复到 job ID，不表示从未向量化。

## 4. 当前基础向量化分层

```text
entities/
├─ document/                  文档与解析状态
└─ embedding-job/             向量任务状态、活动判断与可取消规则

features/embeddings/
├─ api/
│  └─ embedding-api           单个、批量、按 ID 查询与取消；运行时 DTO 校验
├─ model/
│  └─ use-embedding-workspace 文档选择、逐项结果、有界轮询、取消和会话恢复
└─ ui/
   └─ EmbeddingWorkspacePanel 独立工作区

pages/
└─ EmbeddingsPage             路由组合层，向 UI 注入 documents 分页函数
```

API 层把响应先作为 `unknown` 校验，不允许 UI 直接解释 snake_case 或英文错误文本。单篇响应校验 document ID，
批量响应校验每个请求 ID 都有且只有一个结果。页面只保存筛选输入；选择 ID、任务和轮询编排留在 feature model。

## 5. 批量向量化前端规则

- 只提交当前后端文档列表返回的 document IDs；服务端仍必须重新校验 OwnerScope，不能信任前端列表；
- 当前独立页面的“全部文档”指当前 OwnerScope 列表中没有已知成功/活动任务的全部可操作文档，不受文本解析状态筛选影响；
- `uploaded/processing/failed` 文档也可提交，后端将其保存为 `waiting_document`，前端不得伪装为向量成功；
- 遵守后端单批 100 项上限。全部文档超过上限时顺序拆成多批，整批错误不得抹去前序批次已经成功的逐项结果；
- 响应逐项使用 `created`、`already_active`、`not_found` 或 `failed`；成功项中的 `job.status` 再区分
  `waiting_document`、`queued` 或当时已有的 `processing`；当前没有 `already_succeeded` 结果；
- 整批请求失败与单项拒绝分开表达；已成功项不能因其他项失败而回滚为本地失败；
- 重复点击保持可重试，不能为了避免重复而只依赖按钮 disabled；
- `429` 展示用户配额或频率提示并遵守 `Retry-After`；`503` 表示系统暂时饱和；
- 取消调用 `POST /embedding-jobs/:id/cancel`。成功和重复取消返回 `200`；收到 `409` 时重新查询任务，
  说明 Worker 已经领取或任务已经终结，不在客户端强行改为 canceled。
- 当前轮询使用一个调度器，单轮最多 4 路查询并发，不为每一行创建独立定时器；页面隐藏时暂停，恢复可见后立即同步。
- 当前 `sessionStorage` 只保存 document ID 到最近 job ID 的映射。换账户时后端 OwnerScope 会使旧任务返回 404，
  前端随即清理映射；该存储不承担权限判断或永久任务历史。
- 后端目前只复用活动任务，无法让前端先发现历史 succeeded 任务；提交确认必须提示可能重新生成向量并消耗远程模型额度。

## 6. PDF 打开与缓存

PDF 内容必须通过受保护接口或受控短期地址读取。若后端支持 Range，Viewer 应按需加载，不把大文件转换成完整
Base64 放进 Pinia 或普通响应缓存。

缓存响应只接受私有策略：

```text
ETag: "文档内容哈希或 revision"
Cache-Control: private, no-cache
```

高度敏感文档如果后端返回 `private, no-store`，前端不得绕过。退出登录时清理内存中的 Blob URL、选中文档和
请求缓存；浏览器自身已经持久化的私有缓存由响应策略控制。

PDF annotation 与原文件分离。纯批注成功后更新 annotation 查询，不自行把 chunks 或 embeddings 标记为过期。

## 7. Markdown 编辑、草稿与冲突

编辑器内存是当前输入状态；IndexedDB 只用于崩溃和刷新恢复；服务端 revision 才是正式内容。

建议草稿键：

```text
draft:{userId}:{documentId}:{baseRevision}
```

草稿至少保存 `content`、`baseRevision` 和本地更新时间。禁止只按 document ID 保存，否则同一浏览器切换账户时可能
读到其他账户草稿。退出后可以清理当前账户草稿，也可以保留但必须继续按用户隔离；产品需在实现时冻结其中一种。

自动保存采用防抖，不逐字符请求：

```text
用户输入
  → 立即更新编辑器内存
  → 约 1～3 秒无输入后写 IndexedDB
  → 明确自动保存周期或用户点击保存后写服务端
```

服务端保存请求携带 `base_revision` 或 `If-Match`。收到版本冲突时：

1. 保留本地草稿，绝不能清空；
2. 拉取服务端最新版本；
3. 展示“查看最新、复制本地内容、手动合并”等选择；
4. 未实现可靠合并前，不提供无提示强制覆盖。

保存成功后用响应中的新 revision 更新基线、清理对应旧草稿，并失效文档详情、内容、chunks 和向量就绪查询。

## 8. 前端请求缓存边界

适合缓存：文档列表、详情、当前任务状态和带 revision 的只读内容响应。缓存键至少包含资源 ID 和 revision；
认证状态从已登录变为未登录时清空全部用户业务查询。

不适合作为普通请求缓存：

- 编辑器尚未保存的唯一草稿；
- 完整 PDF Base64；
- Worker 进度的唯一事实；
- 权限判断结果的长期副本；
- 未带 revision 的 Markdown 渲染结果。

如果后续接入 Redis Pub/Sub + SSE/WebSocket，推送只负责提示“某资源已变化”，前端仍按 job ID 或 document ID
重新读取服务端最终状态；断线时自动退回当前轮询机制。

## 9. 后端已冻结与仍待冻结的契约

提交 `c4696bc` 已冻结：

1. 单份申请的 `202/200` 幂等语义；
2. 批量请求 `POST /embedding-jobs/batch`、最多 100 个 ID 和逐项结果；
3. `waiting_document/canceled` 正式状态；
4. `POST /embedding-jobs/:id/cancel` 的允许状态、`409` 和稳定错误 code。

前端开发期间仍需避免猜测以下计划契约：

1. 批量按 document IDs 发现当前/最近向量任务或分页列出任务的接口；当前页面只能恢复本浏览器会话已知 job ID。
   优先返回逐文档最新任务或 `null`，避免前端对文档列表发出 N+1 个请求；正式路径和 DTO 仍由后端冻结；
2. PDF/MD 内容读取的 MIME、Range、ETag 与缓存头；
3. Markdown revision DTO、保存成功和冲突响应；
4. 内容变化后 chunks/embeddings 的 stale 表达；
5. 用户默认向量偏好是否属于本阶段。

## 10. 前端验收矩阵

- [x] 独立页面手动选择单篇和多篇向量化；
- [x] 独立页面一键提交全部可操作文档，超过 100 篇时自动顺序拆批；
- [x] 单项失败、整批失败、创建和复用的前端自动化分支；
- [x] 已知 job ID 的刷新恢复、waiting/queued 集中轮询和取消；
- [x] 当前会话向量状态的独立计数、筛选和行级徽标；
- [ ] 批量导入后的选中/本批次全部快捷路径；
- [ ] waiting → queued → processing → terminal 的真实后端与真实模型纵向验收；
- 取消与 Worker 领取竞态返回后的界面重新同步；
- 两个用户不能在列表、任务、PDF 内容、IndexedDB 草稿和请求缓存中串数据；
- PDF 大文件不进入 Pinia/Base64，Range 失败有安全降级；
- Markdown 刷新恢复草稿、保存成功、网络失败和 409/412 版本冲突；
- 新 revision 后旧 chunks/embedding 状态不再显示为当前可用；
- `401` 清理 Auth Store 和用户业务缓存，`429/503` 有明确背压提示；
- 默认自动化不调用真实 Embedding、Generation 或远程存储。
