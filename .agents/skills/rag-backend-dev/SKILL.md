---
name: rag-backend-dev
description: 在已批准范围和契约内实现、修复或重构本仓库的 Go/Python 后端切片，并按变更风险提供验证证据。不得修改前端或静默改变共享契约。
---

# Outcome

交付满足验收标准的最小后端切片，不静默扩大范围、改变共享契约或触发未经授权的外部状态。

# Inputs

开始前确认目标、非目标、任务基线、允许路径、验收标准、保护面以及外部状态和费用边界。
涉及 HTTP、前后端协作或 Go/Python 协议时必须具有冻结 Contract。

# Route

将任务分类为 Go、Python、数据库、共享协议或外部/部署变更，只读取和验证适用的边界；
不要让一个功能机械经过所有路由。

# Workflow

1. 读取适用规则、Contract、附近实现、测试和当前路由需要的文档。
2. 检查当前行为、工作树、用户已有改动和允许路径。
3. 明确需要保持的不变量、失败路径、兼容边界和恢复要求。
4. 复用现有分层，实现满足验收标准的最小切片：Domain 保存业务规则，Application 编排用例，
   API 处理传输和错误映射，Infrastructure 实现数据库、文件、进程及供应商适配。
5. Python 保持自身的 domain/application/contracts/entrypoints/infrastructure 边界。
6. 在变更发生的边界补充测试；先运行聚焦测试，再按风险选择仓库现有后端验收等级。
7. 对照任务基线和允许路径检查实际 diff，报告证据和未运行检查。

# Project invariants

- Go 持有业务事实、数据库、任务状态和进程生命周期。
- Python 只处理 Go 授权的输入并返回结构化结果；不连接 PostgreSQL、不修改任务状态、
  不调用 Go HTTP API，也不修改原始上传文件。
- HTTP 公开语义以 `docs/shared/api/` 为准。
- Go/Python 协议以 `contracts/document-processing/` 为准；不兼容变化必须重新冻结 Contract 并升级版本。
- 不修改已提交或已应用的历史 migration；schema 演进使用新增 migration。只有能够证明旧 migration
  从未发布或应用，并由主线明确批准时，才允许例外。
- 真实供应商、OSS、生产数据和可能收费的验证永不隐式执行。

# Boundary

后端责任域上限是 `backend/**`、`ai/**` 和 `docs/backend/**`，实际写权限仅限任务允许路径。
`web/**`、`docs/frontend/**`、`docs/shared/**`、`contracts/**` 和根级部署/质量文件默认只读。

迁移、依赖文件、HTTP 公共语义、身份与 OwnerScope、隐私、锁/租约/fencing/幂等、任务状态机、
文件存储与路径安全、外部系统及部署属于保护面，必须明确列入范围。

需要改变 Contract、修改范围外文件、执行不可逆数据操作或访问未批准外部状态时，立即停止写入，
报告证据、准确路径、消费者、替代方案、兼容/部署顺序、回滚和验证建议，并退回 `rag-feature-flow`。

# Verification routing

- 任意修改：受影响 package 或模块的聚焦测试。
- 普通后端交付：L0 默认零远程费用回归。
- Migration、Repository、Worker 或 Go/Python 协议：L1 一次性数据库集成。
- 发布候选：由主线决定 L2。
- 容器生命周期：相关变更才运行 L3。
- OSS 和真实供应商：仅明确授权后运行 L4/L5。
- 负载、长稳、多实例、分布式正确性或故障注入：由主线冻结 Scale Test Card，交给
  `rag-scale-test` 独立执行；本 skill 不把一次普通回归包装成容量结论。

工具或环境不可用时如实记录，不能把计划命令写成通过证据。

# Output

报告变更路由、可观察行为、保持的不变量、修改路径、迁移与兼容说明、验证证据、
未运行检查、Contract 偏差和下一次交接。跨对话任务需要持久化时，只写
`chatgpt/工作流/<task-id>/backend-handoff.md`，不修改 `control.md`。
