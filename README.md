# 轻量文档知识中台

一个面向学生、研究者和小团队的轻量文档知识系统。项目以 Go 构建稳定的业务后端，以 Python 承担 PDF 解析、文本分块和后续 AI 能力，优先完成可运行、可测试、可解释的后端主链路。

> 当前状态：P2（异步解析链路，进行中）。文档上传、详情、分页列表和删除接口已经实现并验证；PostgreSQL 自动迁移、解析任务表、任务排队和任务状态查询接口已经完成，后台 Worker 与 Python 解析尚未实现。

## 项目目标

第一版将支持：

- 本地 PDF 上传与文件校验
- 文档列表、详情和删除
- 后台解析任务与状态追踪
- 解析失败原因记录和手动重试
- 文本分块与 PostgreSQL 持久化
- 关键词检索、分页和基础过滤
- 简单的原生 Web 页面
- 核心接口与任务状态测试

项目强调低资源占用：文件流式处理、worker 默认单并发、Python 按需启动、AI 能力可关闭且失败可降级。

## 技术架构

| 模块 | 技术 | 主要职责 |
| --- | --- | --- |
| 业务后端 | Go + Gin + pgx | REST API、参数校验、业务编排、任务状态、数据库访问和文件管理 |
| AI 与文档处理 | Python | PDF 解析、文本分块，以及后期的向量化、语义检索、摘要和问答 |
| 数据库 | PostgreSQL | 文档元数据、任务、文本块和检索数据 |
| 前端 | HTML/CSS/JavaScript | 上传、列表、状态展示和搜索 |

### Go 与 Python 如何协作

第一阶段不让 Python 作为常驻服务运行。Go 创建后台任务后，按需启动 Python 子进程；双方通过标准输入、标准输出和 JSON 交换数据。Go 负责超时、错误处理、状态更新和结果入库，Python 完成计算后退出并释放内存。

当功能和负载证明有独立部署的需要时，再考虑把 Python 改造成 HTTP AI 服务。普通文档管理和关键词检索不依赖 AI 服务，Python 或模型不可用时，Go 后端仍应提供基础能力。

```text
浏览器
  │ HTTP
  ▼
Go API ── PostgreSQL
  │
  │ 按需启动 + JSON
  ▼
Python 文档/AI 任务
```

## 目录结构

```text
rag_reasoning_platform_individual/
├─ backend/                     # Go 业务后端
│  ├─ cmd/server/               # 服务启动入口
│  ├─ internal/
│  │  ├─ api/                   # 路由、HTTP 请求与响应
│  │  ├─ application/           # 业务用例和流程编排
│  │  ├─ config/                # 配置加载与校验
│  │  ├─ domain/                # 核心模型和业务规则
│  │  └─ infrastructure/        # 数据库、文件系统等外部实现
│  └─ migrations/               # PostgreSQL 数据库迁移
├─ ai/                          # Python 文档处理与 AI 能力
│  ├─ src/rag_ai/
│  │  ├─ parsing/               # PDF 解析
│  │  ├─ chunking/              # 文本分块
│  │  ├─ embedding/             # 向量化（后期）
│  │  ├─ retrieval/             # 语义检索（后期）
│  │  └─ generation/            # 摘要与问答（后期）
│  └─ tests/                    # Python 测试
├─ contracts/                   # Go 与 Python 的 JSON 数据契约
├─ web/                         # 原生前端
├─ storage/                     # 本地运行数据，不提交 Git
├─ chatgpt/                     # 对话和非项目资料，不提交 Git
├─ .gitignore
└─ README.md
```

`chatgpt/` 和 `storage/` 是仅供本机使用的目录，已被 Git 忽略。克隆仓库后如果需要，可手动创建它们。

## MVP 边界

### 第一版要做

- 健康检查接口
- PDF 流式上传和大小、类型校验
- 文档增删查
- `uploaded`、`processing`、`ready`、`failed` 状态流转
- 单并发后台解析、超时、失败记录和手动重试
- 文本分块入库
- 关键词检索、分页与过滤
- 核心接口和任务状态测试
- 本地启动说明与 Docker Compose 部署

### 第一版不做

- 多 Agent 协作和知识图谱
- 常驻本地大模型
- 微服务拆分和 Kubernetes
- Redis、Elasticsearch 或消息队列集群
- 复杂组织权限和商业化计费

## 开发原则

1. 每次只推进一个可以实际验证的小任务。
2. Go 负责业务事实和任务状态，Python 不直接修改数据库。
3. 文件采用流式处理，避免一次性读入大文件。
4. worker 默认并发数为 1，并设置文件大小和任务超时限制。
5. AI、向量化和 PDF 解析必须可关闭、可超时、可失败降级。
6. 功能完成必须有测试或实际请求作为验证证据。
7. README 只描述已经完成或明确标记为计划的能力，不夸大完成度。

## 开发路线

- **P0：范围与仓库**——目录、Git、忽略规则、配置示例和基础说明。
- **P1：最小后端骨架**——Go 模块、`/health`、PostgreSQL 和文档基础接口。
- **P2：异步解析链路**——任务表、单并发 worker、Python 解析、失败重试与幂等。
- **P3：检索**——关键词/全文检索、分页、过滤、排序和测量。
- **P4：AI 增强**——向量检索、带来源引用的摘要或问答，以及失败降级。
- **P5：工程化**——测试、日志、配置校验、部署、性能记录和求职材料。

## 本地开发状态

Go 后端已经可以运行，目前提供 `GET /health` 健康检查接口。该接口已通过真实 HTTP 请求和 Go 自动化测试验证。

PostgreSQL 开发容器已通过 Docker Compose 启动，使用本机 `5433` 端口、256 MiB 内存上限和独立数据卷，并已通过健康检查与真实 SQL 验证。

Go 后端已使用 pgx 连接池连接 PostgreSQL，启动时会在 5 秒超时内执行真实 Ping；连接失败时 HTTP 服务不会启动。

SQL 迁移已经建立 `documents` 和 `document_jobs` 表。迁移文件通过 `go:embed` 编译进后端，服务启动时会按数字版本自动执行；执行器使用 PostgreSQL advisory lock、独立事务和 SHA-256 校验，能够防止并发迁移以及历史 SQL 被静默修改。单元测试和真实 PostgreSQL 隔离 schema 测试均已通过。

文档领域模型已定义与数据库字段对应的 Go 结构体、强类型处理状态和合法状态流转，并已通过表驱动测试验证。

PostgreSQL 文档仓储已实现 `Create`、`GetByID`、分页 `List` 和 `Delete`。真实数据库集成测试已验证插入、按 ID 查询、领域级 `ErrNotFound` 转换、总数统计、稳定倒序、`LIMIT/OFFSET` 分页、空页结果、删除、重复删除的 `ErrNotFound` 转换和测试数据清理。

文档应用服务已实现 `GetByID`、分页 `List` 和 `Delete` 用例，负责校验文档 ID 与分页参数、把 `page/page_size` 转换为 `limit/offset`、计算总页数，以及按“查询记录、删除文件、删除数据库记录”的顺序编排删除流程；相关单元测试和 Go 全量测试均已通过。

`GET /documents/:id` 已接入 Gin、应用服务和 PostgreSQL 仓储。接口自动化测试已覆盖非法 ID、文档不存在、内部错误和查询成功，并通过真实 PostgreSQL 数据验证了 HTTP `200`、`404`、`400` 响应；响应不会暴露服务器内部的 `storage_path`。

文档上传应用服务已实现文件保存与数据库记录创建的流程编排，并在数据库写入失败时执行文件补偿删除。本地文件存储已实现 PDF 文件头校验、200 MiB 大小限制、流式写入、SHA-256 计算、临时文件清理、安全路径删除和上下文取消，并通过自动化测试验证。

`POST /documents` 已使用 Gin 的流式 multipart 读取接入上传应用服务，并通过 `http.MaxBytesReader` 限制整个请求体。接口自动化测试已覆盖缺少文件、创建成功、应用错误映射和超大请求体；真实 HTTP 验证已完成文件保存、SHA-256 计算、数据库写入及随后按 ID 查询，成功返回 HTTP `201` 和 `200`。

`GET /documents` 已提供分页列表，默认 `page=1`、`page_size=20`，单页最多 100 条。接口自动化测试已覆盖默认值、自定义参数、非法输入、应用错误映射、内部字段隐藏和空数组响应；真实 HTTP 请求已验证空数据库下的 `200` 分页响应，以及非法页码和超大单页数量的 `400` 响应。

`DELETE /documents/:id` 已完成领域仓储能力、应用服务、PostgreSQL 实现、Gin Handler、路由注册和启动依赖注入。自动化测试覆盖非法 ID、文档不存在、文件删除失败、数据库删除失败、删除顺序以及 HTTP `204`、`400`、`404`、`500` 映射。真实 HTTP 冒烟测试已验证上传返回 `201`、删除前查询返回 `200`、删除返回空响应体的 `204`、删除后查询返回 `404`，并确认数据库记录数归零且物理文件已经删除。

`document_jobs` 已提供 `queued`、`processing`、`succeeded` 和 `failed` 四种任务状态，并通过部分唯一索引保证同一文档同时最多只有一个活动任务。PostgreSQL 仓储集成测试已验证任务创建、重复任务冲突和删除文档后的任务级联清理。

`POST /documents/:id/process` 已实现解析任务排队，成功返回 `202 Accepted`；非法 ID 返回 `400`，文档不存在返回 `404`，文档状态不允许处理或已经存在活动任务时返回 `409`。真实 HTTP 测试已验证首次排队为 `202`、重复排队为 `409`、数据库中只有一个 `queued` 任务，并确认任务排队阶段不会提前把文档标记成 `processing`。

`GET /processing-jobs/:id` 已实现单个解析任务状态查询，返回任务状态、尝试次数、错误信息和各阶段时间。自动化测试已覆盖非法 ID、任务不存在、内部错误和成功响应；真实 HTTP 测试已验证 `200`、`400`、`404`，并确认查询结果与刚创建的 `queued` 任务一致。

Python 项目和后台 Worker 尚未实现。下一步是实现单并发 Worker：领取最早的 queued 任务、更新文档和任务状态、调用 Python 契约，并记录成功或失败结果。

## 配置与安全约定

Go 后端当前支持以下环境变量：

| 变量 | 默认值 | 说明 |
| --- | --- | --- |
| `APP_PORT` | `8080` | Go HTTP 服务监听端口，有效范围为 1 到 65535 |
| `DB_HOST` | `localhost` | Go 连接 PostgreSQL 时使用的主机 |
| `DB_PORT` | `5433` | PostgreSQL 映射到本机的端口 |
| `DB_NAME` | `rag_platform` | 数据库名称 |
| `DB_USER` | `rag_user` | 数据库用户 |
| `DB_PASSWORD` | 无 | 本机私有密码，必须在 `.env` 中设置 |
| `DB_SSLMODE` | `disable` | 本地开发时的 PostgreSQL SSL 模式 |
| `STORAGE_ROOT` | `../storage` | 从 `backend` 目录运行时使用的本地文档存储根目录 |
| `STORAGE_MAX_FILE_SIZE_BYTES` | `209715200` | 单个上传文件允许的最大字节数，即 200 MiB |

`.env.example` 是可以提交到 Git 的配置模板，不得包含密码或真实密钥。`.env` 用于保存本机配置和密钥，已被 Git 忽略。

Docker Compose 会自动读取项目根目录的 `.env`。当前 Go 程序通过 `os.Getenv` 读取操作系统环境变量，不会自动加载 `.env` 文件。

在 PowerShell 中可以这样临时设置 Go 服务端口：

```powershell
$env:APP_PORT = "9090"
go run ./cmd/server
Remove-Item Env:APP_PORT
```

本地 PostgreSQL 常用命令：

```powershell
docker compose up -d postgres
docker compose ps
docker compose stop postgres
```

`docker compose stop postgres` 只停止容器，不会删除数据卷中的数据。

其他安全约定：

- 上传文件和运行数据统一存放在 `storage/`。
- 不提交虚拟环境、缓存、日志、测试覆盖率文件和编译后二进制。
- `go.sum` 和后续采用的 Python 依赖锁文件应提交，以保证依赖可复现。

## License

当前项目用于个人学习和求职作品展示，暂未选择开源许可证。
