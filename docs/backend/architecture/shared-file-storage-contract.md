# 共享文件存储契约

## 1. 核心目的

本阶段先冻结本地文件存储与未来对象存储共同遵守的代码边界，不直接接入 COS、S3 或任何收费云服务。
完成后，Application 与 Domain 只认识“不透明存储键”和最小读写能力，不再假设原始文件一定在当前进程的
本地磁盘上。

这一步解决的是“以后如何安全替换存储实现”，还没有解决“不同主机现在已经能够共享文件”。真正的跨主机
能力仍要等对象存储实现、配置和部署验收完成后才能成立。

## 2. 当前四条文件调用链

### 上传

```text
HTTP multipart
→ UploadService
→ FileSaver.Save
→ 返回 StoragePath、可信 MIME、实际大小和 SHA-256
→ PostgreSQL 创建或取得同一 Owner 的文档记录
→ 重复内容或数据库失败时 StoredFileDeleter.Delete 补偿清理
```

### Go 文本处理

```text
Document.StoragePath
→ StoredFileOpener.Open
→ TextProcessor 流式读取 Markdown/TXT
→ Close
```

### Python 复杂文档处理

```text
Document.StoragePath
→ StoredFileMaterializer.Materialize
→ 得到本次调用可读取的本地绝对路径 + release
→ Python CLI / Python Process Pool
→ 无论成功、失败或取消都调用 release
```

LocalStorage 的文件已经在本机，因而直接返回受控绝对路径，`release` 是非空的空操作。ObjectStorage 会把对象
下载到受控临时目录，返回保留原扩展名的临时文件绝对路径，并在 `release` 中删除临时文件。Python 不直接认识
COS SDK 或对象键。

### 删除

```text
DeleteService 读取 OwnerScope 下的文档
→ StoredFileDeleter.Delete 幂等删除文件实体
→ PostgreSQL 删除文档及级联数据
```

文件系统与 PostgreSQL 仍不是一个真正的原子事务。对象存储阶段如需更强一致性，应继续评估软删除、待清理
状态或 Outbox，不能把跨系统操作误称为数据库事务。

## 3. 稳定端口

端口定义在 `backend/internal/application/document/storage.go`，具体用例只依赖自己需要的最小能力。

| 端口 | 输入 | 输出 | 使用方 |
| --- | --- | --- | --- |
| `FileSaver` | `context`、原始文件名、`io.Reader` | `StoredFile` | 上传用例 |
| `StoredFileOpener` | `context`、存储键 | `io.ReadCloser` | Markdown/TXT 处理器 |
| `StoredFileDeleter` | `context`、存储键 | `error` | 上传补偿、删除用例 |
| `UploadFileStorage` | 组合 Save + Delete | 同上 | 上传用例 |
| `FileStorage` | 组合 Save + Open + Delete | 同上 | 实现完整性编译检查 |

`StoredFileMaterializer` 是 Python 子进程专用的 Infrastructure 边界，定义在
`backend/internal/infrastructure/pythonprocessor/materializer.go`。它没有放进通用 Application 端口，是因为
“必须落成本地绝对路径”属于当前 Python 子进程运行方式，不是业务层对文件存储的普遍要求。

## 4. StoragePath 语义

`Document.StoragePath` 和 `StoredFile.StoragePath` 统一解释为存储实现生成的不透明键：

- Domain、Application 可以保存、比较和传递它；
- Domain、Application 不得使用 `filepath.Join`、截取盘符或推断目录结构；
- LocalStorage 可以把它解析为根目录内的安全路径；
- 对象存储可以把它解释为 bucket 内的 object key；
- 面向前端的 DTO 不应暴露宿主机绝对路径或云存储凭据。

这条约束让 `documents.storage_path` 无需为了切换实现立即改名或迁表；真正切换时仍应确认历史 Local key 与新
Object key 的区分、迁移和回滚策略。

## 5. Materialize 生命周期和错误语义

Materializer 必须返回：

1. Python 当前进程可读取的绝对路径；
2. 永远非 `nil` 的 `release func() error`；
3. 物化失败时的错误。

调用方保证：

- 路径为空或不是绝对路径时拒绝调用 Python；
- 实现返回了无效路径时仍尝试释放已经创建的本地资源；
- Python 成功、失败、超时或上下文取消后都执行 `release`；
- 处理错误与清理错误同时发生时保留两者；
- 清理失败时不返回看似成功的 chunks，避免临时资源泄漏被静默掩盖。

Process Pool 先取得一个有界 Worker 槽位，再物化源文件。对象下载因此也被当前 Worker 并发上限约束，不会在
大量排队任务到来时无界创建临时下载。后续如果指标证明下载成为独立瓶颈，可以再引入单独的预取闸门；第一版
不提前增加另一套并发系统。

## 6. 已完成的零费用验证

- LocalStorage 通过统一 Save/Open/Delete 行为契约；
- ObjectStorage 通过同一套 Save/Open/Delete 行为契约，具体 SDK 被 `ObjectClient` 隔离；
- Local 与 Object 共用 `stageDocumentUpload`，PDF、UTF-8、大小和 SHA-256 规则不会分叉；
- 同一原始文件名多次保存会生成不同键；
- Delete 保持幂等，删除后 Open 失败；
- LocalStorage 物化结果是绝对路径，release 不删除正式文件；
- ObjectStorage 物化会下载受控临时副本，release 幂等删除本地副本但保留正式对象；
- Fake 客户端模拟远端部分写入后失败，适配器会补偿删除可能存在的孤立对象；
- 远端对象异常超限、对象不存在、非法键和上下文取消均不会留下本地暂存文件；
- 取消上下文和非法存储键不能绕过路径安全；
- Python 一次性进程和常驻 Process Pool 都会在每次处理后调用 release；
- 无效物化路径、缺失 release、处理失败与清理失败组合均有单元测试。

### 当前 ObjectStorage 边界

`ObjectStorage` 已经是可工作的生产侧适配器骨架，但当前只通过测试 Fake 驱动。`ObjectClient` 规定
`PutObject/GetObject/DeleteObject` 和稳定 `ErrObjectNotFound`；未来腾讯 COS 或 S3 SDK 只在这个端口后实现。
正式对象使用 128 位随机标识生成 `documents/document-<id>.<ext>` 键，不包含用户原始文件名。Owner 权限仍由
PostgreSQL 文档归属和 Application OwnerScope 保证，对象键本身不是授权凭据，也不返回给浏览器使用。

## 7. 下一阶段

1. 选择并实现一个具体对象客户端，第一候选为腾讯 COS；
2. 增加显式存储类型、endpoint、bucket、region、凭据来源和暂存目录配置，缺失配置必须 fail-fast；
3. 先用本地兼容服务或显式授权的测试 bucket 验证 SDK 错误映射、超时和大文件流式行为；
4. 明确 Local/Object 历史键的识别、迁移和回滚策略；
5. 通过不同进程、不同文件根目录的验收证明 Worker 不再依赖 API 本地磁盘；
6. 最后再讨论浏览器预签名直传、分片上传和真实 COS 成本。

本阶段不修改 HTTP DTO、状态码、前端上传方式、数据库表结构或文档 OwnerScope 规则。
