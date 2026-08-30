# 阿里云 OSS 文件存储

## 1. 这一阶段解决什么问题

API 与 Document Worker 分进程或分主机运行后，`local` 驱动只能看到各自机器的磁盘。`oss` 驱动把正式原始
文件放入同一个私有 Bucket，并继续向 Application 暴露相同的保存、打开和删除接口。Python 处理器需要真实
文件路径时，Infrastructure 会把对象临时下载到 `STORAGE_ROOT`，调用结束后强制清理。

本阶段没有修改 Domain、Application、HTTP DTO 或数据库 `storage_path` 语义。`storage_path` 仍是不透明对象键，
第一版只允许 `documents/document-<随机值>.pdf|md|txt`。

## 2. 已配置的测试资源

| 项目 | 值 |
| --- | --- |
| Bucket | `rag-reasoning-platform-individual-test-368642305` |
| Region | `cn-shanghai` |
| 公网 Endpoint | `https://oss-cn-shanghai.aliyuncs.com` |
| ECS 内网 Endpoint | `https://oss-cn-shanghai-internal.aliyuncs.com` |
| RAM Policy | `RagReasoningPlatformDocumentsTestAccess` |
| ECS RAM Role | `RagReasoningPlatformTestEcsRole` |

Bucket 使用标准存储、本地冗余 LRS、私有 ACL，并保持“阻止公共访问”开启。Policy 仅允许对该 Bucket 的
`documents/*` 执行 `oss:PutObject`、`oss:GetObject` 和 `oss:DeleteObject`，没有列举 Bucket、修改 ACL 或访问
其他前缀的权限。

## 3. ECS 配置

同地域 ECS 使用内网 Endpoint 和 RAM Role 临时凭证：

```dotenv
FILE_STORAGE_DRIVER=oss
STORAGE_ROOT=storage
OSS_BUCKET=rag-reasoning-platform-individual-test-368642305
OSS_REGION=cn-shanghai
OSS_ENDPOINT=https://oss-cn-shanghai-internal.aliyuncs.com
OSS_CREDENTIAL_MODE=ecs_ram_role
OSS_ECS_RAM_ROLE=RagReasoningPlatformTestEcsRole
```

官方 SDK 会从 ECS 元数据服务取得并自动刷新临时凭证。不要再给该容器设置长期
`OSS_ACCESS_KEY_ID` / `OSS_ACCESS_KEY_SECRET`。

## 4. 本机配置

本机无法使用 ECS RAM Role，需要另行创建只绑定同一最小权限 Policy 的测试身份，并通过被 Git 忽略的
`.env` 或进程环境注入：

```dotenv
FILE_STORAGE_DRIVER=oss
STORAGE_ROOT=storage
OSS_BUCKET=rag-reasoning-platform-individual-test-368642305
OSS_REGION=cn-shanghai
OSS_ENDPOINT=https://oss-cn-shanghai.aliyuncs.com
OSS_CREDENTIAL_MODE=environment
OSS_ACCESS_KEY_ID=<仅保存在本机>
OSS_ACCESS_KEY_SECRET=<仅保存在本机>
# 使用 STS 临时凭证时再设置：
OSS_SESSION_TOKEN=
```

任何日志、测试报告、提交、截图或聊天记录都不得包含 Secret。配置加载只验证凭证是否存在，不把凭证值保存到
项目配置结构体；实际签名由官方 OSS SDK 完成。

## 5. 运行与切换边界

- `FILE_STORAGE_DRIVER=local` 是默认值，现有本地开发行为不变；
- `FILE_STORAGE_DRIVER=oss` 时，API 与 Document Worker 必须配置相同 Bucket 和 Region；
- ECS 与本机可以使用不同 Endpoint 和凭证来源，但对象键必须相同；
- 启动过程不会主动执行 `ListObjects` 或 `HeadBucket`，因为最小权限没有授予这些操作；
- 第一次真实上传、读取、解析和删除纵向验收前，不能把“SDK 客户端已接入”等同于“生产可用”；
- 切换驱动不会自动复制历史 `storage/` 文件。必须先把数据库引用的历史文件按原 `storage_path` 上传到
  `documents/*`，验收数量、大小与 SHA-256 后才能切换；
- OSS 故障不能静默回退到 local。否则 API 和 Worker 会把正式文件写到不同事实来源，造成数据库记录存在但
  其他进程看不到文件。

## 6. 当前测试边界

默认自动化测试使用 Fake OSS API 验证请求映射、内容流、SHA-256 元数据、读取器生命周期、404 归一化、幂等
删除和 main 组装，不调用真实 Bucket、不消耗 OSS 请求费用。显式设置 `RUN_OSS_INTEGRATION_TESTS=1` 后，
`aliyun_oss_client_integration_test.go` 才会使用当前环境配置验证真实 Bucket，并在失败时兜底删除随机测试对象。
真实纵向验收必须单独获得授权，并至少覆盖：

1. 上传 PDF/Markdown/Text 后对象位于 `documents/*`；
2. Document Worker 能从另一进程物化并完成解析；
3. 删除文档会同时删除数据库、chunks、vectors 和 OSS 对象；
4. 非 `documents/*` 操作被 RAM Policy 拒绝；
5. ECS RAM Role 临时凭证刷新后仍可继续处理任务；
6. OSS 超时、权限拒绝和对象缺失具有可排查日志且不会留下错误成功状态。

2026-08-30 已完成真实 ECS RAM Role、私有 Bucket、项目 Go 适配器和 Python PDF 解析衔接验收。证据、限制和
尚未覆盖的完整产品链路见 [阿里云 OSS 真实验收记录](aliyun-oss-acceptance-2026-08-30.md)。
