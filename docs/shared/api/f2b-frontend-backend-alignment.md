# F2-B 前后端待对齐事项

> 记录日期：2026-08-18。本文记录 F2-B 前端实现后发现的恢复与并发边界，不把候选方案写成已冻结接口。

## 1. 当前已经可以直接消费的接口

- `GET /documents/:id`：文档详情；
- `POST /documents/:id/process`：创建任务并返回 job ID；
- `GET /processing-jobs/:id`：按已知 job ID 查询；
- `GET /documents/:id/chunks`：ready 后分页读取文本块；
- `DELETE /documents/:id`：删除文档及关联数据。

F2-B 前端已经基于这些接口完成详情、显式开始/重试、job 轮询、chunks 和删除二次确认。

## 2. `CONTRACT-F2B-003`：刷新后恢复活动任务

### 现象

任务创建后，前端当前页面持有 job ID，可以完整轮询。若刷新发生在 Worker 领取前，job 是 `queued`，但文档按当前
设计仍保持 `uploaded/failed`；浏览器丢失 job ID 后不能调用 `/processing-jobs/:id` 恢复该任务。

### 当前降级行为

用户再次点击开始解析时，后端活动任务唯一约束返回 `409`。前端随后轮询文档详情，最终可以观察到
`processing → ready/failed`，但无法展示 queued、job ID、尝试次数或任务错误；如果 Worker 长时间未领取，
文档仍像“等待解析”。

### 建议后端决策

评估增加 OwnerScope 保护的“按 document ID 查询活动或最近 processing job”能力。路径和响应尚未冻结；无论采用
独立接口还是详情内嵌，都需保持：

- 他人文档和不存在文档统一 `404`；
- 没有任务时的结果语义稳定（例如 `404` 或 `200 + null` 二选一并文档化）；
- 活动任务优先级和“最近任务”的排序明确；
- Handler 测试覆盖 queued、processing、terminal、跨用户与无任务场景。

前端在接口冻结前继续保留现有降级恢复，不猜测任务 ID。

## 3. processing 期间的删除语义

当前删除应用服务没有按文档/job 状态拒绝删除。F2-B 前端在已知 queued/processing、文档 processing 或未知任务恢复期间
禁用删除，避免 Worker 与文件删除并发；但客户端限制不能代替服务端并发规则。

请后端选择并冻结其中一种语义：

1. 允许删除活动任务文档，并保证 job 取消/级联、Worker 领取或正在处理时的文件与事务行为可重复；
2. 活动任务期间拒绝删除，返回稳定 `409` 和错误 code，待终态后再删除。

前端当前采用保守的第二种产品表现，但不宣称后端已经返回该冲突。

## 4. 错误 code 一致性

详情和 processing job 查询已有稳定错误 code；创建解析、chunks 和删除的部分 `400/404/409` 目前只有 error 文本。
F2-B 只依据 HTTP 状态做宽泛中文提示，不依赖英文错误文本。后端后续若增加稳定 code，前端可以进一步区分
“已有活动任务”和“文档状态不可处理”等场景。

## 5. 不阻塞与阻塞范围

- 不阻塞：当前页面内创建任务、轮询终态、ready 后 chunks 和非活动状态删除；
- 影响体验：queued 阶段刷新恢复和任务详情可见性；
- 影响最终并发声明：processing 期间删除的服务端保证；
- 不属于本文：ready 到 AI-ready 的向量化产品流程，继续由 `FLOW-P4-001` 跟踪。
