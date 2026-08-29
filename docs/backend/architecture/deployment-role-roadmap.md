# 同一二进制部署角色拆分路线

## 1. 核心目的

项目继续保持一个代码仓库、一个 Go 模块和一个模块化单体，不拆成微服务。部署角色能力允许同一个后端二进制
启动多次，并让每个操作系统进程只运行指定组件，从而把 HTTP 延迟、PDF/Python 资源、Embedding Provider
容量和异步问答故障范围逐步隔离。

```text
同一个 rag-backend 二进制
├─ APP_ROLE=all                 本地兼容模式
├─ APP_ROLE=api                 HTTP API、同步检索与同步问答
├─ APP_ROLE=document-worker     文档任务与 Python Pool
├─ APP_ROLE=embedding-worker    后台向量任务
└─ APP_ROLE=answer-worker       持久化异步问答任务
```

角色由进程启动时的环境变量决定，不是每个 HTTP 请求临时选择。不同角色通过 PostgreSQL 任务表、正式业务表、
Redis 缓存以及后续共享文件存储协作，不通过新的内部 HTTP 微服务相互调用。

## 2. 第一阶段：配置契约（已完成）

`APP_ROLE` 的稳定值为：

| 值 | 最终职责 | 第一阶段行为 |
| --- | --- | --- |
| `all` | API 与全部 Worker | 可以运行，保持重构前行为 |
| `api` | HTTP API、同步检索与同步问答 | 已识别，但启动时安全退出 |
| `document-worker` | 文档领取、Python 解析、chunks 收尾 | 已识别，但启动时安全退出 |
| `embedding-worker` | Embedding 任务领取与向量落库 | 已识别，但启动时安全退出 |
| `answer-worker` | 持久化异步问答 Worker | 已识别，但启动时安全退出 |

空值默认 `all`。配置值会去除首尾空白并转换为小写；其他值返回可通过
`errors.Is(err, config.ErrUnsupportedApplicationRole)` 识别的启动错误。

第一阶段故意不让预留角色继续启动。否则设置 `APP_ROLE=api` 后仍运行 Document/Embedding Worker，
会形成比“暂不支持”更危险的假隔离。`application_started` 日志已经增加 `role` 字段，便于后续验证实际角色。

## 3. 第二阶段：组合根拆分

在现有 `backend/cmd/server/` 内拆分组装文件，不创建新业务层，也不移动 Domain/Application：

```text
cmd/server/
├─ main.go
├─ application.go
├─ api_role.go
├─ document_worker_role.go
├─ embedding_worker_role.go
└─ answer_worker_role.go
```

目标是让每个角色只加载和创建自己需要的基础设施：

- API 不创建 Python Pool，也不启动后台领取循环；
- Document Worker 不创建 Gin 业务路由和远程 Generation Client；
- Embedding Worker 不加载 Python；
- Answer Worker 不启动文档或向量 Worker；
- `all` 复用相同组装函数，行为与当前版本一致。

## 4. 第三阶段：生命周期与同机部署

Worker 角色需要内部健康/就绪探针、结构化 role 日志和统一 SIGTERM 行为。第一版只在同一台机器或同一个
Compose 项目中运行一个 API 和每类一个 Worker，并给 API/Document/Embedding/Answer 分别设置资源限制。
所有角色继续使用同一镜像、同一 PostgreSQL 和同一宿主机存储挂载。

## 5. 多实例前置条件

部署角色拆分不等于已经可以任意增加实例。跨主机或多副本之前仍必须完成：

1. 本地 `storage/` 迁移为共享对象存储；
2. 任务 lease、heartbeat、条件收尾和过期恢复；
3. Redis 跨实例 Provider 并发闸门与共享限流；
4. 数据库迁移的单一执行者或受控 migration job；
5. 各角色独立资源、队列和故障注入压测。

`FOR UPDATE SKIP LOCKED` 可以避免两个 Worker 同时领取同一数据库任务，但不能自动解决本地文件不可见、
远程 Provider 总并发被实例数放大或异常实例长期占用任务的问题。

## 6. 第一阶段验收标准

- 未设置 `APP_ROLE` 时返回 `all`；
- 五个稳定角色值均能被配置层解析；
- 大小写与首尾空白被规范化；
- 未知角色返回稳定错误并保持零值配置；
- `all` 完整运行路径和默认回归不退化；
- 预留拆分角色在任何外部连接前 fail-fast；
- Compose 与 `.env.example` 默认继续使用 `all`；
- 本阶段不修改 HTTP、数据库或前端契约。
