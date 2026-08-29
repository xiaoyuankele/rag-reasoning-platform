# 容器优雅关闭与异常恢复

## 1. 这一阶段解决什么问题

后端不仅要“能够启动”，还必须处理两种停止方式：

- 正常停止：部署者执行 `docker compose stop backend`，容器收到 `SIGTERM`；
- 异常中断：进程崩溃、宿主机断电或被 `SIGKILL` 强制终止，程序没有机会执行清理代码。

P5.3.3 固化了容器信号、停止宽限期、Go 进程退出日志和启动恢复验收。它不新增业务接口，解决的是部署后的
进程生命周期可靠性。

## 2. 正常停止链路

```text
docker compose stop backend
        ↓ SIGTERM
Go signal.Context 被取消
        ↓
HTTP Server 停止接收新请求，最多等待 10 秒完成在途请求
        ↓
取消 Worker 子 context，并等待 Worker goroutine 退出
        ↓
关闭 PostgreSQL 连接池
        ↓
进程返回 exit code 0
```

`main.go` 是组合根，也是进程生命周期入口，所以信号接收、HTTP 关闭、Worker 等待和资源释放应放在这里；
文档上传、检索等业务规则仍然留在 Application，而不是写入 `main.go`。

Compose 和镜像使用三个配套设置：

| 设置 | 作用 |
| --- | --- |
| Dockerfile `STOPSIGNAL SIGTERM` | 固定镜像约定的停止信号 |
| Compose `init: true` | 转发信号并回收可能遗留的 Python 子进程 |
| Compose `stop_grace_period: 30s` | 给 HTTP 最多 10 秒及后续 Worker/数据库清理留出余量，之后 Docker 才强制终止 |

正常退出日志至少包含：

- `application_started`：HTTP 服务已开始监听；
- `application_shutdown_started`：收到取消信号并开始关闭；
- `application_stopped` 且 `outcome=graceful`：HTTP、Worker 和数据库资源清理已经结束。

## 3. 异常中断为何需要启动恢复

`SIGKILL` 不能被程序捕获。进程不会执行 `defer`、HTTP Shutdown 或 Worker 收尾，因此数据库中可能残留
`processing` 任务。服务下次启动时必须在新 Worker 领取任务前恢复这些记录。

当前两条任务生命周期采用不同策略：

| 任务类型 | 启动前状态 | 恢复后状态 | 原因 |
| --- | --- | --- | --- |
| 文档解析 | `processing` 且租约过期 | 任务回到 `queued`，文档暂时变为 `failed`，随后可重新领取 | chunks 与终态均受 fencing token 保护，旧 Worker 已不能覆盖新结果，因此可以自动接管 |
| Embedding | `processing` 且租约过期 | 任务回到 `queued`，文档仍为 `ready` | 向量事务与终态受 fencing token 保护，旧 Worker 不能覆盖接管后的结果 |

恢复错误信息使用稳定安全文本：

- 文档解析：`document processing lease expired and was requeued`；
- Embedding：`embedding job lease expired and was requeued`。

文档解析和 Embedding 都已使用 PostgreSQL 持久化 lease、heartbeat、Worker 身份和随机 fencing token，
只恢复真正过期的任务；这允许多个同类 Worker 进程共享各自队列。Answer 仍保留原有恢复模型，在完成同类
租约升级前不能任意增加 Answer Worker 的多实例数量。

## 4. 可重复验收脚本

前置条件：

- PostgreSQL Compose 服务正在运行且为 `healthy`；
- 开始前不存在 backend 容器；
- 数据库中没有真实 `processing` 文档任务或向量任务。

从项目根目录执行：

```powershell
powershell.exe -NoProfile -ExecutionPolicy Bypass `
    -File .\scripts\maintenance\verify-container-lifecycle.ps1
```

脚本会执行以下动作：

1. 强制关闭 Embedding Worker、在线语义检索和问答，保证零远程模型费用；
2. 从当前工作区重建镜像，在宿主机 `18080` 启动隔离后端；
3. 使用 `docker compose stop backend` 验证 `SIGTERM`、退出码和生命周期日志；
4. 创建两条具有唯一 storage path 的临时 `processing` 任务；
5. 使用 `SIGKILL` 模拟无法收尾的进程死亡，并验证退出码 `137`；
6. 重新启动同一个容器，核对文档与 Embedding 的不同恢复状态和恢复日志；
7. 精确删除两条临时文档及其级联任务，并移除后端测试容器。

脚本拒绝在已有 backend 容器或真实在途任务存在时开始，不会为了测试而覆盖这些状态。JSON 结果和完整日志写入
Git 已忽略的目录：

```text
chatgpt/运行产物/临时/container-lifecycle-<UTC 时间>.json
chatgpt/运行产物/日志/container-lifecycle-<UTC 时间>.log
```

## 5. 2026-08-15 真实验收结果

- 正常停止耗时 443 ms，退出码为 `0`，且未被 OOM Kill；
- 日志存在 `application_shutdown_started` 与 `application_stopped`；
- `SIGKILL` 后容器退出码为 `137`；
- 重启后文档及其任务均为 `failed`；
- 重启后 Embedding 文档仍为 `ready`，任务恢复为 `queued`，`started_at` 已清空；
- 日志存在 `processing_jobs_recovered` 与 `embedding_jobs_requeued`；
- 两条临时数据已删除，后端测试容器已移除，正式 PostgreSQL 容器和数据未删除；
- 三个远程 AI 开关均为 `false`，没有调用模型 API。

本次结果文件位于：

```text
chatgpt/运行产物/临时/container-lifecycle-20260815T075151Z.json
```

## 6. 2026-08-29 租约升级说明

第 5 节是旧版单进程恢复的历史证据；`verify-container-lifecycle.ps1` 仍冻结该历史断言，不能用它证明当前
拆分角色和文档租约门禁。当前租约正确性由真实 PostgreSQL 集成测试
`TestProcessingJobLeaseRecoveryAndFencing` 验证，覆盖有效续租、到期重领、新 fencing token、旧 Worker
chunks 写入拒绝和旧 Worker 终态写入拒绝。容器级异常退出脚本需要在后续将 API 与 `document-worker`
作为两个角色分别验收，更新前不得把旧脚本结果当作当前发布证据。

## 7. 2026-08-29 Embedding 租约升级说明

Embedding Worker 现在会在领取事务中写入 `worker_id`、随机 `lease_token`、`lease_expires_at` 和
`heartbeat_at`。远程批次调用期间由独立 goroutine 续租；向量覆盖、任务成功、延迟重试和永久失败都必须
携带仍有效的 token。向量覆盖与成功终态仍处在同一事务中，因此过期旧 Worker 既不能写半份向量，也不能
改变新 Worker 的状态。

恢复器只重排真正过期或升级前没有租约的 `processing` 任务。Application 在启动和每次领取前执行恢复，
PostgreSQL 使用部分索引与 `SKIP LOCKED` 支持多个实例同时检查。默认租约 60 秒、心跳 15 秒；Fake Provider
测试验证处理期间续租和续租失败停止写入，真实 PostgreSQL 测试验证过期重领与旧 token 拒绝。
