# 后端发布验收与 P5 收尾

## 1. 这项工作的核心目的

P5 收尾不再增加业务接口，而是回答三个工程问题：

1. 一次普通提交至少要检查什么；
2. 准备发布时，哪些真实依赖必须重新验证；
3. 哪些操作会改变外部状态或产生费用，不能隐藏在默认命令里。

这里新增的是已有回归积木的组装层，不重新实现测试、数据库或业务逻辑。

## 2. 分级验收地图

| 级别 | 入口 | 验证边界 | 外部状态 | 远程费用 | 推荐时机 |
| --- | --- | --- | --- | --- | --- |
| L0 默认回归 | `run-backend-regression.ps1` | Go 格式、测试、Vet、Python 单元与 CLI 契约、Compose 配置 | 不访问数据库，不启动容器 | 无 | 每次后端提交前 |
| L1 本地集成 | `run-backend-local-integration.ps1` | migrations、Repository、Worker、Go/Python、HTTP/PDF/chunks | 创建并删除一次性数据库 | 无 | 修改 SQL、Worker 或进程契约后 |
| L2 发布候选 | `run-backend-release-acceptance.ps1` | 顺序执行 L0 与 L1，形成聚合报告 | 与 L1 相同 | 无 | 合并发布候选前 |
| L3 容器生命周期 | 发布候选命令增加 `-IncludeContainerLifecycle` | 镜像构建、健康检查、SIGTERM、SIGKILL 和任务恢复 | 短暂创建后端容器及测试记录，结束后清理 | 无 | 修改 Dockerfile、Compose、启动或关闭逻辑后 |
| L4 真实质量/供应商 | 人工选择样本和已有专项计划 | 复杂 PDF 视觉质量、真实 Embedding/Generation 供应商 | 可能写入业务库或调用第三方 | 可能收费 | 需求明确且获得授权后 |

前端拥有独立 F 阶段，本验收矩阵不读取、修改或构建前端源码。

## 3. 发布候选命令

从项目根目录执行：

```powershell
powershell.exe -NoProfile -ExecutionPolicy Bypass `
    -File .\scripts\quality\run-backend-release-acceptance.ps1
```

它会先执行默认回归，再创建一次性 PostgreSQL 数据库执行真实集成测试。任一步失败都会返回非零退出码，
后续步骤停止；子套件仍负责自己的环境恢复和数据库清理。

如果 Python 不在默认 PATH：

```powershell
powershell.exe -NoProfile -ExecutionPolicy Bypass `
    -File .\scripts\quality\run-backend-release-acceptance.ps1 `
    -PythonExecutable "E:\dev\python\python3.11.1\python.exe"
```

聚合报告保存在 Git 已忽略目录：

```text
chatgpt/运行产物/回归/backend-release-<UTC 时间>.json
```

报告只记录阶段、状态、耗时和安全开关，不记录密码、API Key、文档正文或远程响应。

## 4. 为什么容器验收不是默认步骤

容器生命周期测试会重新构建镜像、启动后端容器、插入专用恢复记录，并模拟正常退出和强制终止。虽然脚本会
关闭远程能力并清理测试记录和容器，但它比普通回归更慢，且会改变 Docker 和数据库的短期状态。

只有部署相关代码变化时才显式执行：

```powershell
powershell.exe -NoProfile -ExecutionPolicy Bypass `
    -File .\scripts\quality\run-backend-release-acceptance.ps1 `
    -IncludeContainerLifecycle
```

运行前必须保证没有需要保留的 `backend` Compose 容器，默认使用宿主机端口 `18080`。需要更换端口时增加
`-ContainerHostPort`。

## 5. 为什么真实 PDF 和远程模型不自动化

真实 PDF 可能包含个人路径、版权文献、密码、扫描页、双栏、公式和异常字体。把任意外部文件自动上传到正式库，
会留下文档、chunks、向量和文件存储，不能作为无副作用的发布门禁。当前自动套件只使用程序生成的数字文本 PDF；
复杂样本按照 [PDF 处理路线图](../architecture/pdf-processing-roadmap.md) 单独登记文档、页码、chunk 和页面对照。

真实 Embedding 与 Generation 会使用第三方 Key、网络和账户额度，供应商故障也不等于本地代码回归。因此它们
继续依据 [语义检索路线图](../architecture/semantic-search-roadmap.md)、
[RAG 问答路线图](../architecture/rag-answer-roadmap.md) 和
[回答质量评估计划](../evaluation/rag-answer-quality-evaluation-plan.md) 单独验收，必须获得明确授权。

## 6. P5 完成边界

P5 完成表示个人版已经具备以下工程基线：

- 路径和配置不依赖偶然的当前工作目录；
- 后端可以本机运行，也可以通过 Compose 构建和启动；
- 数据库与文件能够配套备份、隔离恢复并验证一致性；
- 正常停机、异常退出和遗留任务恢复具有稳定规则；
- HTTP、后台任务和远程模型调用具有结构化日志与请求关联；
- 默认回归、真实本地集成和部署验收具有明确入口；
- 默认和发布候选回归不需要真实模型 Key，也不产生模型费用。

P5 完成不表示系统已经适合公开多人使用。身份、工作区、权限和租户数据隔离属于后续独立的 P6；复杂 PDF、
OCR、公式与表格还原属于文档质量增量路线，不阻塞个人版工程化基线收尾。

## 7. 2026-08-15 首次验收

首次运行在进入第一阶段前发现 PowerShell 会拒绝把空的 `List[object]` 绑定到 `Results` 参数。该问题没有启动
测试、访问数据库或改变外部状态。为参数增加 `[AllowEmptyCollection()]` 后重新运行，结果如下：

- 默认回归通过，耗时 8,201 ms；
- 一次性数据库本地集成通过，耗时 13,627 ms；
- 聚合门禁总耗时 21,977 ms；
- 正式库仍为 46 documents、45 document_jobs、2729 text_chunks、8 embedding_jobs 和
  460 chunk_embeddings；
- 没有残留 `rag_integration_*` 数据库；
- 容器生命周期未重复执行，远程 AI 调用为零。

随后把 `PythonExecutable` 故意指定为 `go`：默认回归通过，本地集成准确停在 `go_python_process`，发布候选
脚本向调用方返回退出码 `1`，对应一次性数据库仍被删除。这证明聚合层不会把子套件失败误报为成功。

聚合报告：

```text
chatgpt/运行产物/回归/backend-release-20260815T101915Z.json
```
