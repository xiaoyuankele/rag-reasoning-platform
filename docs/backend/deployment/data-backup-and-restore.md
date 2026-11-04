# 数据配套备份与恢复指南

## 1. 核心目的

完整业务数据同时存在于两个位置：

- PostgreSQL：用户、Session、文档元数据、任务、chunks、向量、会话、消息、回答结果和迁移版本；
- `storage/`：PDF、Markdown、TXT 等原始文件。

只备份数据库会得到指向不存在文件的 `storage_path`；只备份文件则会丢失文档 ID、处理状态、
文本块和向量。因此 P5.3.2 把两部分冻结在同一个维护窗口内，并使用 `manifest.json` 把它们组成
一个可校验的备份单元。

## 2. 备份产物

默认输出目录是已被 Git 忽略的：

```text
chatgpt/运行产物/备份/backend-data/rag-platform-<UTC 时间>/
├── database.dump
├── storage.tar.gz
└── manifest.json
```

新备份使用 `format_version=3`。`manifest.json` 包含：

- 格式版本、完成状态、UTC 时间、当前 Git commit 和 `git_worktree_dirty`；
- 数据库名、数据库用户、`schema_migration_version`、dump 大小和 SHA-256；
- 原有 `documents`、`document_jobs`、`text_chunks`、`embedding_jobs`、`chunk_embeddings` 行数；
- 长期用户与会话数据 `users`、`user_sessions`、`conversations`、`conversation_messages`、
  `answer_jobs` 行数；
- storage 归档大小、SHA-256、原始文件数量和原始总字节数。

脚本最后才写入 manifest。目录中没有 `manifest.json`，或者状态不是 `complete`，都表示备份没有
完整结束，恢复脚本会拒绝使用。

## 3. 执行备份

从项目根目录执行：

```powershell
powershell.exe -NoProfile -ExecutionPolicy Bypass `
    -File .\scripts\maintenance\backup-data.ps1
```

当前电脑使用 Windows PowerShell 5.1，并且系统执行策略禁止直接运行本地 `.ps1`。上述
`-ExecutionPolicy Bypass` 只对这一次新进程生效，不会修改系统的永久执行策略。两个脚本使用带 BOM 的
UTF-8 保存，以保证 PowerShell 5.1 可以正确读取中文路径和注释。

备份脚本按以下顺序工作：

1. 检查 Docker、tar、Git、Compose 和 PostgreSQL 健康状态；
2. 发现并暂停正在运行的 `backend`、`document-worker`、`embedding-worker` 和 `answer-worker` 写入服务；
3. 在 PostgreSQL 容器内使用 custom-format `pg_dump` 取得一致快照；
4. 使用 `pg_restore --list` 验证 dump 可以读取；
5. 使用 tar 归档整个 `storage/`；
6. 计算两份数据的 SHA-256、文件统计和数据库表行数；
7. 最后写入 `manifest.json`；
8. 如果写入服务原本正在运行，则按原运行集合自动恢复它们。

自定义备份位置：

```powershell
powershell.exe -NoProfile -ExecutionPolicy Bypass `
    -File .\scripts\maintenance\backup-data.ps1 `
    -OutputRoot "D:\rag-backups"
```

`OutputRoot` 不能位于 `storage/` 内部，否则归档可能把正在生成的备份再次包含进自己。需要备份完成后
继续保持维护停机时，可以显式增加 `-KeepBackendStopped`。参数名为兼容旧命令保留；启用后，本次由脚本
暂停的 API 和 Worker 写入服务都会保持停止。

## 4. 默认不覆盖的恢复演练

恢复脚本要求：

- 目标数据库必须不存在；
- 目标目录必须不存在；
- 目标目录不能放在备份目录内部；
- dump 与 storage 归档的 SHA-256 必须匹配 manifest；
- tar 中所有路径必须位于 `storage/` 下；
- v1 旧 manifest 只校验原有五张核心表；v2/v3 manifest 还必须校验用户、Session、会话、消息和回答任务行数；
- v2/v3 恢复后必须通过会话关联完整性门禁：Answer Job 的三个关联 ID 要么全部为空、要么全部存在；关联的
  user/assistant 消息必须属于同一会话且角色正确；成功任务的 sources 和三项 Token 字段必须完整；
  `uq_answer_jobs_user_message` 唯一索引必须存在；
- requested document snapshot 列存在时，live `document_id` 非空的 Job 必须满足
  `requested_document_id = document_id`；迁移版本不低于 35 但缺少该列时恢复失败；
- v2/v3 中每个 standalone succeeded Job 的 source 必须包含合法的 `DocumentID` 或 `document_id`，引用文档必须
  存在且属于 Job Owner；两个键同时存在时其值必须一致。非法 ID、缺失文档和跨 Owner 引用分别计数，任一非零都拒绝恢复；
- v3 必须校验恢复后的 `MAX(schema_migrations.version)` 与 manifest 完全一致；
- 文件数量和文件总字节数必须匹配 manifest。

恢复脚本兼容 `format_version=1`、`2` 和 `3`。v1 只执行原五表和 storage 校验；v2/v3 执行十表与会话门禁，
只要恢复库存在 `requested_document_id` 就同时校验 requested document snapshot；迁移版本低于 35 且没有该列时标记
`not_available`。v3 另增加迁移版本和工作树状态。`git_worktree_dirty=true` 表示
仅靠 manifest 中的 commit 无法复现备份时的未提交代码，必须同时保存对应补丁或先形成正式提交。门禁只输出
表行数、异常计数和索引数量，不输出用户/Owner 标识、问题、消息正文、答案或来源内容。

先取得要恢复的备份路径：

```powershell
$backup = Get-ChildItem `
    -Directory .\chatgpt\运行产物\备份\backend-data |
    Sort-Object Name -Descending |
    Select-Object -First 1
```

再恢复到一个新数据库和新目录：

```powershell
powershell.exe -NoProfile -ExecutionPolicy Bypass `
    -File .\scripts\maintenance\restore-data.ps1 `
    -BackupDirectory $backup.FullName `
    -TargetDatabase rag_platform_recovered `
    -TargetRoot .\chatgpt\运行产物\临时\rag-platform-recovered
```

成功后，新目录中会出现：

```text
rag-platform-recovered/
├── storage/
└── restore-result.json
```

v2/v3 的 `restore-result.json` 会在 `conversation_integrity.standalone_source_privacy` 中记录
`source_entry_count`、`invalid_document_id_count`、`missing_document_count` 和 `owner_mismatch_count`；成功恢复时后三项
必须全部为 0。v1 不读取会话来源，明确记录 `status=not_checked_manifest_v1`。

如果恢复在目标数据库创建后失败，脚本会保留目标数据库和目录用于诊断，不会猜测性地递归删除现场。

## 5. 验证后的无覆盖切换

恢复成功不代表必须立刻覆盖旧环境。第一版推荐保留旧数据，通过 `.env` 切换两个入口：

```dotenv
DB_NAME=rag_platform_recovered
STORAGE_HOST_PATH=./chatgpt/运行产物/临时/rag-platform-recovered/storage
```

然后重建 Compose 服务配置：

```powershell
docker compose stop backend
docker compose up -d --force-recreate postgres
docker compose up -d --force-recreate backend
docker compose ps
curl.exe -i http://127.0.0.1:8080/health
```

PostgreSQL 容器重建只替换容器配置，仍然挂载原命名卷；恢复数据库已经位于该卷内。重新创建 PostgreSQL
可以让 `POSTGRES_DB`、健康检查和后续备份脚本都指向恢复数据库。`STORAGE_HOST_PATH` 只改变宿主机绑定
来源，容器内应用仍然使用 `/app/storage`。

确认文档数量、抽样文件、chunks、向量和主要接口都正常后，才能单独规划旧数据清理。回退时把 `.env`
改回旧 `DB_NAME` 和旧 `STORAGE_HOST_PATH`，再执行相同的 Compose 重建命令。不要在切换当天删除旧数据。

## 6. 安全边界

- 备份使用 `pg_dump`，不直接复制运行中的 PostgreSQL 物理数据卷；
- 恢复不提供自动覆盖、自动 drop 正式数据库或自动递归删除旧文件；
- `database.dump` 可能包含可执行 SQL，只恢复自己创建和可信来源的备份；
- `manifest.json` 用于完整性检查，不是数字签名，不能证明不可信备份的身份；
- 备份与正式数据在同一块硬盘时只能防误操作，不能防硬盘损坏。重要备份还应复制到另一块磁盘或可信
  的加密存储；
- `.env`、API Key 和数据库密码不会写入备份 manifest。

数据库 dump 本身包含私有业务数据，包括密码哈希、Session Token 哈希、用户问题、会话消息、回答和来源快照。
备份目录必须使用受控访问和加密存储；manifest 只记录安全计数，不会降低 dump 本身的敏感级别。

## 7. 在线删除与备份保留期

在线数据删除和备份保留是两套独立生命周期：

- 删除用户、文档、会话或消息，只影响当前在线数据库与 storage，不会改写已经完成的历史备份；
- `ANSWER_STANDALONE_JOB_RETENTION` 清理在线 standalone 终态任务时，也不会删除旧备份中的任务、问题、回答、来源或 Token 统计；conversation-linked Job 随在线会话保留，但同样不会自动改写历史备份；
- 备份必须由单独的保留策略按备份 ID 和创建时间到期删除，不能把在线级联删除误认为备份删除；
- 用户数据删除、合规擦除或密钥泄漏处置必须同时评估仍在保留期内的备份，并记录销毁或到期安排；
- 恢复旧备份可能重新带回在线环境中已经删除的数据。切换恢复库前必须执行删除记录或保留策略对账。

本脚本不会自动删除任何完整备份，也不会根据在线保留期推断备份保留期。

### 7.1 会话迁移 29–37 的维护窗口约束

迁移 29–37 尚未作为正式版本发布。首次部署时必须遵守以下顺序：

1. 停止所有旧版本 answer worker，并确认没有旧 Worker 仍在领取或收尾 Answer Job；
2. 停止会写入会话、文档和 Answer Job 的业务流量；
3. 完成数据库备份，并记录当前 `schema_migrations` 版本；
4. 一次性执行迁移到版本 37，使 migration 36 安装索引和删除触发器，migration 37 完成旧 standalone 来源回填清理；
5. 只启动包含来源 Owner 复核与 document→job 锁顺序的新 Worker；
6. 完成文档删除、standalone 来源隐私和 conversation-linked 结果保留验收后，再恢复业务流量。

不得在旧、新 answer worker 并行运行的滚动窗口中执行 migration 36–37。旧 Worker 不具备 standalone 来源文档的
收尾复核能力，可能在迁移清理之后重新写入不安全结果。

若需要从 37 降级，必须先执行 migration 37 down；该 down 只记录历史隐私清理不可逆，不会伪造恢复被删除的数据。
随后再依次执行 36 → 35 → 34 down。整个降级必须在同一个维护窗口内完成，并保持全程没有业务写流量。migration 35 down
会先对 `answer_jobs` 获取 `ACCESS EXCLUSIVE` 锁，再检查 `requested_document_id` 与 live `document_id` 是否一致；
只要任一任务仍依赖不可变快照，降级会直接失败。不得绕过该门禁或手工删除快照列。降级前必须保留完整数据库备份，
失败后应继续运行当前版本或从备份恢复。

## 8. 真实演练证据（2026-08-15，v1）

使用当前个人库完成了一次真实备份和隔离恢复：

| 项目 | 正式数据 | 恢复结果 |
| --- | ---: | ---: |
| documents | 46 | 46 |
| document_jobs | 45 | 45 |
| text_chunks | 2729 | 2729 |
| embedding_jobs | 8 | 8 |
| chunk_embeddings | 460 | 460 |
| storage 文件 | 45 | 45 |
| storage 原始字节 | 185,226,802 | 185,226,802 |

本次 `database.dump` 约 4.27 MB，`storage.tar.gz` 约 161 MB。恢复后再次使用已存在的目标数据库执行
脚本，脚本返回非零且没有创建新目标目录，证明覆盖保护有效。验收没有调用远程模型 API；验证数据库已
删除，正式数据库、正式 storage、完整备份和 PostgreSQL 数据卷均保留。

该演练使用 v1 manifest，只覆盖原五张核心表。v2 在此基础上增加用户、Session、会话、消息、Answer Job
行数和会话关联完整性门禁。2026-11-01 使用这份历史备份重新执行恢复，v1 兼容分支再次通过；恢复结果明确
标记 `conversation_integrity.status=not_checked_manifest_v1`，没有把旧格式冒充为会话恢复验收。

### 8.1 v2 真实备份与隔离恢复（2026-11-01）

在迁移版本 34 的当时个人库生成并保留了第一份 v2 完整备份
`rag-platform-20261101T123517864Z`，随后恢复到全新的隔离数据库和目录。恢复结果如下：

| 项目 | 正式数据 | 恢复结果 |
| --- | ---: | ---: |
| documents | 96 | 96 |
| document_jobs | 98 | 98 |
| text_chunks | 6713 | 6713 |
| embedding_jobs | 208 | 208 |
| chunk_embeddings | 4661 | 4661 |
| users | 12 | 12 |
| user_sessions | 28 | 28 |
| conversations | 0 | 0 |
| conversation_messages | 0 | 0 |
| answer_jobs | 0 | 0 |
| storage 文件 | 96 | 96 |
| storage 原始字节 | 512,654,081 | 512,654,081 |

会话完整性门禁返回：部分关联 ID 0、无效消息关联 0、孤立消息 0、不完整成功结果 0，且
`uq_answer_jobs_user_message` 唯一索引数量为 1。备份时 API 服务被暂停，结束后恢复并重新达到 healthy。

本次源库的 conversations、conversation_messages 和 answer_jobs 均为 0，因此该演练验证了 v2 manifest、十表
行数、索引和零异常门禁，但尚未覆盖非空会话、关联 Answer Job、来源与 Token 的实际内容恢复。形成非空会话样本后，
需要再执行一次隔离恢复，才能把长期会话数据恢复标记为完整验收。

### 8.2 v3 非空会话 fixture 恢复（2026-11-01）

使用迁移版本 35 的纯 schema 临时源库构造了不含正式业务数据的小型 fixture：1 个用户、1 个文档、1 个会话、
2 条 user/assistant completed 消息和 1 个 succeeded linked Answer Job。Job 含非空 sources、
`prompt_tokens=10`、`completion_tokens=5`、`total_tokens=15`，且 live/requested document ID 一致；storage 只含
1 个 7 字节测试文件。

fixture manifest 为 `format_version=3`，记录 `schema_migration_version=35` 和
`git_worktree_dirty=true`。通过正式 `restore-data.ps1` 恢复到隔离数据库和目录后，十表行数、storage 文件/字节、
会话消息角色、三项关联 ID、Owner 归属、succeeded sources/Token、唯一 user-message 索引和 requested document
snapshot 全部通过；恢复后的迁移版本与 manifest 一致。

该 fixture 有意固定在迁移 35，用于直接覆盖 `requested_document_id` 快照门禁。当前在线数据库和代码迁移头已经推进到
37（migration 36 安装回答来源文档隐私约束，migration 37 清理历史不安全 standalone 来源）；fixture 证据不冒充
schema 37 的完整恢复演练。后续正式 v3 备份会把当时在线库的
`schema_migration_version` 写入 manifest，并在隔离恢复时要求精确一致。

演练完成后，两个 fixture 数据库、fixture 备份目录和恢复目录均已精确清理。正式 v2 完整备份
`rag-platform-20261101T123517864Z` 继续保留，没有进入 fixture 清理范围。

### 8.3 standalone 来源隐私恢复门禁（2026-11-01）

另用隔离 schema 36 fixture 验证来源隐私门禁。正向 v3 fixture 同时包含合法 `DocumentID` 和 `document_id`，恢复后
`source_entry_count=2`，非法 ID、缺失文档和 Owner 不匹配计数均为 0。三个负向 fixture 分别覆盖：

- v2：字符串 ID 与超出 bigint 上界的 JSON number，稳定报错 `restored standalone answer sources contain invalid document IDs`；
- v3：来源文档不存在，稳定报错 `restored standalone answer sources reference missing documents`；
- v3：来源文档属于其他 Owner，稳定报错 `restored standalone answer sources reference documents owned by another user`。

三次负向恢复都在写出 `restore-result.json` 前失败，没有因非法文本触发 bigint cast 异常。演练完成后，源库、四个恢复库、
fixture 备份/恢复目录和容器临时 SQL 均已精确清理；正式 v2 完整备份未被修改。当前仍未构造哈希损坏或结构损坏的 dump
做负向恢复。备份脚本不会自动删除完整备份，备份保留仍需单独治理。
