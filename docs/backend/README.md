# 后端正式文档

本目录保存 Go API、Python 文档处理、PostgreSQL、检索、向量化和 RAG 问答的正式文档。

## 分类

- `architecture/`：系统能力与阶段性架构方案；
- `development/`：需要长期执行的开发规范；
- `evaluation/`：检索、回答等能力的评估方法；
- `performance/`：数据库和接口性能基线。

## 主要架构入口

- [PDF 与复杂文档处理路线](architecture/pdf-processing-roadmap.md)
- [语义检索路线](architecture/semantic-search-roadmap.md)
- [带来源问答路线](architecture/rag-answer-roadmap.md)
- [多能力检索编排路线](architecture/search-orchestration-roadmap.md)
- [概念词典与多语言检索设想](architecture/concept-retrieval-roadmap.md)
- [文档文本块浏览接口](architecture/document-chunk-browsing.md)
- [运行路径与配置契约](development/runtime-path-configuration.md)
- [日志与请求追踪规范](development/logging-observability.md)

## 质量评估入口

- [关键词与语义检索质量评估](evaluation/retrieval-quality-evaluation-plan.md)
- [带来源问答质量评估与 P4 收尾结论](evaluation/rag-answer-quality-evaluation-plan.md)

截至 2026-08-14，P4 AI 增强第一版已经完成质量收尾：15 条冻结样本的 HTTP 行为全部符合预期，
问答/拒答样本的语言和引用编号全部通过，复杂表格解释与最佳证据选择仍作为已知质量边界保留。

当前后端进入 P5 个人版工程化，优先处理运行路径、配置、日志、部署、备份、回归和性能/成本记录。
P5 不包含用户认证和多租户；未来 P6 的工作区、权限与数据隔离计划见
[产品演进路线](../shared/architecture/product-evolution-roadmap.md)。

P5.1 已建立统一 `APP_ROOT` 路径基准：本地开发可从项目根目录或 `backend` 目录启动，
部署环境则必须显式提供绝对应用根目录。文件存储与 Python 源码的相对路径不再依赖偶然的当前工作目录。

P5.2.1 已建立 `X-Request-ID` 与 JSON HTTP 访问日志：请求编号同时进入响应头、Go `context.Context`
和日志字段，便于前端反馈与后端排障关联。P5.2.2 第一版已为文档查询和解析任务查询建立统一错误响应：
前端使用稳定 `code` 判断错误，未知内部错误只返回安全消息，原始错误、诊断码和请求 ID 仅进入后端日志。
P5.2.3 已为文档解析 Worker 增加 `started`、`succeeded`、`failed` 和 `unfinished` 结构化生命周期事件，
能够通过任务 ID、文档 ID、尝试次数、状态和耗时排查异步任务。Embedding 任务事件和外部供应商错误分类
仍在后续 P5.2 中推进。

前端需要依赖的接口字段和 HTTP 行为，不在本目录单独定义，统一查看
[HTTP API 总览](../shared/api/http-api-overview.md)。
