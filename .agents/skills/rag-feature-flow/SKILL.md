---
name: rag-feature-flow
description: 编排本仓库跨前后端或架构级功能开发：冻结范围与短契约、分派责任域实现、处理越界变更并汇总验证证据。普通单域编码不触发。
---

# Outcome

维护一张权威的 Feature Control Card，使跨域功能从现状检查推进到验证完成，
同时避免范围、契约和完成状态静默漂移。

# Workflow

1. 读取适用的仓库规则，并只读检查理解当前目标所需的代码、测试和文档。
2. 区分已验证事实、证据支持的推断和待实现方案。
3. 将任务分类为单域、跨域或保护面变更。普通单域实现直接交给对应开发 skill。
4. 如果未经验证的关键概念阻碍安全决策，先进入 `rag-concept-brief`。
5. 记录任务基线、目标、非目标、责任人、允许路径、保护面、验收标准和验证计划。
6. 跨域任务在实现前冻结短 Contract，至少说明接口或状态、成功与失败语义、兼容性、
   双方保证和验收标准。
7. 为前端、后端和按需启用的规模测试生成只包含必要上下文的交接，不代替责任域实现。
8. 实现需要未批准路径或 Contract 变化时，停止写入；只读检查影响、替代方案、
   准确路径、兼容性、回滚和验证，再取得所需决策。
9. 汇总 Contract、实现和验证证据。必需检查未运行、范围未收敛或契约仍漂移时，不得完成。

当验收问题涉及容量、负载下延迟、长稳、多实例、分布式正确性或故障恢复时，才启用
`rag-scale-test`。先冻结测试环境、拓扑、负载、正确性不变量、阈值、停止条件、外部状态和清理方案。

# Feature Control Card

保持控制卡简短：

```text
Status:
Goal / Non-goals:
Base commit:
Change class:
Owners:
Allowed write paths:
Protected surfaces:
Short contract:          # 仅跨域时
Acceptance criteria:
Scale test mode/environment: # 仅系统规模或可靠性问题需要
Verification evidence:
Open decisions:
Next handoff:
```

状态按需要使用 `DISCOVERY`、`LEARNING`、`CONTRACT_DRAFT`、`READY`、
`IMPLEMENTING`、`VERIFYING`、`SCOPE_CHANGE_REQUIRED` 和 `DONE`，不要为了完整而机械经过每个状态。

# Persistence

单次工作可以只在对话中维护控制卡。跨对话或跨日工作使用
`chatgpt/工作流/<task-id>/control.md`；本 skill 是该文件的唯一写入者。
前端、后端、规模测试和概念 skill 将证据写入各自的 handoff 文件，本 skill 读取后更新控制卡。

# Boundary

- 可以跨域只读检查；只写已批准的协调、架构或共享契约产物。
- 默认不实现前端或后端业务代码。
- 全局优化建议作为独立候选记录，不自动扩大当前功能范围。
- 产品取舍、破坏性兼容、安全、不可逆数据和外部状态需要对应的明确决策或授权。

# Output

输出当前控制卡、所需交接或最终整合结论。清楚列出证据、未运行检查、未决问题和下一步。
