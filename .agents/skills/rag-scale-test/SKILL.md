---
name: rag-scale-test
description: 设计并执行本仓库的负载、压力、长稳、多实例和故障恢复测试，生成可复现的容量与分布式正确性证据。普通单元测试和功能测试不触发。
---

# Outcome

在冻结的环境、负载、拓扑和安全边界内，回答一个明确的规模或可靠性问题，
同时验证性能指标和系统正确性，不把局部结果包装成生产 SLA。

# Inputs

运行前冻结一张 Scale Test Card：

```text
Question / hypothesis:
Base commit:
Mode:
Environment / topology:
Resource limits:
Workload model / fixed dataset:
Changed variable:
Correctness invariants:
Metrics / pass-fail thresholds:
Stop conditions:
Allowed test paths:
External state / cost authorization:
Cleanup and recovery:
```

# Modes

按问题选择 `BASELINE`、`LOAD`、`STRESS`、`SPIKE`、`SOAK`、`DISTRIBUTED` 或 `FAULT`；
不要让一次测试机械覆盖全部模式。

只执行已有的前端检查或后端 L0-L5 门禁不触发本 skill；只有需要设计新的负载、拓扑、
故障假设或受控对照实验时才使用。

# Workflow

1. 读取 Feature Control Card、Contract、适用的性能基线、相关实现和现有测试资产。
2. 固定 commit、环境、拓扑、数据集、请求分布和资源限制；每轮对照只改变一个变量。
3. 先做小规模 dry-run，验证 harness、观测、停止条件和成功/失败后的清理。
4. 分级增加负载、实例或故障，区分 offered、admitted、completed 和 rejected 工作量。
5. 异步任务追踪到正式终态，不能把 `202 Accepted` 当作任务完成。
6. 记录吞吐、P50/P95/P99、queue wait、错误、429/503、CPU、内存、I/O、连接池和资源饱和。
7. 同时断言适用的正确性不变量：无重复领取/写回、stale Worker 被 fencing 拒绝、
   只恢复过期租约、Owner 隔离与公平、多实例容量不放大、降级安全且无资源残留。
8. 验证测试数据、进程、容器、临时文件和外部对象已经按计划清理。
9. 与冻结基线比较，输出 `PASS`、`FAIL`、`INCONCLUSIVE` 或 `ENVIRONMENT_BLOCKED`，
   并明确当前环境、样本和测试不能证明什么。

# Failure classification

按证据将失败分类为 `CORRECTNESS_DEFECT`、`PERFORMANCE_REGRESSION`、`CAPACITY_LIMIT`、
`HARNESS_DEFECT`、`BASELINE_DRIFT` 或 `EXTERNAL_DEPENDENCY`。容量极限不自动等同于代码缺陷。

# Boundary

- 只修改任务明确批准的测试 harness、场景、fixture 和报告；生产代码与 Contract 默认只读。
- 测试工具、依赖、Docker/Compose、CI、共享 fixture、质量脚本和正式性能文档属于保护面。
- 缺少指标、故障注入点或测试 seam 时，提出准确需求并交回对应开发 skill，不顺手修改生产代码。
- 正确性缺陷或性能回归交回对应开发 skill；阈值、环境、范围或 Contract 不明确时退回
  `rag-feature-flow`。不得在本 skill 下修复生产代码。
- 默认使用隔离环境、一次性数据库、唯一 run marker、固定合成数据和 Fake Provider。
- 不自动访问生产、真实凭据、付费服务或未批准外部系统。远程或高资源测试必须具有资源、
  时长、费用、停止和清理边界。

# Output

报告环境与拓扑、负载和数据集、唯一变量、指标与原始证据、正确性不变量、失败分类、
清理结果、局限和下一次交接。跨对话任务需要持久化时，只写
`chatgpt/工作流/<task-id>/scale-test-handoff.md`，不修改 `control.md`。
