# 2026-08-30 阿里云 OSS 真实验收记录

## 1. 验收结论

本轮真实环境验收通过了两条关键链路：

1. 项目的 Go OSS 适配器能够通过 ECS RAM Role 对私有 Bucket 中的 `documents/*` 对象执行保存、读取、
   物化、释放和删除；
2. Python `rag_ai` 能够解析从 OSS 下载并物化到临时目录的 PDF，返回成功状态、页和文本块。

这证明当前 OSS 存储适配器和 Python 文档解析器可以衔接，但不等于完整产品纵向链路已经验收。HTTP API、
PostgreSQL、独立 Document Worker、任务状态和 OSS 删除补偿在同一次运行中的全链路验收仍未执行。

## 2. 冻结身份

| 项目 | 值 |
| --- | --- |
| 分支 | `codex/object-storage-adapter` |
| OSS 运行代码提交 | `1ea6b030742766131131290016d59755aa6a2852` |
| 真实集成门禁提交 | `756a2c157a86f09be6bb4dadd212d29b1ae291de` |
| 本地后端镜像 | `rag-reasoning-platform-backend:local` |
| 镜像 ID | `sha256:9cd6288cbbe8d13112374cf069b2898b027325afa4700c2aaf7c5580c6096457` |
| 镜像大小 | `66141065` 字节 |
| Bucket | `rag-reasoning-platform-individual-test-368642305` |
| Region | `cn-shanghai` |
| ECS Endpoint | `https://oss-cn-shanghai-internal.aliyuncs.com` |
| ECS RAM Role | `RagReasoningPlatformTestEcsRole` |
| RAM Policy | `RagReasoningPlatformDocumentsTestAccess` |

Bucket 保持私有并启用“阻止公共访问”。策略只允许对该 Bucket 的 `documents/*` 执行 `PutObject`、
`GetObject` 和 `DeleteObject`。验收过程中未输出、复制或记录 AccessKey Secret，也没有开放 ECS 端口、调用模型
或安装系统软件。

## 3. 验收过程与证据

### 3.1 本地构建

执行 `docker compose build backend` 构建后端镜像。首次访问 Docker Hub 时出现 EOF，重试后构建成功。
这属于镜像源网络波动，不是项目代码错误。

### 3.2 Markdown 对象生命周期

测试对象：

```text
documents/acceptance-20260830T214800Z-1ea6b03.md
```

结果：保存成功；读取内容一致；物化到临时文件成功；释放后临时文件消失；删除成功；删除后读取稳定返回
HTTP 404 / Object Not Found。

### 3.3 PDF 与 Python 解析衔接

测试对象：

```text
documents/acceptance-python-20260830T220000Z-1ea6b03.pdf
```

PDF 从 OSS 物化后交给冻结版本的 `rag_ai` CLI。返回 `status=succeeded`、1 页、1 个 chunk，并提取到预期
文本 `OSS Python acceptance`。随后临时文件和 OSS 对象均被删除，删除后读取返回 404。

### 3.4 项目 Go 适配器真实门禁

由于 ECS 无 Go 环境且 `go.dev`、`dl.google.com` 的官方发行包下载受阻，本地从提交 `756a2c1` 交叉编译
Linux amd64 测试二进制，再通过 Workbench 上传。未使用第三方 Go 镜像源。

| 证据 | 值 |
| --- | --- |
| 测试二进制大小 | `11041888` 字节 |
| SHA-256 | `543F1384E14B42969E671A6DC6ED7182EA0110E3BC623AAFBA37A19531AFBD5A` |
| 实际测试对象 | `documents/document-00047e4ee59421a3067ed716f15d34bd.md` |
| 测试结果 | `PASS`，约 `0.21s` |

远端 SHA-256 与本地一致。`TestAliyunOSSObjectStorageIntegration` 通过 ECS RAM Role 直接运行项目中的
`AliyunOSSObjectClient` 和 `ObjectStorage`，覆盖：

```text
Save -> Open -> Materialize/release -> Delete -> 删除后不存在
```

删除后错误被稳定归一化为 `HTTP 404 / OBJECT_NOT_FOUND`。远端测试对象、二进制和临时目录均已清理。

## 4. 已通过与未覆盖边界

已通过：

- 私有 Bucket 与 ECS RAM Role 最小权限连通；
- Go OSS SDK 适配器的真实对象生命周期；
- OSS 临时物化文件的创建和释放；
- Python 对真实 OSS 物化 PDF 的解析；
- 对象删除和删除后 404 识别；
- 本地 Linux 后端镜像构建。

尚未覆盖：

- HTTP 上传接口、PostgreSQL、Document Worker、Python、chunks 写入和 OSS 删除补偿的单次完整纵向验收；
- 删除文档时数据库记录、chunks、vectors 和 OSS 对象的一致性；
- 历史本地文件迁移到 OSS；
- ECS RAM Role 长时间运行时的临时凭证自动刷新；
- 大文件、并发上传、网络超时、限流和权限撤销；
- 非 `documents/*` 路径的显式拒绝探针。

因此当前状态应表述为“真实 OSS 适配器与解析衔接已通过”，不能表述为“OSS 生产链路全部完成”。
