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
| `api` | HTTP API、同步检索与同步问答 | 第二阶段已放开 |
| `document-worker` | 文档领取、Python 解析、chunks 收尾 | 第二阶段已放开 |
| `embedding-worker` | Embedding 任务领取与向量落库 | 第二阶段已放开，要求启用向量 Worker |
| `answer-worker` | 持久化异步问答 Worker | 第二阶段已放开，要求启用问答与异步任务 |

空值默认 `all`。配置值会去除首尾空白并转换为小写；其他值返回可通过
`errors.Is(err, config.ErrUnsupportedApplicationRole)` 识别的启动错误。

第一阶段曾故意让预留角色安全退出，防止形成假隔离；第二阶段完成条件组装后已经移除这道临时限制。
`application_started` 日志包含 `role` 和 HTTP 状态，便于验证实际角色。

## 3. 第二阶段：条件组装（已完成）

角色能力矩阵放在现有 `backend/cmd/server/` 组合根中，不创建新业务层，也不移动
Domain/Application。当前实现按角色控制配置、Repository、远程客户端、Handler 和 Worker 生命周期：

```text
                    all   api   document   embedding   answer
HTTP                 ✓     ✓        -          -          -
LocalStorage         ✓     ✓        ✓          -          -
Python Pool          ✓     -        ✓          -          -
Document Worker      ✓     -        ✓          -          -
Embedding Worker     按开关 -        -         必须启用       -
同步检索/问答         按开关 按开关    -          -          -
Answer Job Worker    按开关 -        -          -         必须启用
认证与验证码          ✓     ✓        -          -          -
```

已经建立以下边界：

- API 不创建 Python Pool，也不启动后台领取循环；
- Document Worker 不创建 Gin 业务路由和远程 Generation Client；
- Embedding Worker 不加载 Python；
- Answer Worker 不启动文档或向量 Worker；
- `all` 复用相同组装函数，行为与当前版本一致。

专用 Worker 角色不监听 HTTP，而是在完成组装后等待退出信号，任务循环通过 PostgreSQL 队列表领取工作。
通用镜像不再固化 `/health` 检查；当前 Compose 的 HTTP `backend` 服务单独拥有健康检查，避免 Worker 被误判
为不健康。

### 启动约束

- `APP_ROLE=embedding-worker` 要求 `EMBEDDING_WORKER_ENABLED=true`；
- `APP_ROLE=answer-worker` 要求 `ANSWER_ENABLED=true` 且 `ANSWER_JOBS_ENABLED=true`；
- `APP_ROLE=api` 可以按原有开关选择是否暴露语义检索、同步问答和异步任务提交接口；
- `APP_ROLE=all` 保持向后兼容，所有可选远程能力仍默认关闭。

本阶段只证明单机角色隔离，尚未证明可以任意扩容。Embedding Provider Gate、Answer 并发闸门和上传闸门
目前仍是进程内状态；同时启动多个同类角色会把真实总并发按进程数放大。

## 4. 第三阶段：同机部署清单与角色探针（已完成）

Compose 已经显式声明一个 API 和三类 Worker，四个进程继续复用同一镜像：

| Compose 服务 | 角色 | 默认启动 | 健康依据 |
| --- | --- | --- | --- |
| `backend` | `api` | 是 | `GET /health` |
| `document-worker` | `document-worker` | 是 | `/tmp/rag-role-ready` |
| `embedding-worker` | `embedding-worker` | `embedding` Profile | `/tmp/rag-role-ready` |
| `answer-worker` | `answer-worker` | `answer` Profile | `/tmp/rag-role-ready` |

Worker 在数据库迁移、异常任务恢复和 Worker Pool 初始化完成后才写入 `APP_READY_FILE`；退出时先删除该文件，
再等待 goroutine 清理。这个文件是部署就绪信号，不是任务状态或业务事实。

API 与 Document Worker 挂载完全相同的 `STORAGE_HOST_PATH`。Embedding/Answer Worker 不挂载原始文件，也不
接收 Python、SMTP 等无关配置或密钥。远程 Worker 使用 Profile，避免普通 `docker compose up` 意外产生
模型费用。

本地 PostgreSQL `max_connections=20` 时，四类角色默认连接池分别为 5、3、3、5，总计 16；资源上限也按
角色单独配置，不再把 Python 内存和 HTTP 内存混成一个容器预算。

## 5. 多实例前置条件

部署角色拆分不等于已经可以任意增加实例。跨主机或多副本之前仍必须完成：

1. 本地 `storage/` 迁移为共享对象存储；
2. 任务 lease、heartbeat、条件收尾和过期恢复；
3. Redis 跨实例 Provider 并发闸门与共享限流；
4. 数据库迁移的单一执行者或受控 migration job；
5. 各角色独立资源、队列和故障注入压测。

`FOR UPDATE SKIP LOCKED` 可以避免两个 Worker 同时领取同一数据库任务，但不能自动解决本地文件不可见、
远程 Provider 总并发被实例数放大或异常实例长期占用任务的问题。

## 6. 已完成验收标准

- 未设置 `APP_ROLE` 时返回 `all`；
- 五个稳定角色值均能被配置层解析；
- 大小写与首尾空白被规范化；
- 未知角色返回稳定错误并保持零值配置；
- `all` 完整运行路径和默认回归不退化；
- 每个专用角色只启用能力矩阵内的组件；
- 专用远程 Worker 缺少功能开关时 fail-fast，不启动空壳进程；
- Worker-only 角色不创建 HTTP Server，收到退出信号后等待后台循环结束；
- 本机直接运行默认继续使用 `all`，Compose 默认拆为 `api` 与 `document-worker`；
- Worker 就绪文件必须使用绝对路径，并在退出时清理；
- 远程 Worker 由显式 Profile 控制，默认不会启动；
- HTTP、数据库和前端契约均未改变。

本地隔离验收使用同一个临时镜像和空 pgvector/PostgreSQL，依次启动五种角色并发送 SIGTERM；五个进程均以
退出码 0 完成清理。API 日志未出现 Python/Worker 组装事件，三个 Worker 日志均显示
`http_enabled=false`；空任务库保证验收过程没有远程 Provider 调用。
