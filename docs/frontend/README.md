# 前端正式文档

> 当前状态：F0 工程骨架与 F1 API 基础已完成，F2-A 已完成带登录态的列表、单文件上传和
> 同用户内容去重前端切片；F2-B 已实现详情、解析任务创建与轮询、chunks 分页和删除二次确认，
> 批量导入解析队列也已复用现有单文档接口完成；解析任务现在可按文档恢复最近任务、取消 queued 任务并识别
> canceled/cancelable，上传与解析容量拒绝会保留操作上下文并按 Retry-After 开放手动重试；真实专项联调仍待执行。
> 上传前重复预检正式契约已经接入：浏览器在单路 Web Worker 中分块计算 SHA-256，命中当前用户已有文档时跳过正文上传；
> 网络、超时和 5xx 按契约降级到原上传接口，后端哈希与数据库唯一约束继续承担最终一致性。
> 独立的文档向量化页面已经接入单篇/批量申请、活动任务查询与等待/排队取消；向量模块使用自己的领域模型、API、
> composable 和 UI，不侵入文档库页面。页面初始化会按每批最多 100 个文档调用 `POST /embedding-jobs/latest` 发现最近任务，
> 再只对 waiting_document/queued/processing 任务继续轮询；刷新和换设备后的历史任务发现已完成，但最近 succeeded 尚不能
> 证明它匹配当前 document revision。
> F2-C 第一阶段“单篇 / 全部文档”共享检索范围选择器已经接入，F3 关键词检索不再要求手填文档 ID，
> 并已完成真实后端结果联调、零命中辨识、文本块关键词安全高亮，以及 2～8 个关键词的 all/any + within=chunk 检索；
> F4 语义检索基础切片已经在同一检索页以显式模式接入，复用全部/单篇范围，支持 top_k、来源页码、相似度、409 向量化引导、
> 容量冷却和安全错误状态；它使用独立 DTO、API、composable 与结果组件，不会随输入、页面挂载或模式切换自动调用远程模型。
> 语义结果支持当前标签页刷新恢复，缓存按公开用户 ID 与文档范围隔离并做运行时校验；正文只标记实际存在的较长文字片段，
> 不用碎片化双字铺满结果，也不伪造纯语义对应区间。
> 结果保留默认关闭，用户开启时页面明确说明包含文本片段；缓存最长 30 分钟，并在关闭选项、退出、401 或密码重置时清除。
> 当前 `SEMANTIC_SEARCH_ENABLED=false`，真实远程 Embedding 纵向联调等待用户显式启用。
> F4 带来源问答基础页已经按正式 `/answers` 契约接入全部/单篇范围、语言、top_k、来源、Token、重试和安全错误状态。
> 当前 `ANSWER_ENABLED=false`，真实 Generation 与费用链路等待用户显式启用。
> F100-1 第一批容量交互已经接入：向量化页面识别 owner/global 容量拒绝和批量逐项暂缓，问答页面识别 Answer/在线
> Embedding 容量拒绝；两侧均按 `Retry-After` 使用单一可取消倒计时，等待期间禁止重复提交，到期后仅开放手动重试。
> P6 前端第一批认证基础、公共页面和受保护应用外壳已经实现，并通过真实 Go 后端、PostgreSQL、Mailpit、
> Vite `/api` 代理和浏览器表单纵向联调。
> Vue 3 + TypeScript + Vite、
> Vue Router、Pinia、Axios、Vitest、Vue Test Utils、ESLint 和 Prettier 已安装并接入；
> P6 后端 B1～B7、忘记/重置密码和全链路个人数据隔离均已完成并通过发布验收；前端已接入
> Auth DTO/API、三态 Auth Store、`/users/me` 启动恢复、登录/注册/退出、忘记/重置密码和受保护路由。
> F2 上传 API 会把 `201 + duplicate:false` 表现为新建成功，把 `200 + duplicate:true` 表现为
> “该内容已存在”并刷新已有列表，不按文件名自行判重。性能工程继续保持“前端有界上传与轮询、后端决定任务并发”的边界；
> 下一步补齐容量真实联调、固定样本 A/B、大文件、F2-B 解析恢复/取消专项和双用户产品隔离验收；
> Element Plus 仍按需后置，不作为基础架构依赖。
> 成员管理与租户切换仍留到 P7；前端不读取 HttpOnly Cookie，也不向业务接口提交 `user_id`。

前端开发必须先阅读：

1. [HTTP API 总览](../shared/api/http-api-overview.md)；
2. [前端应用架构](architecture/frontend-application-architecture.md)；
3. [前端分层与阶段路线](architecture/frontend-roadmap.md)；
4. [P6 个人用户域与私有数据闭环](../shared/architecture/personal-user-domain.md)；
5. [工程化、个人用户与团队演进路线](../shared/architecture/product-evolution-roadmap.md)；
6. [高性能与高并发工程目标](../shared/architecture/performance-engineering-goal.md)；
7. [P6 个人用户认证前端交接](architecture/p6-authentication-handoff.md)；
8. [F2 文档上传去重联调交接](architecture/f2-document-upload-integration-handoff.md)；
9. [F2-B 文档操作闭环](architecture/f2b-document-lifecycle.md)；
10. [F2 批量导入与解析队列](architecture/f2-batch-import-queue.md)；
11. [F2 上传前重复文件预检](architecture/f2-upload-preflight-evaluation.md)；
12. [F2-C 单篇 / 全部文档检索范围选择器](architecture/f2c-document-scope-picker.md)；
13. [F3 关键词检索结果高亮](architecture/f3-keyword-result-highlighting.md)；
14. [F3 多关键词与位置范围检索设计](architecture/f3-multi-keyword-search-design.md)；
15. [F3 检索对 Chunk 质量的依赖](architecture/f3-chunk-quality-dependency.md)；
16. [F3 文档解析器替换与并行评估](architecture/f3-document-parser-package-evaluation.md)；
17. [用户可选向量化与文档编辑前端交接](architecture/document-vectorization-editing-handoff.md)（基础向量化页面已实现）；
18. [F4 语义检索基础页面](architecture/f4-semantic-search-handoff.md)；
19. [F4 带来源问答基础页面](architecture/f4-grounded-answer-handoff.md)；
20. [100 人在线并发前端交接](architecture/100-user-concurrency-handoff.md)；
21. Git 忽略目录中的 `chatgpt/前端/README.md` 和当前进度记录。

## 安全与问题追溯

- [SECURITY-F4-001：检索结果缓存缺少用户隔离](incidents/SECURITY-F4-001-search-result-cache-isolation.md)：记录不安全本地缓存的
  时间线、影响边界、根因、永久修复、验证证据和以后新增浏览器存储时的强制检查清单。

## 文档边界

- 本目录只记录已经确认、需要长期可信的前端架构与开发路线；
- 实际安装版本以 `web/package.json` 和锁文件为准；
- 对话过程、每日进度、学习复盘和问题调查保存在 Git 忽略的 `chatgpt/前端/`；
- 请求字段、响应 DTO、HTTP 状态和错误语义统一以 `docs/shared/api/` 为权威入口；
- 选型不等于已接入，文档必须明确区分“已确认”“已安装”“已验证”。
