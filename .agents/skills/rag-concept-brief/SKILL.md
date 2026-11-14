---
name: rag-concept-brief
description: 解释并验证会影响当前开发决策的核心概念，将其映射到仓库证据、关键不变量和最小实验，并判断能否继续开发。普通语法问题和无关通用教程不触发。
---

# Outcome

解决一个与当前开发决策直接相关的知识缺口，建立足以继续开发的正确心智模型，
而不是提供通用教程或为每个新名词暂停开发。

# Trigger

用于用户明确要求先理解概念，或未经验证的机制会影响架构、Contract、安全、并发、
迁移、不可逆数据或外部系统的情况。普通语法、局部 API 用法、已有明确实现模式的改动不触发。

# Evidence order

按以下顺序建立结论：当前代码和测试、冻结 Contract、项目正式文档、当前依赖版本的官方文档或源码、
明确标记的推断。计划和路线图不能证明能力已经实现。

# Workflow

1. 明确该概念正在影响的功能、假设或决策。
2. 检查直接相关的代码、测试、Contract 和适用文档。
3. 区分已验证事实、证据支持的推断和未验证方案。
4. 用一句话模型、项目调用路径和少数关键不变量解释概念。
5. 指出它能解决什么、不能保证什么、常见误解和主要失败方式。
6. 将“应该可以”等模糊判断改写成可观察命题，并提供最小、安全、可逆的验证。
7. 给出 `READY`、`VERIFY`、`DECIDE` 或 `BLOCKED` 结论。

# Concept Card

保持一屏左右，按需要包含：

```text
Concept:
One-line model:
Why it matters now:
What it solves / does not solve:
Project mapping:
Key invariants:
Common false assumption:
Failure modes:
Evidence:
Smallest verification:
Development verdict:
Impact on scope/Contract:
```

- `READY`：关键行为已有足够证据，可以继续。
- `VERIFY`：存在可逆不确定性，先执行指定的隔离实验。
- `DECIDE`：技术事实已清楚，但仍需产品、Contract 或架构取舍。
- `BLOCKED`：缺少资料，或验证需要额外权限、外部状态或费用。

# Boundary

- 默认只读，不修改业务代码、Contract 或任务范围。
- 可以运行无副作用的本地检查或隔离临时实验。
- 不访问生产系统、真实凭据、外部写入或付费服务。
- 不要求用户为普通概念复述或接受测验。
- 需要产品取舍、范围扩张、正式代码改动或额外授权时，输出证据并退回 `rag-feature-flow`。

# Output

默认在对话中返回 Concept Card。跨对话任务需要持久化时，将卡片写入
`chatgpt/工作流/<task-id>/concepts.md`，不修改 `control.md`。长期项目事实是否进入正式文档由主线决定。
