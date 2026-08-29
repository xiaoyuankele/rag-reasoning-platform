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

## 5. 第四阶段：跨进程远程模型容量协调（已完成）

进程内闸门继续负责本进程的 Owner 公平、等待队列和快速保护；独立 Redis 容量协调器负责所有 API、
Embedding Worker 和 Answer Worker 进程合计不能突破远程模型执行上限：

```text
本地进程内准入
  ├─ Embedding：全局 + Worker/Online 分类
  └─ Answer：全局 + Owner
            │
            ▼
Redis 原子租约（跨进程）
            │
            ▼
远程 Provider
```

一次申请通过 Lua 原子检查并写入多个维度。Embedding 同时占用“Provider 全局槽位”和
“Worker/Online 分类槽位”；Answer 同时占用“生成全局槽位”和“当前 Owner 槽位”。不同进程使用相同键，
因此新增角色进程不会把真实 Provider 并发按进程数放大。

容量 Redis 与查询/答案缓存 Redis 分离，并使用 `noeviction`。协调不可用时拒绝新的远程调用：同步请求沿用
稳定 503 与 `Retry-After`，异步任务沿用有限重试；不会像缓存故障那样直接回源。租约带 TTL，异常退出后会
自动恢复容量；当前默认 3 分钟，并要求长于一次完整问答调用预算。

该阶段没有改变 HTTP DTO、状态码或前端契约，也没有把任务状态放入 Redis。PostgreSQL 仍是任务和结果的
唯一事实来源。

## 6. 多实例前置条件

部署角色拆分不等于已经可以任意增加实例。跨主机或多副本之前仍必须完成：

1. 本地 `storage/` 迁移为共享对象存储；
2. 文档与 Embedding 任务的 lease、heartbeat、fencing 条件收尾和过期恢复已经完成；Answer 任务仍需按相同原则升级；
3. Redis Provider 并发闸门已完成；验证码、认证、上传等待区等共享频率/排队限制仍需按实测逐项迁移；
4. 数据库迁移的单一执行者或受控 migration job；
5. 各角色独立资源、队列和故障注入压测。

`FOR UPDATE SKIP LOCKED` 可以避免两个 Worker 同时领取同一数据库任务，但不能自动解决本地文件不可见、
远程 Provider 总并发被实例数放大或异常实例长期占用任务的问题。

### 文档任务租约（已完成）

文档 Worker 领取任务时会把 `worker_id`、随机 `lease_token`、`lease_expires_at` 和 `heartbeat_at`
与 `processing` 状态写入同一事务。处理期间按固定周期续租；只有仍持有未过期 token 的 Worker 才能：

- 原子替换该文档的 chunks；
- 把任务和文档写成 succeeded/failed；
- 激活等待中的 Embedding 任务。

恢复器只选择已经过期或升级前没有租约的 `processing` 任务，并通过 `FOR UPDATE SKIP LOCKED`
将其重新放回 `queued`。旧 Worker 即使稍后返回结果，也会收到 `ErrProcessingJobLeaseLost`，不能覆盖新 Worker。
默认租约为 60 秒、心跳为 15 秒；两者可通过环境变量调整，且心跳周期必须短于租约。

### Embedding 任务租约（已完成）

Embedding Worker 领取任务时同样持久化 Worker 身份、随机 token、到期时间和心跳。远程模型调用期间持续续租；
整份文档的向量覆盖与 succeeded 仍在一个事务中，并在事务入口核对 token 和数据库时间。临时失败的 requeue、
永久失败终态也使用相同 fencing 条件。租约到期后任务可以获得新 token 重新领取，而旧进程即使恢复也不能
写向量或终态。默认租约 60 秒、心跳 15 秒，对应 `EMBEDDING_JOB_LEASE_DURATION` 和
`EMBEDDING_JOB_HEARTBEAT_INTERVAL`。

## 7. 已完成验收标准

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
- 两个独立 Redis 客户端不能同时突破全局或 Owner/来源分类上限；
- 进程异常遗留的容量租约会按 TTL 自动回收；
- Redis 协调故障不会绕过闸门直接调用远程 Provider；
- 容量协调关闭时，原有单进程开发模式保持兼容。
- 文档任务有效心跳不会被恢复，过期任务能够获得新 fencing token 重新领取；
- 旧 token 不能写 chunks，也不能写成功或失败终态。
- Embedding 有效心跳不会被恢复，过期任务可重新领取且产生不同 token；
- Embedding 旧 token 不能覆盖向量、requeue 或写成功/失败终态。

本地隔离验收使用同一个临时镜像和空 pgvector/PostgreSQL，依次启动五种角色并发送 SIGTERM；五个进程均以
退出码 0 完成清理。API 日志未出现 Python/Worker 组装事件，三个 Worker 日志均显示
`http_enabled=false`；空任务库保证验收过程没有远程 Provider 调用。
