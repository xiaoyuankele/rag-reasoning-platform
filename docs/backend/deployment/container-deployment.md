# 后端容器部署指南

## 1. 这一阶段解决什么问题

P5.3.1 把已经能在开发机运行的后端封装成可重复构建的运行单元。当前 Go Worker 会按需启动
Python 子进程处理 PDF，因此第一版不是“纯 Go 镜像”，而是在同一个后端镜像中放入：

- 编译后的 Go HTTP 服务与后台 Worker；
- Python 3.11.16；
- 项目的 `rag_ai` 包、`pypdf` 和加密 PDF 所需依赖。

PostgreSQL 继续作为独立 Compose 服务。该组合符合当前个人版的低复杂度边界；只有并发、扩缩容或
故障隔离形成真实需求后，才考虑把 Python 拆成常驻 HTTP 服务。

```text
宿主机 127.0.0.1:BACKEND_HOST_PORT
                 │
                 ▼
backend 容器：Go API/Worker ──按需子进程──> Python rag_ai
                 │
                 │ Compose 内网 postgres:5432
                 ▼
PostgreSQL 容器：表、任务、chunks、vectors

宿主机 ./storage <────绑定挂载────> backend:/app/storage
Docker 数据卷   <────命名卷──────> postgres:/var/lib/postgresql/data
```

## 2. 构建和启动前准备

1. 在项目根目录复制 `.env.example` 为 `.env`；
2. 至少把 `DB_PASSWORD` 改成真实的本机数据库密码；
3. `.env` 已被 Git 忽略，不能提交真实密码或 API Key；
4. 日常无远程费用运行时保持以下开关为 `false`：

```dotenv
EMBEDDING_WORKER_ENABLED=false
SEMANTIC_SEARCH_ENABLED=false
ANSWER_ENABLED=false
```

`DB_HOST=localhost` 和 `DB_PORT=5433` 是宿主机直接运行 Go 时使用的值。Compose 会在容器内覆盖为
`postgres:5432`，因为容器不能通过自己的 `localhost` 访问另一个容器。

正常运行保持 `STORAGE_HOST_PATH=./storage`。它只决定哪个宿主机目录绑定到容器 `/app/storage`；
隔离恢复验证成功后，可以把它切换到新的恢复目录，而不覆盖旧文件。

## 3. 构建与启动

所有命令都从项目根目录执行：

```powershell
docker compose config --quiet
docker compose build backend
docker compose up -d backend
docker compose ps
```

`docker compose up -d backend` 会自动启动它依赖的 PostgreSQL，并等待数据库健康后再启动后端。
启动时后端会执行嵌入二进制的数据库迁移；迁移本身有版本、校验和、事务与 advisory lock 保护。

## 4. 健康检查与排障

默认宿主机端口是 `8080`：

```powershell
curl.exe -i http://127.0.0.1:8080/health
docker compose logs --tail 100 backend
docker compose ps
```

成功标准：

- `rag_reasoning_postgres` 与 `rag_reasoning_backend` 都显示 `healthy`；
- HTTP 返回 `200 OK` 和 `{"status":"ok"}`；
- 后端日志没有启动失败或数据库连接错误。

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
docker compose stop backend
```

需要停止数据库时：

```powershell
docker compose stop postgres
```

上述命令不会删除容器或数据卷。`docker compose down` 会删除 Compose 容器和网络，但默认保留命名卷；
`docker compose down -v` 会连同 PostgreSQL 命名卷一起删除，除非已经确认不需要数据或完成可靠备份，
否则不要执行。

后端镜像显式使用 `SIGTERM`，Compose 使用 init 进程转发信号，并提供 30 秒停止宽限期。Go 会先停止 HTTP、
再等待 Worker、最后关闭数据库连接池；异常退出遗留任务的恢复规则和可重复验收命令见
[容器优雅关闭与异常恢复](container-lifecycle-and-recovery.md)。

## 7. 资源与权限约束

- 后端容器限制为 768 MiB 内存和 1.5 CPU；PostgreSQL 限制为 256 MiB 和 1 CPU；
- 后端以固定 UID `10001` 的 `appuser` 运行，不使用 root；
- Windows Docker Desktop 的目录绑定通常可以直接写入；Linux 部署时必须让 UID `10001` 对宿主机
  `storage/` 目录拥有读写权限；
- 服务默认只绑定宿主机 `127.0.0.1`。当前版本没有登录、鉴权和租户隔离，不能直接暴露到公网。

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
