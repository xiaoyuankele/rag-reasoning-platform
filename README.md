# 轻量文档知识中台

一个面向学生、研究者和小团队的轻量文档知识系统。项目以 Go 构建稳定的业务后端并直接处理 Markdown/TXT，以 Python 承担 PDF、DOCX 等复杂文档解析和后续 AI 能力，优先完成可运行、可测试、可解释的后端主链路。

> 当前状态：P5（个人版工程化基线）已完成。P0～P4 的文档管理、异步解析、关键词检索、向量生产、语义检索和带来源问答均已形成可运行闭环；P5 已补齐稳定路径、结构化日志、容器部署、配套备份恢复、异常任务恢复及分级自动化回归。P6 后端 B1～B7 及忘记/重置密码已完成：身份闭环、PostgreSQL Session、全链路数据隔离、本地 Mailpit 邮件联调、历史数据显式认领及 `owner_user_id NOT NULL` 硬约束均已通过发布验收。下一步对接前端登录态与个人用户完整产品验收；团队工作区和成员权限留到 P7。混合检索、DOCX、OCR、复杂学术版面及表格公式质量继续作为后续增量能力。

## 项目目标

第一版将支持：

- 本地 PDF、Markdown 和 TXT 上传与文件校验
- 文档列表、详情和删除
- 后台解析任务与状态追踪
- 解析失败原因记录和手动重试
- 文本分块与 PostgreSQL 持久化
- 关键词检索、分页和基础过滤
- 简洁的 Vue Web 工作台
- 核心接口与任务状态测试

项目强调低资源占用：文件流式处理、worker 默认单并发且可配置为有界并发、Python 可选择按任务启动或通过有界进程池复用、AI 能力可关闭且失败可降级。

## 技术架构

| 模块 | 技术 | 主要职责 |
| --- | --- | --- |
| 业务后端 | Go + Gin + pgx | REST API、参数校验、业务编排、任务状态、数据库访问、文件管理以及 Markdown/TXT 规范化分块 |
| AI 与复杂文档处理 | Python | PDF、DOCX 等复杂来源的解析，以及后期的向量化、语义检索、摘要和问答 |
| 数据库 | PostgreSQL | 文档元数据、任务、文本块和检索数据 |
| 前端 | Vue 3 + TypeScript + Vite | 上传、列表、状态展示、检索和带来源问答 |

### Go 与 Python 如何协作

Markdown/TXT 首先由 Go 直接处理，不需要启动 Python。对于 PDF、DOCX 等复杂格式，Go 创建后台任务后调用 Python 子进程，双方通过 UTF-8 标准输入、标准输出和版本化 JSON 交换数据。默认 `oneshot` 模式仍是一份文档一个进程的稳定降级路径；可选 `pool` 模式使用 JSON Lines 让固定数量的 Python 进程连续处理多份文档，并在达到回收上限、超时、崩溃或协议异常后重建。Go 负责超时、进程生命周期、错误处理、状态更新和结果入库，Python 不访问 PostgreSQL。

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
│  │  ├─ infrastructure/        # 数据库、文件系统等外部实现
│  │  └─ maintenance/           # 不进入 HTTP 主链路的一次性升级维护用例
│  └─ migrations/               # PostgreSQL 数据库迁移
├─ ai/                          # Python 文档处理与 AI 能力
│  ├─ src/rag_ai/
│  │  ├─ domain/                # 框架无关模型与稳定处理错误
│  │  ├─ application/           # 文档处理用例与 Protocol 端口
│  │  ├─ contracts/             # Python 侧版本化 JSON DTO 与校验
│  │  ├─ entrypoints/           # CLI 入口、依赖组装和边界转换
│  │  ├─ infrastructure/        # pypdf 与文本切分具体适配器
│  │  ├─ embedding/             # 向量化（后期）
│  │  ├─ retrieval/             # 语义检索（后期）
│  │  └─ generation/            # 摘要与问答（后期）
│  └─ tests/                    # Python 测试
├─ contracts/                   # Go 与 Python 的 JSON 数据契约
├─ web/                         # Vue 3 + TypeScript + Vite 前端
├─ docs/
│  ├─ shared/                   # 前后端共同遵守的正式接口契约
│  ├─ backend/                  # 后端、AI、数据库、评估与性能文档
│  └─ frontend/                 # 前端架构、规范与路线图
├─ storage/                     # 本地运行数据，不提交 Git
├─ chatgpt/                     # 前后端分区的对话学习资料与运行产物，不提交 Git
├─ .gitignore
└─ README.md
```

`chatgpt/` 和 `storage/` 是仅供本机使用的目录，已被 Git 忽略。克隆仓库后如果需要，可手动创建它们。

## MVP 边界

### 第一版要做

- 健康检查接口
- PDF、Markdown 和 TXT 流式上传及大小、类型校验
- 文档增删查
- `uploaded`、`processing`、`ready`、`failed` 状态流转
- 默认单并发、可配置 1～4 的后台解析，支持超时、失败记录和手动重试
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
- **P2：异步解析链路**——任务表、有界 worker、Python 解析、失败重试与幂等。
- **P3：检索**——关键词/全文检索、分页、过滤、排序和测量。
- **P4：AI 增强**——向量检索、带来源引用的摘要或问答，以及失败降级。
- **P5：个人版后端工程化**——运行路径、测试、日志、配置校验、部署、备份和性能/成本记录。
- **P6：个人用户产品化（后端已完成，前端联调中）**——个人账户、PostgreSQL Session、忘记/重置密码、文档归属与全链路私有数据隔离。
- **P7：团队与多租户（未来）**——工作区、成员权限、共享、配额与审计；只有出现团队协作需求后启动。

前端不复用后端的 P 阶段编号，而是沿着独立的 `F0～F5` 路线推进：从 Vue 工程骨架、统一 API Client，
逐步完成文档管理、检索、AI 工作台和前端工程化。P6 将先增加个人登录态，租户切换仍留在 P7。

### 当前里程碑进度（2026-08-17）

| 阶段 | 状态 | 当前结论 |
| --- | --- | --- |
| P0 | 已完成 | 仓库、目录、忽略规则、配置模板和项目说明已经建立 |
| P1 | 已完成 | Go 服务、PostgreSQL、迁移、文档增删查和真实 HTTP 验证已经完成 |
| P2 | 已完成 | 异步任务、Worker、Markdown/TXT、异常恢复、Go/Python 适配器和普通数字 PDF 纵向链路均已通过自动化及真实中英文文献验收 |
| P3 | 已完成 | 关键词检索、分页、文档过滤、标题来源、稳定排序、性能基线、`pg_trgm + GIN` 和真实 HTTP 验收均已完成 |
| P4 | 已完成（第一版） | 向量生产、独立语义检索、带来源问答、回答语言、未就绪门禁、证据多样化和 15 条冻结样本人工质量评估均已完成；复杂表格和证据可回答性问题已保留边界 |
| P5 | 已完成（个人版基线） | 稳定运行路径、可观测性、容器部署、配套备份恢复、任务恢复、默认回归、一次性数据库集成及发布候选分级验收均已完成；不包含用户与租户系统 |
| P6 | 后端 B1～B7 及密码重置已完成 | 身份闭环、忘记/重置密码、全链路用户隔离、本地 Mailpit 注册、46 篇历史文档事务化认领和 `owner_user_id NOT NULL` 已完成；默认回归、一次性 PostgreSQL、真实 Go/Python 及双用户 HTTP 隔离发布验收全部通过。下一步完成前端登录态与个人用户产品闭环 |
| P7 | 未开始 | 团队工作区、成员权限、共享、租户配额和审计尚未设计或实现 |

前端 F0、F1 已完成，F3 关键词检索最小切片已通过真实联调；下一步先接入 P6 后端身份边界，再完成
带登录态的 F2 文档管理和后续检索、问答闭环。

PDF 文献处理的错误分类、页码来源、资源限制、解析库选择和分阶段验收标准见
[PDF 文献处理架构与分阶段路线](docs/backend/architecture/pdf-processing-roadmap.md)。

文献标题自动提取、落库、搜索结果展示和未来文献/文件拆分边界见
[文献标题与文档内检索架构](docs/backend/architecture/document-title-and-search-filter.md)。

概念词典、同义词、中英文标注、语义检索与缓存后置的未实现设想见
[概念词典、多语言标注与语义检索设想](docs/backend/architecture/concept-retrieval-roadmap.md)。

P3 关键词搜索的数据规模、表/索引空间、`EXPLAIN ANALYZE` 证据和阶段结论见
[关键词检索性能基线](docs/backend/performance/search-baseline-2026-08-10.md)。

Embedding 与 Generation JSONL 日志的次数、Token、P50/P95 耗时汇总口径和零费用命令见
[模型调用成本基线](docs/backend/performance/model-call-cost-baseline.md)。

关键词检索与语义检索的中英文真实问题、排名、相关性评分和混合检索重新评估条件见
[检索质量评估计划](docs/backend/evaluation/retrieval-quality-evaluation-plan.md)。

第一版“问题 → 语义证据 → 远程生成 → 答案与来源”的职责、接口和安全边界见
[带来源引用问答（RAG 第一版）架构](docs/backend/architecture/rag-answer-roadmap.md)。

问答质量的冻结样本类型、引用一致性检查、人工评分与失败保留规则见
[带来源问答质量评估计划](docs/backend/evaluation/rag-answer-quality-evaluation-plan.md)。

Python 类、函数和方法的中文 IDE 悬停说明要求见
[Python Docstring 与 IDE 悬停说明规范](docs/backend/development/python-docstrings.md)。

前端技术栈、模块边界、状态归属、接口适配与测试策略见
[前端应用架构](docs/frontend/architecture/frontend-application-architecture.md) 和
[前端分层与阶段路线](docs/frontend/architecture/frontend-roadmap.md)。

P6 个人账户、Session、文档归属和全链路隔离的细化主线图见
[P6 个人用户域与私有数据闭环](docs/shared/architecture/personal-user-domain.md)。

个人版 P5、前端 F0～F5、P6 个人用户与未来 P7 团队多租户之间的边界和推荐执行顺序见
[产品演进路线](docs/shared/architecture/product-evolution-roadmap.md)。

## 本地开发状态

Go 后端已经可以运行，`GET /health` 健康检查接口已通过真实 HTTP 请求和 Go 自动化测试验证；文档管理、异步处理任务和关键词检索接口也已经接入同一服务入口。

PostgreSQL 开发容器已通过 Docker Compose 启动，使用本机 `5433` 端口、256 MiB 内存上限和独立数据卷，并已通过健康检查与真实 SQL 验证。后端也已提供多阶段 Docker 镜像：最终镜像包含编译后的 Go 服务、Python 3.11 和 `rag_ai` 文档处理依赖，不包含 Go 编译器；Compose 会等待 PostgreSQL 健康后再启动后端。

Go 后端已使用 pgx 连接池连接 PostgreSQL，启动时会在 5 秒超时内执行真实 Ping；连接失败时 HTTP 服务不会启动。

SQL 迁移已经建立 `documents`、`document_jobs`、`text_chunks` 和 `embedding_jobs` 表。迁移文件通过 `go:embed` 编译进后端，服务启动时会按数字版本自动执行；执行器使用 PostgreSQL advisory lock、独立事务和 SHA-256 校验，能够防止并发迁移以及历史 SQL 被静默修改。单元测试和真实 PostgreSQL 隔离 schema 测试均已通过。

文档领域模型已定义与数据库字段对应的 Go 结构体、强类型处理状态和合法状态流转，并已通过表驱动测试验证。

PostgreSQL 文档仓储已实现 `Create`、`GetByID`、分页 `List` 和 `Delete`。真实数据库集成测试已验证插入、按 ID 查询、领域级 `ErrNotFound` 转换、总数统计、稳定倒序、`LIMIT/OFFSET` 分页、空页结果、删除和重复删除。分页测试使用独立临时 schema，默认并行运行全部数据库测试时不会受到其他包的临时数据干扰，并会在结束后自动清理测试 schema。

文档应用服务已实现 `GetByID`、分页 `List` 和 `Delete` 用例，负责校验文档 ID 与分页参数、把 `page/page_size` 转换为 `limit/offset`、计算总页数，以及按“查询记录、删除文件、删除数据库记录”的顺序编排删除流程；相关单元测试和 Go 全量测试均已通过。

`GET /documents/:id` 已接入 Gin、应用服务和 PostgreSQL 仓储。接口自动化测试已覆盖非法 ID、文档不存在、内部错误和查询成功，并通过真实 PostgreSQL 数据验证了 HTTP `200`、`404`、`400` 响应；响应不会暴露服务器内部的 `storage_path`。

文档上传应用服务已实现文件保存、同一用户内按 SHA-256 内容去重和数据库记录创建的流程编排。首次上传保留新记录和物理文件；同一用户再次上传完全相同内容时返回已有记录并补偿删除本次新文件；不同用户仍保留各自的独立记录与文件。PostgreSQL 的 `(owner_user_id, sha256)` 唯一索引负责并发兜底，避免两个同时到达的上传请求绕过查重。数据库写入失败时同样执行文件补偿删除。本地文件存储支持 `.pdf`、`.md`、`.markdown` 和 `.txt` 白名单：PDF 校验 `%PDF-` 文件头，Markdown/TXT 以固定内存流式校验 UTF-8，`.markdown` 的物理扩展名统一保存为 `.md`。200 MiB 大小限制、流式写入、SHA-256 计算、临时文件清理、安全路径删除和上下文取消均已通过自动化测试验证。

`POST /documents` 已使用 Gin 的流式 multipart 读取接入上传应用服务，并通过 `http.MaxBytesReader` 限制整个请求体。首次保存返回 `201 Created` 与 `duplicate: false`；同一用户的重复内容返回原文档、`200 OK` 与 `duplicate: true`，不会重复解析、切块或生成向量。接口自动化测试覆盖缺少文件、创建成功、重复命中、应用错误映射和超大请求体；真实 HTTP/PostgreSQL/本地文件系统集成测试确认重复上传后只有一条文档记录和一个物理文件。PDF、MD、MARKDOWN 和 TXT 均返回正确 MIME，数据库保存规范化物理扩展名，不支持的 DOCX 返回 `415 Unsupported Media Type`。

`GET /documents` 已提供分页列表，默认 `page=1`、`page_size=20`，单页最多 100 条。接口自动化测试已覆盖默认值、自定义参数、非法输入、应用错误映射、内部字段隐藏和空数组响应；真实 HTTP 请求已验证空数据库下的 `200` 分页响应，以及非法页码和超大单页数量的 `400` 响应。

`GET /documents/:id/chunks` 已提供文档文本块分页浏览，默认 `page=1`、`page_size=20`，单页最多 100 条。响应按照 `chunk_index` 原文顺序返回内容、物理页码和稳定 chunk ID；只有 `ready` 文档可以读取，其他状态返回 `409`，避免重新处理期间把旧 chunks 当成正式结果。接口已通过 Handler、Application、真实 PostgreSQL 分页和 HTTP 纵向测试。

`DELETE /documents/:id` 已完成领域仓储能力、应用服务、PostgreSQL 实现、Gin Handler、路由注册和启动依赖注入。自动化测试覆盖非法 ID、文档不存在、文件删除失败、数据库删除失败、删除顺序以及 HTTP `204`、`400`、`404`、`500` 映射。真实 HTTP 冒烟测试已验证上传返回 `201`、删除前查询返回 `200`、删除返回空响应体的 `204`、删除后查询返回 `404`，并确认数据库记录数归零且物理文件已经删除。

`document_jobs` 已提供 `queued`、`processing`、`succeeded` 和 `failed` 四种任务状态，并通过部分唯一索引保证同一文档同时最多只有一个活动任务。PostgreSQL 仓储集成测试已验证任务创建、重复任务冲突和删除文档后的任务级联清理。

`POST /documents/:id/process` 已实现解析任务排队，成功返回 `202 Accepted`；非法 ID 返回 `400`，文档不存在返回 `404`，文档状态不允许处理或已经存在活动任务时返回 `409`。真实 HTTP 测试已验证首次排队为 `202`、重复排队为 `409`、数据库中只有一个 `queued` 任务，并确认任务排队阶段不会提前把文档标记成 `processing`。

`GET /processing-jobs/:id` 已实现单个解析任务状态查询，返回任务状态、尝试次数、错误信息和各阶段时间。自动化测试已覆盖非法 ID、任务不存在、内部错误和成功响应；真实 HTTP 测试已验证 `200`、`400`、`404`，并确认查询结果与刚创建的 `queued` 任务一致。

Worker 已实现按 Owner 公平领取下一条任务：PostgreSQL 事务使用 `FOR UPDATE SKIP LOCKED` 同时锁定 Owner 调度游标和具体任务，防止并发重复领取。存在多个等待用户时，每个用户默认先获得 1 个解析槽位；没有其他用户可获得基础槽位时，同一用户最多可借用到 2 个槽位，既避免单个大批量用户长期占满 Worker，也避免低负载时闲置算力。等待超过 2 分钟的 Owner 进入防饥饿优先级；任务领取、文档进入 `processing` 和调度游标更新在同一事务内完成。真实 PostgreSQL 集成测试已验证不同 Owner 优先、空闲容量借用、借用上限、槽位释放、防饥饿及两个并发 Worker 领取不同 Owner 任务。

Worker Application 已定义可替换的文档处理器端口，并实现单次“领取、查询文档、调用处理器、保存统一文本块、成功或失败收尾”编排。PostgreSQL 会在事务中同步更新任务与文档：成功时分别进入 `succeeded` 和 `ready`，处理或文本块保存失败时都进入 `failed` 并保存安全错误说明。单元测试和真实 PostgreSQL + Fake Processor 集成测试已经覆盖成功、处理失败、文本块保存失败、状态回写失败和双重错误保留。

统一文本块领域模型已经区分处理器输出 `ChunkInput` 和持久化结果 `TextChunk`。PostgreSQL 文本块仓储支持在事务中原子替换一份文档的全部文本块，并按块序号查询；真实数据库测试已验证替换、稳定排序、约束失败回滚、缺失文档错误转换和删除文档后的级联清理。Worker 只有在文本块成功保存后才会把任务和文档标记为成功。

本地文件存储已经提供安全 `Open` 能力，能够校验存储路径、保留文件系统错误并在读取过程中响应 context 取消。Go 原生 `TextProcessor` 支持 `text/markdown` 和 `text/plain`，会流式读取 UTF-8 字符、折叠连续空白、去除 BOM，并按每块最多 1000 个 Unicode 字符生成从 0 开始的稳定文本块。自动化测试已覆盖格式拒绝、打开失败、空文本、非法 UTF-8、context 取消、成功与失败关闭，以及真实 LocalStorage 跨层组合。

固定大小 Worker Pool 已经实现连续领取、空队列等待、错误上报和 context 取消；默认并发为 1，可通过 `DOCUMENT_WORKER_CONCURRENCY` 配置为 1～4。Python Process Pool 也已实现固定槽位租借、进程复用、单进程处理数量上限、超时杀进程、崩溃替换、输出边界和 shutdown；默认仍使用 `oneshot`，显式配置 `pool` 后才启用。两个不同任务的 PostgreSQL 收尾、两个一次性 Python 子进程以及两个常驻 Python 槽位均已完成并发验收。HTTP 服务使用标准库 `http.Server` 响应退出信号和执行限时优雅关闭；真实 HTTP、PostgreSQL 与本地文件链路已经验证 Markdown 文档能够自动处理为统一文本块。Worker 使用独立子 context 限制单份文档的处理时间，默认超时为 5 分钟，并在超时后使用仍有效的父 context 将任务安全标记为失败。

文档解析任务入队已经增加 PostgreSQL 原子背压：默认每个用户最多保留 5 条 `queued/processing` 任务，全系统最多 40 条；并发请求通过文档行锁和短事务 advisory lock 竞争容量，不会突破最后一个名额。用户满额返回 `429`，全局满额返回 `503`，成功或失败的历史任务不占用名额。

服务启动时会在 Worker 运行前恢复上一次异常退出遗留的 `processing` 任务，并在同一 PostgreSQL 事务内把任务和关联文档标记为 `failed`。真实启动测试已验证恢复数量、双表状态一致性、安全错误信息和第二次启动的幂等性。当前恢复策略建立在单实例 Worker 约束上；未来扩展为多实例时需要使用 lease/heartbeat 判断任务是否真正失联。

`ProcessorDispatcher` 已作为统一处理器入口接入 Worker，根据数据库中的可信 MIME 类型选择具体实现。`text/markdown` 和 `text/plain` 路由到 Go `TextProcessor`，`application/pdf` 路由到生产 `PythonProcessor`；尚未注册的格式会返回可判断的错误，而不会误用其他处理器。未来增加 DOCX、OCR 或替换 PDF 解析器时，可以继续添加或替换适配器，Worker 的任务领取、超时、文本块保存和状态收尾流程不需要修改。

Go/Python 文档处理契约已在 `contracts/document-processing/v1` 中定义版本化请求、成功响应、失败响应、稳定错误码、示例和进程通信规则。Go 基础设施层已经实现安全绝对路径解析、请求构造、UTF-8 JSON 编码、严格响应解码、文本块不变量校验、结构化 Python 失败错误、子进程超时取消和 stdout/stderr 输出上限；同一份 v1 消息结构既支持 oneshot，也支持一行请求对应一行响应的 JSON Lines 常驻传输。生产 `PythonProcessor` 和 `ProcessPool` 都满足相同的 Application 处理器端口，自动化测试覆盖复用、主动回收、非法响应、进程崩溃替换、超时取消与输出超限。Python 内部已经按 domain、application、contracts、entrypoints 和 infrastructure 完成轻量分层；pypdf 与简单文本切分器通过 application 定义的 Protocol 接入，不向核心层暴露第三方框架对象。Python PDF 处理能够识别伪造或损坏文件、密码要求、提取权限限制、OCR 需求以及文件和页数超限，并能对普通数字 PDF 逐页提取、规范化、页内分块和返回物理页码。真实英文文献已生成 126 个带页码文本块，真实中文文献已生成 42 个带页码文本块；二者均通过 HTTP 上传、Worker 异步处理、PostgreSQL 入库和 `ready` 状态验收。连字、页眉页脚、双栏和表格阅读顺序等质量改进留在 PDF-4 阶段。

`GET /search` 已实现以统一文本块为结果单位的关键词检索，并在 P6/B5.2 接入 Session 与 OwnerScope。接口保留 `q` 完整短语，同时支持 2～8 个重复 `term`、`operator=all|any` 和 `within=chunk`，可要求全部关键词或任一关键词出现在同一个 chunk。Handler 另接收可选 `document_id`、`page` 和 `page_size`，Application 负责规范化、去重、业务校验、分页换算与总页数计算，PostgreSQL 仓储在 `text_chunks` 与 `documents` 之间执行关联查询；count 与 data 两条 SQL 都使用相同的用户、状态、词项和文档条件。跨文档搜索只表示当前用户的全部 `ready` 文档，其他用户即使指定真实 `document_id` 也只得到正常 `200` 空结果。HTTP 响应包含规范化检索模式、命中文本、文献标题、原始文件名、物理页码和分页元数据。Repository 使用参数化 `ILIKE` 保持中英文大小写不敏感的字面子串语义，并转义 `%`、`_` 与反斜杠；第 6 号迁移通过 `pg_trgm + GIN` 加速数万文本块规模下的稀有子串查询。当前排序仍为确定性的“较新文档优先、文档内原文顺序”，不把时间顺序包装成相关性评分；sentence/paragraph 共现尚未实现。

`POST /documents/:id/embeddings` 已实现独立向量任务的手动入队，并在 P6/B5.1 接入 Session 与 OwnerScope。当前用户自己的文档可以保存向量化意图：文本已经 ready 时进入 `queued`，尚未完成解析时进入 `waiting_document`；同一文档已有活动任务时幂等返回原任务。非法 ID 返回 `400`，文档不存在或属于其他用户统一返回 `404`。任务会冻结 `model_name` 和 `dimensions`，第一版数据库固定使用 1536 维向量；DashScope 默认模型为 `text-embedding-v4`，OpenAI 默认模型为 `text-embedding-3-small`。入队端默认限制每用户 100、全局 500 条活动任务，并通过 PostgreSQL 事务级 advisory lock 原子执行容量检查与创建；用户满额返回 `429`，系统满额返回 `503`。当 `EMBEDDING_WORKER_ENABLED=true` 时，后台固定大小 Worker Pool 会按文件并行、按文件内部批次顺序调用当前配置的远程 API，并通过 PostgreSQL 事务原子保存一份文档的全部 chunk 向量与任务成功状态。并发数由 `EMBEDDING_WORKER_CONCURRENCY` 控制，默认 1 可安全降级，允许 1～4；PostgreSQL `FOR UPDATE SKIP LOCKED` 保证并发 Worker 不会领取同一任务。向量任务也采用独立的 Owner 公平调度：多用户竞争时默认每个 Owner 先获得 1 个 processing 槽位，无其他用户等待时最多借用到 2 个；到期 queued 任务真正等待超过 2 分钟后进入防饥饿优先级，重试任务从 `next_attempt_at` 开始计算等待。远程调用容量采用“分类隔离 + 全局保险”：后台 Worker 默认最多占 2 个槽位，语义检索与问答内部检索共用另外 2 个在线槽位，所有入口合计不超过 4。在线请求等待 2 秒仍无槽位时返回稳定 `503 embedding_provider_capacity_exhausted`，后台任务则继续等待至任务取消或服务关闭，因此后台任务堆积不会把在线语义检索完全饿死。临时错误按指数退避重新排队，鉴权、参数、余额或额度耗尽等永久错误进入 `failed`，正常 shutdown 遗留的 `processing` 任务会在下次单实例启动时恢复为 `queued`。Worker 默认关闭，不会产生远程调用或模型费用。

`GET /embedding-jobs/:id` 已提供受 Session 保护的向量任务状态查询。接口通过任务 JOIN 所属文档并按 OwnerScope 过滤，任务不存在或属于其他用户统一返回 `404`；成功时返回任务所属文档、冻结的模型与维度、当前状态、尝试次数、下次重试时间、错误信息、Token 用量和各阶段时间戳。前端可以在创建任务获得 ID 后轮询该接口，而不需要读取数据库或依赖后端日志。

`POST /embedding-jobs/latest` 已提供按最多 100 个文档 ID 批量发现最新向量任务的能力，用于页面刷新、换浏览器或换设备后恢复服务端状态，避免逐文档产生 N+1 查询。Application 对 ID 去重并保持首次出现顺序，PostgreSQL 通过 OwnerScope JOIN 和 `(document_id, id DESC)` 索引一次选出各文档最新任务；没有任务、不存在和属于其他用户统一返回 `job: null`。该接口只表达最新任务快照，不能在尚无 document revision 契约时把历史成功任务冒充为当前版本向量就绪。

DashScope 真实验收已经完成：文档 20 的 42 个文本块创建任务 22，使用 `text-embedding-v4`
生成 1536 维向量；任务只领取 1 次并进入 `succeeded`，记录 16399 个输入 Token。数据库最终存在
42 条互不重复、维度一致且非零的向量，没有遗漏文本块，也没有遗留 `queued/processing` 任务。

`POST /semantic-search` 是受 Session 保护的接口，使用 JSON 请求体接收自然语言 `query`、可选 `document_id` 和可选 `top_k`。
Application 使用当前配置的同一模型生成一条查询向量，PostgreSQL + pgvector 再按照精确余弦相似度
返回最接近的文本块及文献标题、原始文件名和物理页码。查询只比较模型名称与维度都一致的向量；
`top_k` 默认是 5，第一版上限为 20。指定文档不存在时返回 `404`；文档存在但状态、chunks 或当前
模型向量尚未完整就绪时返回 `409`，并在调用远程 API 前终止。该接口由 `SEMANTIC_SEARCH_ENABLED`
独立控制，默认关闭；通过就绪检查后的检索会调用远程 Embedding API，可能产生费用。内部各层、错误映射、真实 PostgreSQL
查询和真实 DashScope HTTP 均已经通过验收。中文问题“磁悬浮列车如何通过控制方法提高系统稳定性？”
在文档 20 中返回 5 条相似度降序结果；不提供 `top_k` 时默认返回 5 条，明确提供 0 时返回 400。P6/B5.3
进一步让 Handler、Application、向量就绪检查和相似度 SQL 显式传递同一 OwnerScope；全库检索只覆盖当前
用户文档，其他用户的 `document_id` 按不存在处理，并在调用远程 Embedding 前返回 `404`。

`POST /answers` 同样受 Session 保护，使用 `query`、可选 `document_id`、可选 `top_k` 和可选 `response_language`，先通过语义检索取得已编号
证据，再调用 DashScope Chat Completions 生成带 `[1]`、`[2]` 引用标记的回答。响应同时返回来源文献、
原始文件名、物理页码、相似度和 Token 用量。当前模型向量完整但没有证据时返回稳定降级答案与空
`sources`，不会调用生成模型；指定文档不存在返回 `404`，向量未完整就绪返回 `409`。该接口由
`ANSWER_ENABLED` 独立控制且默认关闭；应用分层、远程适配器、错误映射和
`main.go` 生产组合已经通过自动化测试。P5.2.5 已为在线生成调用增加 `started`、`succeeded`、`failed`
和无证据 `skipped` 结构化事件；日志通过 `request_id` 关联 HTTP 请求，记录模型、回答语言、证据数、远程耗时、
Token 与供应商错误分类，但不记录用户问题、Prompt、证据正文或答案。P4 收尾使用 8 篇中英文文献、460 个完整向量和 15 条冻结样本
完成真实远程验收：15/15 HTTP 行为符合预期，14/14 回答语言和引用编号通过，答案/行为人工支持度为
25/28；未向量化文档在远程调用前返回 `409` 并消耗 0 Token。该结果建立了第一版基线，不代表生产级
准确率；复杂表格列关系和全库问答的最佳证据选择仍是已登记问题。`response_language` 支持 `auto`、`zh` 和 `en`；省略时等同于
`auto`，由问题中的主要字符选择语言，响应会返回最终解析后的 `zh` 或 `en`。真实 HTTP 验收已确认
英文问题、省略、自动中文和显式语言覆盖均生效，非法值返回 400。P6/B5.4 要求 Answer Service 把同一
OwnerScope 传给 Semantic Search，只消费已经隔离的证据；未登录请求返回 `401`，跨用户文档返回 `404`，
且这两类请求都不会调用远程 Embedding 或 Generation。

`POST /auth/verification-codes` 已完成第一条 P6 认证 HTTP 纵向链路。Handler 接收 `channel`、`destination` 和
`purpose`，Application 调用 Domain 规则规范化联系方式并编排 PostgreSQL Challenge、密码学安全随机码、
HMAC 和默认零费用 Fake Sender。成功返回 `202` 以及 `verification_id`、UTC `expires_at` 和 UTC
`resend_after`，不暴露明文验证码或摘要；非法请求返回 `400`，数据库冷却或 HTTP 限流返回带
`Retry-After` 的 `429`，发送渠道不可用返回 `503`，未知故障返回安全 `500`。除数据库按联系方式执行
60 秒冷却外，HTTP 边界还按远端 IP 和全局预算执行单实例滑动窗口限流。

`POST /auth/register` 已完成第二条 P6 认证纵向链路。Application 校验显示名与密码策略、核对验证码 HMAC、
生成 Argon2id 密码哈希和 256-bit Session Token；PostgreSQL 在同一事务中创建用户、消费验证码并创建
Session，重复联系方式由唯一索引稳定映射为 `409`。成功返回 `201` 与公开 User DTO，并设置
`HttpOnly`、`SameSite=Lax` 的 `rag_session` Cookie；数据库只保存 Token 的 SHA-256 摘要。

`POST /auth/login` 已完成邮箱或 E.164 手机号登录。联系方式不存在、密码错误和停用账户统一返回
`401 invalid_credentials`；不存在账户仍执行哑 Argon2id 核对，减少通过响应耗时枚举账户的差异。成功登录
创建新的 PostgreSQL Session、返回 `200` 和与注册相同的公开 DTO，并设置新的 `rag_session` Cookie。

忘记密码使用现有 `POST /auth/verification-codes`，但必须提交独立用途 `purpose=password_reset`；注册验证码
不能用于重置密码。`POST /auth/password-reset` 接收 `verification_id`、六位 `verification_code` 和
`new_password`。PostgreSQL 在一个事务中锁定并消费挑战、更新 Argon2id 密码哈希并撤销该用户全部旧
Session；成功清除当前 Cookie 并返回 `204`，随后必须使用新密码重新登录。旧密码、旧 Cookie 和重复使用的
验证码均会失效；联系方式没有账户时也统一表现为验证码无效，不向 HTTP 调用方暴露账户是否存在。

Session 鉴权中间件会把 `rag_session` 原始 Token 转换为 SHA-256 摘要，联表恢复仍有效的 Session 与
active 用户，并在 Gin Context 中写入可信 `Actor{UserID, SessionID}`。`GET /users/me` 使用该身份返回
公开用户 DTO；缺少、伪造、过期或已撤销的 Cookie 统一返回 `401 authentication_required`。
`POST /auth/logout` 幂等撤销当前 Session、始终清除 Cookie 并返回 `204`；旧 Cookie 随后无法再次认证。
上述链路已经通过单元测试、真实 PostgreSQL Repository 测试和注册→登录→当前用户→退出→旧 Cookie
失效的 HTTP 集成测试。B4 已把 `POST /documents`、`GET /documents`、`GET /documents/:id` 和
`DELETE /documents/:id` 接入认证保护：Handler 只从可信 Actor 构造 OwnerScope，Application 显式传递
Scope，PostgreSQL 在 SQL 中写入或过滤 `owner_user_id`。双用户真实 HTTP 测试已确认用户 B 看不到、也不能
删除用户 A 的文档。B4 还完成了 `POST /documents/:id/process`、`GET /documents/:id/chunks` 和
`GET /processing-jobs/:id`：解析任务通过关联文档创建和查询，chunks 的统计与分页 SQL 也会连接文档并
限定所有者；Worker 保持系统级全局消费，不依赖浏览器 Session。B5.1 又把向量任务创建和查询迁入受保护
路由，Application 显式传递 OwnerScope，Repository 通过关联文档约束任务归属；双用户真实 HTTP/PostgreSQL
测试已覆盖未登录 `401`、越权创建/查询 `404` 和所有者正常操作。B5.2 进一步把关键词搜索迁入受保护
路由，count/data SQL 都限定 OwnerScope；双用户相同关键词与越权 `document_id` 过滤已通过真实 HTTP/PostgreSQL
测试。B5.3～B5.4 已把语义检索和问答迁入受保护路由：向量就绪检查与 pgvector 候选集都在 SQL 阶段
限定所有者，Answer Service 继续传递同一 Scope。双用户真实 HTTP/PostgreSQL 测试使用本地固定 Embedder
和 Generator 验证结果与引用来源不串用户，并保证默认回归不产生远程费用。B6 已提供默认 dry-run、显式
`-confirm` 和预计数量复核的 `assign-document-owner` 运维命令，并在单个 PostgreSQL 事务中把 46 篇历史
无主文档认领给正式个人用户；2729 个 chunks、45 个解析任务、8 个向量任务和 460 条向量继续通过
`document_id` 继承归属，没有复制或丢失。B7 已把运行事实升级为数据库 `NOT NULL` 硬约束，并移除用户写操作的
无作用域仓储入口；默认回归与一次性 PostgreSQL 发布验收均已通过。

Python PDF 测试依赖 `ai/pyproject.toml` 中锁定的解析库。首次运行前安装项目依赖，然后执行测试：

```powershell
cd ai
python -m pip install -e .
python -m unittest discover -s tests -v
```

日常提交后端代码前，推荐从项目根目录执行统一默认回归：

```powershell
powershell.exe -NoProfile -ExecutionPolicy Bypass `
    -File .\scripts\quality\run-backend-regression.ps1
```

该命令检查 Go 格式、Go 测试、`go vet`、Python 测试和 Compose 配置，主动禁用真实数据库集成与远程 AI，
不会产生模型费用。覆盖范围、环境隔离、JSON 报告和分级验收边界见
[后端默认零远程费用回归](docs/backend/development/default-regression.md)。

修改 migrations、Repository、Worker 或 Go/Python 契约后，再显式执行本地集成回归：

```powershell
powershell.exe -NoProfile -ExecutionPolicy Bypass `
    -File .\scripts\quality\run-backend-local-integration.ps1
```

该命令会创建并删除一次性 PostgreSQL 数据库，真实验证 SQL、事务、Python 子进程、PDF、chunks 和 HTTP
纵向链路，但仍不调用远程模型。安全边界和故障清理见
[后端本地集成回归](docs/backend/development/local-integration-regression.md)。

准备后端发布候选时，使用聚合门禁顺序执行默认回归和本地集成回归：

```powershell
powershell.exe -NoProfile -ExecutionPolicy Bypass `
    -File .\scripts\quality\run-backend-release-acceptance.ps1
```

容器生命周期、真实复杂 PDF 和远程供应商验收不会被默认误触。分级入口、外部状态和费用边界见
[后端发布验收与 P5 收尾](docs/backend/development/release-acceptance.md)。

## 配置与安全约定

Go 后端当前支持以下环境变量：

| 变量 | 默认值 | 说明 |
| --- | --- | --- |
| `APP_PORT` | `8080` | Go HTTP 服务监听端口，有效范围为 1 到 65535 |
| `LOG_LEVEL` | `info` | 最低日志级别，可选 `debug`、`info`、`warn`、`error` |
| `LOG_FORMAT` | `json` | 日志输出格式，可选机器友好的 `json` 或终端友好的 `text`；成本汇总必须使用 `json` |
| `DB_HOST` | `localhost` | Go 连接 PostgreSQL 时使用的主机 |
| `DB_PORT` | `5433` | PostgreSQL 映射到本机的端口 |
| `DB_NAME` | `rag_platform` | 数据库名称 |
| `DB_USER` | `rag_user` | 数据库用户 |
| `DB_PASSWORD` | 无 | 本机私有密码，必须在 `.env` 中设置 |
| `DB_SSLMODE` | `disable` | 本地开发时的 PostgreSQL SSL 模式 |
| `DB_MAX_CONNECTIONS` | `10` | 单个 Go 后端实例的 PostgreSQL 连接池上限；多实例时应按实例数合计 |
| `VERIFICATION_HMAC_SECRET` | 无 | 验证码 HMAC 服务端密钥，至少 32 字节，必须保存在本机 `.env` |
| `VERIFICATION_SENDER` | `fake` | 验证码发送实现；`fake` 仅供自动化测试，`mailpit` 用于本地人工邮件联调 |
| `VERIFICATION_SMTP_HOST` | `127.0.0.1` | 直接运行 Go 后端时连接 Mailpit 的 SMTP 主机 |
| `VERIFICATION_SMTP_COMPOSE_HOST` | `mailpit` | Compose 后端容器内使用的 Mailpit 服务名 |
| `VERIFICATION_SMTP_PORT` | `1025` | Mailpit 容器内部 SMTP 端口 |
| `VERIFICATION_SMTP_FROM_ADDRESS` | `no-reply@rag.local` | 本地验证码邮件的信封和头部发件地址 |
| `VERIFICATION_SMTP_FROM_NAME` | `RAG Reasoning Platform` | 本地验证码邮件的发件人显示名 |
| `VERIFICATION_SMTP_TIMEOUT` | `5s` | 本地 SMTP 建连和交付的最长等待时间 |
| `MAILPIT_SMTP_HOST_PORT` | `1025` | Mailpit SMTP 映射到宿主机的端口 |
| `MAILPIT_WEB_HOST_PORT` | `8025` | Mailpit Web 收件箱映射到宿主机的端口 |
| `VERIFICATION_RATE_LIMIT_WINDOW` | `1m` | 验证码 HTTP 单实例滑动窗口长度 |
| `VERIFICATION_PER_CLIENT_LIMIT` | `5` | 同一远端 IP 在窗口内允许占用的请求数 |
| `VERIFICATION_GLOBAL_LIMIT` | `100` | 整个后端进程在窗口内允许占用的验证码请求数 |
| `AUTH_SESSION_TTL` | `168h` | 注册和登录创建的 PostgreSQL Session 有效期，默认 7 天 |
| `AUTH_COOKIE_SECURE` | `false` | 是否给 `rag_session` Cookie 增加 `Secure`；生产 HTTPS 必须为 `true` |
| `AUTH_RATE_LIMIT_WINDOW` | `1m` | 注册与登录共用的单实例滑动窗口长度 |
| `AUTH_PER_CLIENT_LIMIT` | `10` | 同一远端 IP 在窗口内允许的认证请求数 |
| `AUTH_GLOBAL_LIMIT` | `200` | 整个后端进程在窗口内允许的认证请求数 |
| `APP_ROOT` | 开发环境自动发现 | 应用运行时资源的共同根目录；显式配置时必须是已经存在的绝对目录 |
| `STORAGE_ROOT` | `storage` | 本地文档存储根目录；相对路径固定以 `APP_ROOT` 为基准 |
| `STORAGE_HOST_PATH` | `./storage` | Compose 挂载的宿主机文件目录；恢复验收后可无覆盖切换到新目录 |
| `STORAGE_MAX_FILE_SIZE_BYTES` | `209715200` | 单个上传文件允许的最大字节数，即 200 MiB |
| `UPLOAD_MAX_CONCURRENCY_PER_USER` | `2` | 单个用户同时执行的完整上传链路上限 |
| `UPLOAD_MAX_CONCURRENCY_GLOBAL` | `16` | 单个后端实例同时执行的完整上传链路上限，不能小于单用户值 |
| `UPLOAD_QUEUE_WAIT_TIMEOUT` | `2s` | 上传请求等待单用户和全局槽位的最长时间 |
| `WORKER_POLL_INTERVAL` | `2s` | 文档 Worker 在空队列或单轮错误后的轮询间隔 |
| `WORKER_PROCESSING_TIMEOUT` | `5m` | 单份文档从处理器调用到任务收尾的最大时间 |
| `DOCUMENT_WORKER_CONCURRENCY` | `1` | 同一实例的文档 Worker 数，允许 1～4；默认 1 可安全降级 |
| `PROCESSING_MAX_ACTIVE_JOBS_PER_USER` | `5` | 单个用户允许的 queued/processing 文档解析任务总数 |
| `PROCESSING_MAX_ACTIVE_JOBS_GLOBAL` | `40` | 全系统允许的活动文档解析任务总数，不能小于单用户上限 |
| `PROCESSING_MAX_IN_FLIGHT_PER_OWNER` | `1` | 多用户竞争时，每个 Owner 默认可以占用的 processing 槽位数 |
| `PROCESSING_MAX_BORROWED_IN_FLIGHT_PER_OWNER` | `2` | 没有其他 Owner 可获得基础槽位时，单个 Owner 可借用到的 processing 绝对上限 |
| `PROCESSING_STARVATION_THRESHOLD` | `2m` | queued 任务等待多久后进入 Owner 防饥饿优先级 |
| `PYTHON_EXECUTABLE` | `python` | Go 启动复杂文档处理子进程时使用的 Python 可执行程序 |
| `PYTHON_SOURCE_ROOT` | `ai/src` | 包含 `rag_ai` 包的 Python 源码目录；相对路径固定以 `APP_ROOT` 为基准 |
| `PYTHON_PDF_MAX_FILE_SIZE_BYTES` | `52428800` | PDF 解析文件上限，即 50 MiB；独立于上传上限 |
| `PYTHON_PDF_MAX_PAGES` | `500` | 单份 PDF 允许解析的最大页数 |
| `PYTHON_PROCESS_MODE` | `oneshot` | `oneshot` 每任务一个进程；`pool` 启用可复用常驻进程 |
| `PYTHON_PROCESS_POOL_SIZE` | `2` | pool 模式的 Python 槽位数，允许 1～4且不能小于文档 Worker 数 |
| `PYTHON_PROCESS_MAX_DOCUMENTS` | `20` | 单个常驻 Python 进程处理多少份文档后主动回收 |
| `EMBEDDING_WORKER_ENABLED` | `false` | 是否启动远程 Embedding Worker；默认关闭以避免意外费用 |
| `EMBEDDING_WORKER_CONCURRENCY` | `1` | 同一实例内的向量 Worker 数，允许 1～4；默认 1 可安全降级 |
| `EMBEDDING_MAX_ACTIVE_JOBS_PER_USER` | `100` | 单个用户允许的 waiting/queued/processing 向量任务总数 |
| `EMBEDDING_MAX_ACTIVE_JOBS_GLOBAL` | `500` | 全系统允许的活动向量任务总数，不能小于单用户上限 |
| `EMBEDDING_MAX_IN_FLIGHT_PER_OWNER` | `1` | 多用户竞争时，每个 Owner 默认可以占用的向量 processing 槽位数 |
| `EMBEDDING_MAX_BORROWED_IN_FLIGHT_PER_OWNER` | `2` | 无其他 Owner 可获得基础槽位时，单个 Owner 可借用到的向量 processing 上限 |
| `EMBEDDING_STARVATION_THRESHOLD` | `2m` | 到期 queued 向量任务等待多久后进入 Owner 防饥饿优先级 |
| `EMBEDDING_PROVIDER_MAX_CONCURRENCY` | `4` | 单个后端进程内所有远程 Embedding 在途调用的全局硬上限 |
| `EMBEDDING_WORKER_PROVIDER_CONCURRENCY` | `2` | 后台向量 Worker 可占用的远程调用上限；启用 Worker 时不能小于 Worker 数 |
| `EMBEDDING_ONLINE_PROVIDER_CONCURRENCY` | `2` | 语义检索和问答内部语义检索共享的在线远程调用上限 |
| `EMBEDDING_ONLINE_QUEUE_WAIT_TIMEOUT` | `2s` | 在线语义检索或问答等待共享 Embedding 槽位的最长时间 |
| `SEMANTIC_SEARCH_ENABLED` | `false` | 是否注册在线语义检索接口；启用后每次检索会调用远程 Embedding API |
| `EMBEDDING_PROVIDER` | `dashscope` | 当前远程提供方，可选 `dashscope` 或 `openai` |
| `DASHSCOPE_API_KEY` | 无 | 选择 DashScope 且 Worker 或语义检索启用时必填；只能保存在本机 `.env` |
| `DASHSCOPE_EMBEDDING_ENDPOINT` | 百炼中国内地兼容地址 | DashScope Embeddings HTTP API 地址 |
| `OPENAI_API_KEY` | 无 | 选择 OpenAI 且 Worker 或语义检索启用时必填；保留供以后切回 |
| `OPENAI_EMBEDDING_ENDPOINT` | OpenAI 官方地址 | OpenAI Embeddings HTTP API 地址 |
| `EMBEDDING_MODEL` | 按提供方决定 | 留空时 DashScope 使用 `text-embedding-v4`，OpenAI 使用 `text-embedding-3-small` |
| `EMBEDDING_DIMENSIONS` | `1536` | 第一版固定维度；调整前必须迁移数据库并全量重建向量 |
| `EMBEDDING_BATCH_SIZE` | 按提供方决定 | 留空时 DashScope 为 10、OpenAI 为 32；不能超过提供方上限 |
| `EMBEDDING_HTTP_TIMEOUT` | `30s` | 单次远程 HTTP 请求超时 |
| `EMBEDDING_PROCESSING_TIMEOUT` | `5m` | 单个向量任务包含全部批次的总处理超时 |
| `EMBEDDING_POLL_INTERVAL` | `2s` | 空队列或单轮错误后的轮询等待时间 |
| `EMBEDDING_MAX_ATTEMPTS` | `5` | 临时错误允许的最大领取次数 |
| `EMBEDDING_RETRY_BASE_DELAY` | `5s` | 第一次延迟重试的基础等待时间 |
| `EMBEDDING_RETRY_MAX_DELAY` | `2m` | 指数退避等待时间上限 |
| `ANSWER_ENABLED` | `false` | 是否注册带来源问答接口；启用后会调用 Embedding 与 Generation API |
| `ANSWER_MAX_CONCURRENCY` | `10` | 单个后端进程同时执行的完整问答链路全局上限；保护远程 API、数据库连接池和本机资源 |
| `ANSWER_MAX_CONCURRENCY_PER_USER` | `2` | 同一用户同时执行的问答上限，避免单个用户占满全局槽位 |
| `ANSWER_MAX_WAITERS_GLOBAL` | `500` | 单个后端进程允许同步等待问答槽位的全局请求上限 |
| `ANSWER_MAX_WAITERS_PER_USER` | `5` | 同一用户允许同步等待问答槽位的请求上限；超出返回 `429` |
| `ANSWER_QUEUE_WAIT_TIMEOUT` | `5s` | 问答请求等待执行槽位的最长时间；超时返回可重试的 `503 answer_capacity_exhausted` |
| `DASHSCOPE_GENERATION_ENDPOINT` | 百炼中国内地兼容地址 | DashScope Chat Completions HTTP API 地址 |
| `GENERATION_MODEL` | `qwen3.6-flash` | 第一版回答生成模型；由后端配置，不接受前端任意指定 |
| `GENERATION_HTTP_TIMEOUT` | `60s` | 单次远程回答生成请求超时 |
| `GENERATION_MAX_OUTPUT_TOKENS` | `1024` | 生成答案的最大输出 Token，上限为 8192 |
| `GENERATION_TEMPERATURE` | `0.1` | 生成温度，有效范围为 0 到 2；低温度用于减少回答随机性 |
| `GENERATION_THINKING_ENABLED` | `false` | 是否启用百炼混合思考；首版默认关闭以控制延迟、Token 和空最终答案风险 |

`.env.example` 是可以提交到 Git 的配置模板，不得包含密码或真实密钥。`.env` 用于保存本机配置和密钥，已被 Git 忽略。

Docker Compose 会自动读取项目根目录的 `.env`。当前 Go 程序通过 `os.Getenv` 读取操作系统环境变量，不会自动加载 `.env` 文件。

开发环境没有设置 `APP_ROOT` 时，后端会从当前工作目录向上查找同时包含
`backend/go.mod` 与 `ai/src/rag_ai` 的项目根目录，因此从项目根目录或 `backend`
目录启动都能得到相同的存储和 Python 路径。部署产物不一定带有源码目录标志，必须显式设置
绝对路径形式的 `APP_ROOT`。详细规则见
[运行路径与配置契约](docs/backend/development/runtime-path-configuration.md)。

Go + Python 后端镜像的构建、Compose 启停、健康检查、数据持久化和安全停止方式见
[后端容器部署指南](docs/backend/deployment/container-deployment.md)。

PostgreSQL 与 `storage/` 的配套备份、SHA-256 manifest、默认不覆盖恢复和无覆盖切换方法见
[数据配套备份与恢复指南](docs/backend/deployment/data-backup-and-restore.md)。

容器 `SIGTERM` 优雅关闭、`SIGKILL` 异常模拟、文档/Embedding 差异化任务恢复及可重复验收命令见
[容器优雅关闭与异常恢复](docs/backend/deployment/container-lifecycle-and-recovery.md)。

在 PowerShell 中可以这样临时设置 Go 服务端口和一次性验证码 HMAC 密钥：

```powershell
$env:APP_PORT = "9090"
$env:VERIFICATION_HMAC_SECRET = `
    [Convert]::ToHexString(
        [Security.Cryptography.RandomNumberGenerator]::GetBytes(32)
    ).ToLowerInvariant()

go run ./cmd/server

Remove-Item Env:APP_PORT
Remove-Item Env:VERIFICATION_HMAC_SECRET
```

上面的随机密钥只适合单次启动验收；进程重启后旧验证码会失效。正常本地开发应生成一次随机值并写入被 Git
忽略的 `.env`，再像 `DB_PASSWORD` 一样加载到当前 PowerShell 环境。禁止把真实密钥写入 `.env.example`、日志或提交记录。

本地人工注册可以启动可选 Mailpit：

```powershell
docker compose --profile mailpit up -d mailpit
```

然后把当前后端进程的 `VERIFICATION_SENDER` 设置为 `mailpit`，保持
`VERIFICATION_SMTP_HOST=127.0.0.1` 和 `VERIFICATION_SMTP_PORT=1025`，再访问
`http://localhost:8025` 查看验证码邮件。Mailpit 不向互联网发送邮件，也不能证明真实邮箱所有权；它只用于
零费用本地开发。默认 `fake` 回归不启动或访问 Mailpit。

P6/B6 历史文档认领必须先执行 dry-run，确认目标用户和数量：

```powershell
cd backend
go run ./cmd/assign-document-owner -owner-user-id 17
```

工具不会在 dry-run 修改数据库，并会输出一条包含当前预计数量的确认命令。只有显式提供 `-confirm` 和完全
匹配的 `-expected-unowned` 才会在单个事务内写入；目标用户不存在、已停用或数量变化都会整体拒绝，不会
留下部分认领。用户 ID 和数量必须以本机 dry-run 为准，不能照抄文档中的历史示例值。

本地 PostgreSQL 常用命令：

```powershell
docker compose up -d postgres
docker compose ps
docker compose stop postgres
```

`docker compose stop postgres` 只停止容器，不会删除数据卷中的数据。

从项目根目录启动完整后端容器：

```powershell
docker compose build backend
docker compose up -d backend
docker compose ps
curl.exe -i http://127.0.0.1:8080/health
docker compose stop backend
```

`docker compose up -d backend` 会按依赖关系同时启动 PostgreSQL。容器内后端始终监听 `8080`，
本机映射端口由 `BACKEND_HOST_PORT` 控制。不要在日常停止时执行 `docker compose down -v`，因为
`-v` 会删除 PostgreSQL 数据卷；完整配置、Linux 文件权限和恢复边界见
[后端容器部署指南](docs/backend/deployment/container-deployment.md)。

其他安全约定：

- 上传文件和运行数据统一存放在 `storage/`；物理文件名和相对路径由后端生成，不使用客户端本地路径。
- 不提交虚拟环境、缓存、日志、测试覆盖率文件和编译后二进制。
- `go.sum` 和后续采用的 Python 依赖锁文件应提交，以保证依赖可复现。

## License

当前项目用于个人学习和求职作品展示，暂未选择开源许可证。
