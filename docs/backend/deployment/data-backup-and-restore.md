# 数据配套备份与恢复指南

## 1. 核心目的

完整业务数据同时存在于两个位置：

- PostgreSQL：文档元数据、任务、chunks、向量和迁移版本；
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

`manifest.json` 包含：

- 格式版本、完成状态、UTC 时间和当前 Git commit；
- 数据库名、数据库用户、dump 大小和 SHA-256；
- `documents`、`document_jobs`、`text_chunks`、`embedding_jobs`、`chunk_embeddings` 行数；
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
2. 如果 backend 正在运行，使用 `docker compose stop backend` 完成优雅停止；
3. 在 PostgreSQL 容器内使用 custom-format `pg_dump` 取得一致快照；
4. 使用 `pg_restore --list` 验证 dump 可以读取；
5. 使用 tar 归档整个 `storage/`；
6. 计算两份数据的 SHA-256、文件统计和数据库表行数；
7. 最后写入 `manifest.json`；
8. 如果 backend 原本正在运行，则自动恢复它。

自定义备份位置：

```powershell
powershell.exe -NoProfile -ExecutionPolicy Bypass `
    -File .\scripts\maintenance\backup-data.ps1 `
    -OutputRoot "D:\rag-backups"
```

`OutputRoot` 不能位于 `storage/` 内部，否则归档可能把正在生成的备份再次包含进自己。需要备份完成后
继续保持维护停机时，可以显式增加 `-KeepBackendStopped`。

## 4. 默认不覆盖的恢复演练

恢复脚本要求：

- 目标数据库必须不存在；
- 目标目录必须不存在；
- 目标目录不能放在备份目录内部；
- dump 与 storage 归档的 SHA-256 必须匹配 manifest；
- tar 中所有路径必须位于 `storage/` 下；
- 恢复后的五张核心表行数、文件数量和文件总字节数必须匹配 manifest。

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

## 7. 真实演练证据（2026-08-15）

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
