# 后端默认零远程费用回归

## 1. 核心目的

默认回归用于回答一个具体问题：**本次代码修改是否破坏了已经能够工作的后端能力？**

它不是新的业务接口，也不重新实现测试逻辑。PowerShell 脚本只是统一编排已有工具，并在任一步骤失败后返回
非零退出码，使开发者或未来 CI 能够阻止不合格代码继续提交或发布。

## 2. 一键命令

首次运行前，需要已经安装 Go、Python 3.11、Docker CLI 与 AI 项目依赖。从项目根目录执行：

```powershell
powershell.exe -NoProfile -ExecutionPolicy Bypass `
    -File .\scripts\quality\run-backend-regression.ps1
```

脚本根据自身路径定位项目根目录，不依赖调用者当前位于 `backend` 还是其他目录。成功时进程退出码为 `0`；
任一步失败时退出码非零，并在终端指出失败阶段。

## 3. 默认执行的五个阶段

| 阶段 | 实际工具 | 检查内容 |
| --- | --- | --- |
| `go_format` | `gofmt -l` | 检查所有 Go 文件格式，不修改源码 |
| `go_test` | `go test -count=1 ./...` | 禁用测试缓存，运行默认 Go 测试 |
| `go_vet` | `go vet ./...` | 检查常见静态错误和可疑写法 |
| `python_test` | `python -m unittest discover -s tests -v` | 运行 Python 领域、应用、解析器和 CLI 契约测试 |
| `compose_config` | `docker compose config --quiet` | 只解析 Compose，不构建镜像、不启动容器 |

Go 文件通过 `.gitattributes` 固定为 LF，避免 Windows Git 的 CRLF 检出被 `gofmt` 误判为整个后端未格式化。

## 4. 为什么不会产生远程费用

脚本在自己的进程中保存并覆盖以下环境变量：

- `RUN_DATABASE_TESTS=0`；
- `RUN_PYTHON_TESTS=0`；
- `EMBEDDING_WORKER_ENABLED=false`；
- `SEMANTIC_SEARCH_ENABLED=false`；
- `ANSWER_ENABLED=false`；
- 两种远程 API Key 与数据库密码使用明确不可用的占位值。

脚本结束后，无论成功或失败，都会恢复调用者原来的进程环境。即使外层终端误留了数据库测试开关、真实 Key
或远程能力开关，默认回归仍不会连接真实 PostgreSQL、启动后端 Worker 或请求模型供应商。

远程客户端的默认单元测试使用 `httptest` 本机假 HTTP 服务模拟成功、超时、限流和错误响应；这验证的是适配器
如何处理协议，不是对供应商执行真实请求。

## 5. 结果报告

每次运行都会在 Git 已忽略的位置生成 JSON：

```text
chatgpt/运行产物/回归/backend-default-<UTC 时间>.json
```

报告记录：

- 套件名称、总体状态和开始/结束时间；
- 总耗时及每个阶段耗时；
- 每个阶段的 `passed` 或 `failed`；
- `remote_ai_enabled=false`、`database_tests_enabled=false` 等安全边界。

报告不记录 `.env` 内容、API Key、数据库密码、文档正文或远程响应。

## 6. “默认通过”不代表什么

默认套件追求快速、稳定和可重复，因此通过结果**不能证明**以下能力已经被本次修改重新验收：

- PostgreSQL 迁移和 Repository 的真实 SQL；
- Go 启动 Python 子进程的真实跨进程链路；
- 真实 PDF 的排版与文本提取质量；
- Docker 镜像重新构建与容器生命周期；
- DashScope/OpenAI 的真实 Embedding 或 Generation；
- 前端页面和浏览器联调。

这些能力使用显式分级验收，不应偷偷塞进默认入口：

| 套件 | 是否本地 | 是否可能收费 | 运行时机 |
| --- | --- | --- | --- |
| 默认回归 | 是 | 否 | 每次提交前 |
| PostgreSQL/Go-Python 集成 | 是 | 否 | 修改迁移、Repository 或进程契约后 |
| 真实 PDF/容器验收 | 是 | 否 | 修改解析器或部署配置后 |
| 远程模型验收 | 否 | 是 | 明确授权且需要验证供应商链路时 |

前端拥有独立 F 阶段和回归入口；后端 P5.4 不修改或捆绑前端源码。

PostgreSQL 与 Go/Python 集成套件已经在 P5.4.2 落地，具体的一次性数据库隔离、命令和验收结果见
[后端本地集成回归](local-integration-regression.md)。
发布候选的聚合命令、容器验收以及真实 PDF/远程供应商边界见
[后端发布验收与 P5 收尾](release-acceptance.md)。

## 7. 2026-08-15 首次验收

首次验收故意在外层进程设置数据库测试、Python 集成和三个远程能力开关为启用，并放入不可使用的 Key，
用于证明脚本会执行安全覆盖。最终五个阶段全部通过：

- Go 默认测试全部通过；
- `go vet` 通过；
- Python 39 项测试通过；
- Compose 配置通过且没有启动容器；
- 总耗时 8663 ms；
- PostgreSQL 未被默认回归访问，远程模型调用为零。

对应结果：

```text
chatgpt/运行产物/回归/backend-default-20260815T084856Z.json
```
