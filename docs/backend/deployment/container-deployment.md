# 后端容器部署指南

## 1. 这一阶段解决什么问题

后端被封装成一个可重复构建的镜像，再通过 `APP_ROLE` 启动为不同进程。Document Worker 会按需启动
Python 子进程处理 PDF，因此运行镜像不是“纯 Go 镜像”，而是同时包含：

- 编译后的 Go HTTP 服务与后台 Worker；
- Python 3.11.16；
- 项目的 `rag_ai` 包、`pypdf` 和加密 PDF 所需依赖。

PostgreSQL 继续作为独立 Compose 服务。角色拆分不是微服务：所有进程仍共享同一个数据库契约、任务表和
镜像版本，不增加内部 HTTP 调用。远程模型执行槽位由独立 Redis 协调，查询/答案缓存继续使用另一套 Redis。

```text
浏览器 ──> backend(api) ───────────────┐
                     │ 上传文件         │ PostgreSQL 任务/业务表
                     ▼                  │
宿主机 ./storage <──共享挂载──> document-worker ──> Python rag_ai
                                        │
embedding-worker(Profile) ──────────────┤
answer-worker(Profile) ─────────────────┘

backend / embedding-worker / answer-worker
        └──> redis-coordination（短期 Provider 执行租约）
```

## 2. 构建和启动前准备

1. 在项目根目录复制 `.env.example` 为 `.env`；
2. 至少把 `DB_PASSWORD` 改成真实的本机数据库密码；
3. 为 `VERIFICATION_HMAC_SECRET` 生成至少 32 字节的本机随机密钥；
4. 生产 HTTPS 部署设置 `AUTH_COOKIE_SECURE=true`；本地 HTTP 开发保持 `false`；
5. `.env` 已被 Git 忽略，不能提交真实密码、HMAC 密钥或 API Key；
6. 日常无远程费用运行时保持以下开关为 `false`：

```dotenv
EMBEDDING_WORKER_ENABLED=false
SEMANTIC_SEARCH_ENABLED=false
ANSWER_ENABLED=false
```

`DB_HOST=localhost` 和 `DB_PORT=5433` 是宿主机直接运行 Go 时使用的值。Compose 会在容器内覆盖为
`postgres:5432`，因为容器不能通过自己的 `localhost` 访问另一个容器。

正常运行保持 `STORAGE_HOST_PATH=./storage`。它只决定哪个宿主机目录绑定到容器 `/app/storage`；
隔离恢复验证成功后，可以把它切换到新的恢复目录，而不覆盖旧文件。

需要跨主机共享原始文件时可以把 `FILE_STORAGE_DRIVER` 切换为 `oss`。此时 `/app/storage` 只作为上传校验和
Python 处理的受控暂存目录，正式对象保存在私有 OSS Bucket；API 与 Document Worker 必须使用相同 Bucket、
Region 和对象键规则。ECS 推荐使用 RAM Role 和同地域内网 Endpoint，不在容器环境中保存长期 AccessKey。
完整配置、权限边界和切换前置条件见
[阿里云 OSS 文件存储](aliyun-oss-storage.md)。

## 3. 构建与启动

所有命令都从项目根目录执行：

```powershell
docker compose config --quiet
docker compose build backend
docker compose up -d backend document-worker
docker compose ps
```

基础启动不会创建远程 Worker，因此不会仅因为 Compose 启动而调用模型。确认密钥、费用和功能开关后，分别
启用远程 Profile：

```powershell
docker compose --profile embedding up -d embedding-worker
docker compose --profile answer up -d answer-worker
```

同一台主机需要增加 Worker 进程时，可以使用 Compose 的 `--scale`。三个 Worker 服务故意不设置固定
`container_name`，否则 Compose 无法为副本生成互不冲突的容器名：

```powershell
docker compose up -d --scale document-worker=2 document-worker
docker compose --profile embedding up -d --scale embedding-worker=2 embedding-worker
docker compose --profile answer up -d --scale answer-worker=2 answer-worker
```

每个副本都会运行独立进程和独立 Worker Loop，并通过 PostgreSQL 租约安全领取任务。未显式配置
`*_WORKER_ID` 时，程序会使用容器 hostname 与进程 ID 生成身份；如果自行设置，必须保证每个副本唯一。
数据库连接池也是“每进程一份”，扩容前要按“单进程连接数 × 副本数”核算 PostgreSQL 上限。

local 模式下，同机 Document Worker 共享相同的 `STORAGE_HOST_PATH`，所以能读取 API 保存的原始文件；
oss 模式下，各进程通过统一对象键读取同一私有 Bucket，本地目录不再是正式文件事实来源。

所有角色都会等待 PostgreSQL 健康后启动，并执行嵌入二进制的迁移；迁移通过 advisory lock 串行化，避免
四个进程同时修改 schema。API、Embedding Worker 和 Answer Worker 还会等待 `redis-coordination` 健康；
只有实际启用远程能力时，后端才创建容量客户端并执行启动 Ping。

## 4. 健康检查与排障

默认宿主机端口是 `8080`：

```powershell
curl.exe -i http://127.0.0.1:8080/health
docker compose logs --tail 100 backend
docker compose logs --tail 100 document-worker
docker compose ps
```

成功标准：

- PostgreSQL、`backend` 和所有 `document-worker` 副本都显示 `healthy`；
- `rag_reasoning_redis_coordination` 显示 `healthy`；
- HTTP 返回 `200 OK` 和 `{"status":"ok"}`；
- Document Worker 日志包含 `role=document-worker`、`ready_file_enabled=true`；
- 后端日志没有启动失败或数据库连接错误。

API 使用 HTTP 健康检查。三个 Worker 不监听端口，在完成数据库迁移、任务恢复和 Worker Pool 初始化后写入
`/tmp/rag-role-ready`；Compose 通过该文件判断就绪。不要给 Worker 配置假的 `/health`。

如果本机 `8080` 已被占用，只改宿主机映射端口，不改容器端口：

```powershell
$env:BACKEND_HOST_PORT = "18080"
docker compose up -d backend
curl.exe -i http://127.0.0.1:18080/health
Remove-Item Env:BACKEND_HOST_PORT
```

## 5. 数据持久化边界

| 数据 | 持久化位置 | 重建后端容器后 |
| --- | --- | --- |
| 文档元数据、任务、chunks、vectors | Docker 命名卷 `rag_postgres_data` | 保留 |
| 上传的原始文件 | `STORAGE_HOST_PATH` 指定的宿主机目录，默认 `./storage` | 保留 |
| Go 程序、Python 与依赖 | 后端镜像 | 由镜像重新创建 |
| 日志 | 当前输出到容器标准输出 | 需要 `docker compose logs` 查看；长期保存方案后续补充 |

数据库记录和 `storage/` 文件共同构成完整业务数据。P5.3.2 已建立二者配套的备份、恢复与一致性验收，
不要把“只备份数据库”误认为完整备份。命令和安全切换步骤见
[数据配套备份与恢复指南](data-backup-and-restore.md)。

## 6. 安全停止与危险命令

日常只停止后端：

```powershell
docker compose stop backend document-worker embedding-worker answer-worker
```

需要停止数据库时：

```powershell
docker compose stop postgres
```

上述命令不会删除容器或数据卷。`docker compose down` 会删除 Compose 容器和网络，但默认保留命名卷；
`docker compose down -v` 会连同 PostgreSQL 命名卷一起删除，除非已经确认不需要数据或完成可靠备份，
否则不要执行。

后端镜像显式使用 `SIGTERM`，Compose 使用 init 进程转发信号，并提供 30 秒停止宽限期。API 会先停止 HTTP；
Worker 会先删除就绪文件，再等待 goroutine，最后关闭数据库连接池。异常退出遗留任务的恢复规则见
[容器优雅关闭与异常恢复](container-lifecycle-and-recovery.md)。

## 7. 资源与权限约束

- API、Document、Embedding、Answer 默认分别限制为 768/1024/512/512 MiB，CPU 分别为 2/2/1/1；
- 四类角色默认数据库连接池分别为 5/3/3/5，合计 16，不能只看单进程配置；
- PostgreSQL 本地默认限制为 256 MiB、1 CPU 和 20 个连接；云端配置需要单独测量；
- 查询缓存 Redis 默认 128 MiB、允许 LFU 淘汰；容量 Redis 默认 32 MiB、使用 `noeviction`，二者不能混用；
- 后端以固定 UID `10001` 的 `appuser` 运行，不使用 root；
- Windows Docker Desktop 的目录绑定通常可以直接写入；Linux 部署时必须让 UID `10001` 对宿主机
  `storage/` 目录拥有读写权限；
- 服务默认只绑定宿主机 `127.0.0.1`。P6 已完成个人账户、Session、忘记/重置密码以及个人数据所有者隔离，
  但当前仍是个人版架构，尚未实现团队工作区、租户管理和公网生产环境所需的完整安全运维能力，因此不能直接
  暴露到公网或交给互不信任的多个租户共同使用。

## 8. 已完成验收（2026-08-15）

- `docker compose config --quiet` 通过；
- 多阶段镜像成功构建，最终镜像约 64 MB；
- 最终进程用户为 `appuser`，Gin 默认运行在 release 模式；
- 容器内 Python 3.11.16、`rag_ai` 与 `pypdf 6.14.2` 可导入；
- PostgreSQL 与后端均达到 `healthy`；
- 宿主机真实请求 `GET /health` 返回 `200`；
- 验收期间三个远程 AI 开关均为 `false`，没有调用模型 API；
- 验收结束只移除后端测试容器，PostgreSQL 容器与数据卷未删除。
- `SIGTERM` 正常停止耗时 443 ms、退出码为 `0`；`SIGKILL` 异常停止退出码为 `137`；
- 异常重启后，文档任务由 `processing` 恢复为 `failed`，Embedding 任务由 `processing` 恢复为 `queued`。

## 9. 部署角色验收（2026-08-29）

- 同一临时镜像依次以 `all/api/document-worker/embedding-worker/answer-worker` 启动；
- API 没有创建 Python Pool 或后台 Worker；三个专用 Worker 均未监听 HTTP；
- 三个 Worker 在启动完成后写入就绪文件，SIGTERM 后删除；
- 五种角色均以退出码 0 完成清理；
- 空测试数据库保证验收没有调用真实 Provider；
- 默认 Compose 与全 Profile Compose 均通过 `docker compose config --quiet`。

## 10. 跨进程容量协调验收（2026-08-29）

- 两个独立 Redis 客户端共享同一全局槽位和不同 Owner 槽位；
- 同一 Owner 的第二次申请在 Owner 上限处被拒绝，其他 Owner 可使用剩余全局槽位；
- 全局槽位满后，第三个 Owner 也不能继续进入；
- 未主动释放的短租约在 TTL 后自动回收；
- Application 单元测试覆盖成功释放、容量等待超时、Redis 故障包装和上下文取消；
- 验收使用本地隔离 Redis 与 Fake 下游，没有调用真实 Embedding 或 Generation Provider。

## 11. 后台任务多进程边界（2026-08-29）

- Document、Embedding 与 Answer Worker 已分别使用 PostgreSQL 持久化租约、心跳和 fencing token；
- 多个同类 Worker 进程可以共享队列，只有租约真正过期的 `processing` 任务会被重排；
- 文档 chunks 写入以及向量整批覆盖都核对当前 token，旧 Worker 不能提交陈旧结果；
- 三类任务的 `*_LEASE_DURATION` 与 `*_HEARTBEAT_INTERVAL` 配置均要求心跳周期短于租约时长；
- 原始文件仍是本地绑定目录，跨主机部署前必须改为共享对象存储。
- 迁移 28 后不得混跑不认识租约字段的旧 Worker；升级时先停止旧 Worker，再迁移并启动同一版本的新 Worker。

## 12. 多进程组合验收（2026-08-29）

在本地空数据库中同时启动了 2 个 Document Worker、2 个 Embedding Worker 和 2 个 Answer Worker：

- 六个容器均使用同一镜像，但拥有独立的角色进程、Worker ID 和数据库连接池；
- 第一条 Document Worker 完成迁移后，数据库 schema 达到版本 28；
- 每个容器的 `APP_ROLE` 与预期一致，全部写入 Worker 就绪文件；
- 空任务库和不可达的本地假 Provider 地址共同保证没有产生远程模型调用；
- 六个容器收到 `SIGTERM` 后均以退出码 0 停止，并删除临时容器和临时数据库；
- 验收脚本位于 `scripts/maintenance/verify-worker-multiprocess.ps1`，默认每类角色启动两个副本。

执行命令：

```powershell
powershell.exe -NoProfile -ExecutionPolicy Bypass `
    -File .\scripts\maintenance\verify-worker-multiprocess.ps1 `
    -ReplicasPerRole 2
```

`-SkipBuild` 仅用于已经明确构建过当前工作区镜像的情况。该验收证明同机进程隔离、启动和退出边界正确，
不等价于生产负载、跨主机共享存储或真实 Provider 压测。
