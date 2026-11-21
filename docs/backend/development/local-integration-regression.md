# 后端本地集成回归

## 1. 核心目的

默认回归使用 fake、内存对象和本机假 HTTP 服务，适合每次提交前快速运行。本地集成回归进一步验证默认测试
无法证明的真实边界：

- 嵌入式 SQL migrations 能否在 PostgreSQL 从零执行；
- Repository 的 SQL、事务、锁、分页和 pgvector 是否真实可用；
- Application Worker 能否使用真实 Repository 完成状态收尾；
- 100 个 Owner 的 200 条异步问答能否在真实 PostgreSQL 和 10 个 Worker 下恰好执行一次并遵守并发边界；
- Go 能否真正启动 Python 进程并通过标准输入输出交换 JSON；
- HTTP、PDF、文本块和 PostgreSQL 能否组成纵向链路。

该套件不调用 DashScope 或 OpenAI，模型费用仍为零。

## 2. 为什么使用一次性数据库

部分测试使用临时 schema，部分纵向测试会在连接的数据库中创建表和短期记录。仅依赖每个测试自己删除记录，
仍可能在进程崩溃或清理代码出错时污染正式 `rag_platform`。

集成脚本因此增加数据库级沙盒：

```text
正式 rag_platform（只读取连接配置，不作为测试目标）

创建 rag_integration_<时间>_<随机值>
        ↓
执行 migrations / Repository / Worker / Go-Python / 跨层测试
        ↓
finally 强制断开测试连接并 DROP 临时数据库
        ↓
查询 pg_database，确认临时数据库已经不存在
```

测试即使在第三或第四阶段失败，也会进入 `finally`。这里的收尾不是“释放一点内存”，而是恢复外部资源状态：
删除测试数据库、恢复环境变量并写出失败报告。

## 3. 执行命令

前置条件：

- Docker Desktop 正常运行；
- Compose PostgreSQL 已经启动且状态为 `healthy`；
- Go、Python 3.11 和 `ai/pyproject.toml` 依赖已经安装；
- 当前 PostgreSQL 用户具有创建测试数据库的权限。

从项目根目录执行：

```powershell
powershell.exe -NoProfile -ExecutionPolicy Bypass `
    -File .\scripts\quality\run-backend-local-integration.ps1
```

如果 Python 命令不是 `python`，可以显式指定：

```powershell
powershell.exe -NoProfile -ExecutionPolicy Bypass `
    -File .\scripts\quality\run-backend-local-integration.ps1 `
    -PythonExecutable "E:\dev\python\python3.11.1\python.exe"
```

## 4. 五个可定位阶段

| 阶段 | 主要架构边界 | 验证内容 |
| --- | --- | --- |
| `database_migrations` | migrations → PostgreSQL | 从零迁移、重复执行、扩展、索引与校验和 |
| `postgres_repositories` | Infrastructure → PostgreSQL | 文档、任务、chunks、搜索、向量及事务 |
| `document_worker_database` | Application → Repository | Worker 领取、成功/失败收尾和文档状态 |
| `go_python_process` | Go Infrastructure → Python CLI | 子进程、stdin/stdout JSON、PDF 页码和错误契约 |
| `cross_stack_document_flow` | HTTP/Application/Infrastructure | 文本块 HTTP、PDF 解析、文件存储、异步问答并发和数据库落库 |

Go 使用 `-p 1` 顺序运行相关包，避免个人开发环境 `max_connections=20` 时并行测试争用连接，也避免共享
PostgreSQL 扩展的创建/清理互相干扰。

## 5. 安全边界

- 数据库用户名、密码和映射端口从正在运行的 PostgreSQL 容器读取，不打印密码；
- 测试环境的 `DB_NAME` 固定指向脚本生成的 `rag_integration_*`，不是正式数据库；
- 删除前再次校验数据库名称前缀，拒绝删除名字不符合约定的数据库；
- 测试文件使用 Go `t.TempDir()`，不写入正式 `storage/`；
- Embedding Worker、在线语义检索和问答均关闭，API Key 被不可用占位值覆盖；
- 环境变量在脚本结束后恢复；
- PostgreSQL 容器和正式数据卷不会被停止或删除。

如果 PowerShell 进程被强制杀死或电脑断电，`finally` 没有机会执行，可能留下 `rag_integration_*`。先只读查询：

```powershell
docker compose exec postgres psql `
    -U rag_user `
    -d postgres `
    -c "SELECT datname FROM pg_database WHERE datname LIKE 'rag_integration_%';"
```

确认确实是测试数据库后再单独规划清理，不要让脚本猜测性删除历史数据库。

## 6. 报告与结果含义

报告保存在 Git 已忽略目录：

```text
chatgpt/运行产物/回归/backend-local-integration-<UTC 时间>.json
```

报告包含每阶段状态与耗时、临时数据库名称、清理状态和远程 AI 关闭状态，不记录密码、Key 或文档正文。

本地集成通过仍不等于真实复杂文献质量或远程供应商可用：测试 PDF 是程序生成的两页数字文本 PDF；复杂双栏、
扫描件、公式和乱码继续由真实 PDF 质量验收负责。远程模型验收必须单独获得授权。

异步问答并发回归只用 Fake 替代会产生费用的检索/生成执行器；用户、`answer_jobs`、Owner 调度游标、
`FOR UPDATE SKIP LOCKED` 领取、WorkerLoop、WorkerPool、成功事务和启动恢复全部使用生产实现。固定场景为
100 个用户、每人 2 条任务、10 个 Worker，要求 200 个唯一问题都恰好执行一次，全局并发不超过 10、单用户
不超过 2。另一个场景会在执行中取消 Worker context，并验证任务保持 `processing`、随后由启动恢复转回
`queued`。这证明的是本地调度正确性，不代表远程模型在 100 用户下具有相同吞吐或延迟。

## 7. 2026-08-15 首次验收

- 五个阶段全部通过，总耗时 13,497 ms；
- Repository、Worker、Go/Python 和 PDF/PostgreSQL 纵向测试均使用真实依赖；
- 临时数据库 `rag_integration_20260815092452_9abbcc55` 已删除；
- 正式库前后均为 46 documents、45 document_jobs、2729 text_chunks、8 embedding_jobs 和
  460 chunk_embeddings；
- 故意让 Python 阶段失败后，失败报告正确定位 `go_python_process`，对应临时数据库仍成功删除；
- 远程 AI 调用为零，后端容器没有启动。

成功报告：

```text
chatgpt/运行产物/回归/backend-local-integration-20260815T092452Z.json
```

准备发布候选时不需要手工分别记住两个命令，可以使用
[后端发布验收与 P5 收尾](release-acceptance.md) 中的聚合入口顺序执行默认回归与本套件。
