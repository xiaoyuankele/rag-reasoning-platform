# 多轮会话 M2 第一片：前端回答任务交互验收

> 验收日期：2026-11-01
> 范围：任务恢复、游标分页、回答参数、来源与 Token、会话管理、轮询、取消、再次提交和容量退避
> 状态：前后端代码已分别提交为 `d64170c` 和 `e1977e5`；真实 PostgreSQL、本地 HTTP 及三轮真实 `qwen3.6-flash` Generation 已通过。SSE 和历史来源快照不在本片范围

## 1. 本片目标

在已经完成的 M1 会话与消息持久化、M2 后端 Answer Job 关联之上，补齐前端可观察的任务状态交互：

- 用户可以看到回答任务处于排队、生成中、完成、失败或取消状态；
- `queued` 任务可以通过现有取消接口取消；
- Worker 已领取任务后，前端不强行改写状态，继续轮询并明确提示不可取消；
- 刷新或重新进入会话时，前端恢复随会话保留的 latest linked Answer Job；活动任务继续轮询，终态任务恢复结果或重试；
- 会话列表和消息历史支持逐页加载，消息默认从最新 50 条向更早历史展开；
- 用户可以选择全部/单篇文档、`top_k` 和回答语言，成功任务展示来源与 Token；
- 失败或取消后可以再次提交原问题，但必须创建新的会话 turn 和新的 `client_message_id`；
- 问答队列容量错误遵守后端稳定 code 和 `Retry-After`，冷却期间禁止重复提交。

## 2. 实现边界

### 已实现

- `web/src/features/conversations/api/conversation-api.ts`
  - 新增 `cancelConversationAnswerJob`；
  - 新增 `getActiveConversationAnswerJob`，按会话处理 `200` 活动任务和 `204` 无活动任务；
  - 新增 `getLatestConversationAnswerJob`，恢复 queued/processing 和成功/失败/取消终态；
  - 新增会话和消息 cursor API；游标为后端生成的版本化 opaque 字符串，前端只原样回传；
  - 保留旧 page API，cursor 路由不可用时整条列表实例回退 legacy，禁止两种模式混用；
  - 完整校验 Job 参数、状态、来源、页码、相似度和 Token 不变量；
  - 新增会话 PATCH/DELETE，并校验更新 DTO、响应 ID 和删除 204；
  - 取消响应继续执行运行时 DTO 校验和任务 ID 一致性检查。
- `web/src/pages/ConversationsPage.vue`
  - 记录当前 Answer Job；
  - 重新进入会话时先查询活动任务，再读取消息并恢复 queued/processing 轮询；
  - 使用选择代次隔离迟到响应，快速切换会话时不污染当前消息和任务；
  - 发送、再次提交和取消操作冻结会话选择代次与 Job ID，迟到写响应不会污染后来选择的会话；
  - 取消成功后使在途轮询失效，旧 queued/processing 响应不能覆盖 canceled 终态；
  - 当消息读取成功且活动任务端点返回 `404` 时，兼容默认关闭 Answer Jobs 的 M1 页面；
  - `queued` 状态显示取消按钮；
  - 取消成功后刷新会话消息，助手消息保持 `interrupted`；
  - 取消竞争返回 `409` 时重新读取任务状态，不在客户端伪造 `canceled`；
  - `failed/canceled` 状态显示“再次提交此问题”；
  - 再次提交使用新的 `client_message_id`，形成新的用户消息和助手占位；
  - 处理 `answer_job_owner_queue_capacity` 和 `answer_job_queue_capacity`；
  - 使用 `Retry-After` 倒计时，冷却期间禁用提交和再次提交；
  - 保留请求编号，便于日志关联。
  - 会话列表按页加载并按 ID 去重；消息从最新页开始，可继续加载更早历史；
  - 回答参数支持全部/单篇文档、`top_k=1..20` 和 `auto/zh/en`，历史合法值可以完整恢复；
  - retry 使用原 Job 的问题、文档范围、`top_k` 和请求语言，并生成新的 `client_message_id`；
  - succeeded Job 展示来源和 Token；空来源且零 Token 显示证据不足。
  - 支持内联重命名、归档/恢复和删除二次确认；归档后内容只读，历史与结果仍可查看；
  - 成功的迟到管理响应始终按目标 ID 收敛侧栏，但不会抢回已经切换的新会话；
- `web/src/shared/api/capacity-error.ts`
  - 增加两种异步问答队列容量 code 的安全展示文案。

### 明确未实现

- SSE 流式输出；
- 多轮历史上下文拼接；
- 摘要、来源快照和历史消息召回；
- 将已失败任务原地重置为 `queued`；
- 按任意历史 turn 打开对应 Job 结果，以及带文档 revision、内容 hash 或证据正文的独立历史快照；当前 linked Job 虽随会话保留，前端只恢复 latest；
- SSE 流式 Generation 链路验收。

## 3. 验收证据

从 `web` 目录执行：

```powershell
npm.cmd run format:check
npm.cmd run type-check
npm.cmd run lint
npm.cmd run build
npm.cmd run test -- --run
```

结果：

- Prettier 格式检查通过；
- TypeScript 类型检查通过；
- ESLint 通过；
- Vite 生产构建通过；
- 40 个测试文件通过；
- 205 个测试通过。

新增覆盖：

- 会话回答任务取消后刷新为中断助手消息；
- 页面刷新后恢复 queued/processing 任务、轮询和 queued 取消；
- 活动任务 404 兼容和快速切换会话的迟到响应隔离；
- 发送、重试、取消切换会话的迟到响应隔离，以及取消后旧轮询失效；
- 回答启动响应中的会话、消息角色和 Job 关联 ID 一致性校验；
- 会话加载更多、消息最新页/更早页合并、页间重复去重和稳定顺序；
- cursor codec 响应校验、opaque 透传、跨模式回退、迟到响应和大于一页新增消息的缺口桥接；
- latest 404 回退 active、各任务终态恢复和成功结果来源/Token 校验；
- 文档范围、`top_k`、语言提交，及 retry 原参数复用；
- 重命名、归档/恢复、删除确认、活动任务冲突、级联删除和管理迟到响应收敛；
- 问答队列容量错误显示稳定提示、请求编号和 5 秒冷却；
- 冷却期间禁止重复提交，冷却结束后恢复手动提交；
- 失败回答使用新的客户端消息 ID 创建新的会话 turn 再次提交；
- 取消 API 的响应校验和请求路径。

本次状态复核重新执行了 `go test -count=1 ./...`、`go vet ./...` 和完整前端门禁，结果通过。Docker Desktop
Linux Engine 已恢复，隔离数据库中的迁移、会话/Answer Job PostgreSQL 仓储、8 路并发幂等重放和清理/创建
竞态测试均通过。持久开发库已从迁移 28 升级到 37，并通过本地 HTTP 冒烟：3 个会话分页、6 条消息的
`order=desc` 三页窗口、非法 order 400、latest Job 204/200、参数快照、queued 取消、终态幂等重放和最新消息
窗口均符合契约；`math.MaxInt64` 页码对会话和消息列表均稳定返回 400，没有溢出成数据库 500；测试用户及其
级联数据已经清理。

保留策略验证进一步覆盖：standalone succeeded/failed/canceled 按 TTL 分批清理，conversation-linked 三种终态
越过 cutoff 仍保留；同一 user message 永久重放原 Job；删除会话后 linked Job 级联清理。删除指定文档会立即
清理 standalone Job，而 linked Job 保留 result/source/token 和不可变 `requested_document_id`；新 turn 使用已删除
文档重试稳定返回 404，不会扩成全部文档范围。

文档删除隐私门禁还覆盖全库 standalone 结果：终态 sources 引用被删文档时立即清理；处理中的任务在成功收尾前
重新锁定并验证全部来源文档，删除抢先时立即进入 failed，answer/sources/Token 不落库。migration 35 的危险 down
会 fail closed，避免丢失单篇请求快照后把重试扩大到全部文档。

备份恢复 manifest 已演进到 v3，记录迁移版本和 Git 工作树是否 dirty；v1/v2 保持兼容。非空 linked succeeded
fixture 已通过十表行数、关联角色、sources/Token、requested document snapshot 和唯一索引的隔离恢复门禁；另有
standalone 来源 fixture 验证 `DocumentID` 与 `document_id` 两种合法键形态可恢复，非法 ID、缺失文档和跨 Owner
引用均在写出结果文件前被拒绝。

cursor HTTP 冒烟验证了 5 个会话分 3 批无重复、消息 `[5,6] → [3,4] → [1,2]` 稳定历史链、首批后追加
sequence 7 不影响旧 cursor、刷新头批次得到 `[6,7]`，以及跨会话/跨类型 cursor 稳定返回 400。前端在两次
刷新之间新增超过一页时会检测序号缺口，临时采用新头 cursor 向下桥接，再通过 ID 去重接回原历史窗口。

会话管理 HTTP 冒烟进一步验证：标题 trim、孤立 `assistant/generating` 在归档时原子收尾为 `interrupted`、
archived 写入/重试返回 409、queued Job 阻止归档和删除、取消后允许归档、恢复 active 后允许写入，以及删除后
conversation/messages/linked answer_jobs 均为 0。测试用户与数据已经清理。

完整集成回归第一次与 Docker 镜像编译并行运行时，一条隔离 schema 迁移测试超过 15 秒超时；镜像编译完成后
单独重跑整套门禁通过；包含会话管理、归档自愈、级联删除和 cursor 的最新完整报告为
`chatgpt/运行产物/回归/backend-local-integration-20261101T145336Z.json`。该最终回归已覆盖 schema 37、standalone
来源文档删除隐私、历史不安全来源回填、requested document 快照降级并发保护、会话保留、游标和会话管理；远程 AI 和
真实 OSS 专项保持关闭。

后端排队失败补偿使用独立的 5 秒上下文，不继承客户端断开产生的取消信号；补偿仍受超时约束，并保留原请求
上下文中的日志关联值。Handler 和前端都会校验会话、用户消息、助手消息和 Answer Job 的关联 ID 与角色，内部
错配不会作为正常 DTO 返回。

## 4. 数据与安全边界

- 会话消息继续由 PostgreSQL 保存；原始文档和文件存储边界不变；
- 浏览器不提交 `user_id`，所有会话和任务查询继续由 Session → OwnerScope → SQL owner filter 保护；
- 再次提交不复用失败/取消任务的旧客户端消息 ID，避免把历史终态悄悄改写为活动任务；
- 容量冷却期间不自动重放，避免重复提交形成请求洪峰；
- `ANSWER_ENABLED` 和 `ANSWER_JOBS_ENABLED` 的模板默认值均为 `false`，部署环境可以覆盖；HTTP 冒烟只创建并
  取消 queued 任务，没有启动 Answer Worker 或触发真实远程 Generation。验收后 API 容器已恢复
  `ANSWER_JOBS_ENABLED=false`。本机忽略的 `.env` 不作为长期文档事实。

2026-11-04 又使用真实 `qwen3.6-flash` 完成三个 conversation-linked Answer Job：`1439`、`1440`、`1441`
均一次成功，无重试和错误；总耗时分别约 `9.95s`、`7.04s`、`4.33s`，每轮均保存 20 条来源、Token 用量和
completed 助手消息。该证据证明单用户顺序执行的真实主链路，不外推为多用户压力、故障恢复或上下文记忆验收。

## 5. 后续工作

1. 设计并验收 SSE，明确断线、重连和终态恢复；
2. 冻结多轮上下文 Token 预算和随会话生命周期保存的来源快照协议；
3. 增加按历史消息查询 linked Job 结果的入口；
4. 将 M2 读写压力和长期 retained Job 规模纳入后续 k6/容量基线；
5. 将 conversation-linked Job 纳入后续 k6 混合容量与故障恢复门禁。
