# 前端正式文档

> 当前状态：F0 工程骨架与 F1 API 基础已完成，F2-A 已完成带登录态的列表、单文件上传和
> 同用户内容去重前端切片；F2-B 已实现详情、解析任务创建与轮询、chunks 分页和删除二次确认，
> 批量导入解析队列也已复用现有单文档接口完成；自动化门禁已通过，真实上传、解析和删除已由用户验证正常，
> 真实多文件批次、chunks 分页和刷新恢复仍待专项验收。
> F2-C 第一阶段“单篇 / 全部文档”共享检索范围选择器已经接入，F3 关键词检索不再要求手填文档 ID，
> 并已完成真实后端结果联调、零命中辨识和文本块关键词安全高亮；
> P6 前端第一批认证基础、公共页面和受保护应用外壳已经实现，并通过真实 Go 后端、PostgreSQL、Mailpit、
> Vite `/api` 代理和浏览器表单纵向联调。
> Vue 3 + TypeScript + Vite、
> Vue Router、Pinia、Axios、Vitest、Vue Test Utils、ESLint 和 Prettier 已安装并接入；
> P6 后端 B1～B7、忘记/重置密码和全链路个人数据隔离均已完成并通过发布验收；前端已接入
> Auth DTO/API、三态 Auth Store、`/users/me` 启动恢复、登录/注册/退出、忘记/重置密码和受保护路由。
> F2 上传 API 会把 `201 + duplicate:false` 表现为新建成功，把 `200 + duplicate:true` 表现为
> “该内容已存在”并刷新已有列表，不按文件名自行判重。下一步使用真实 ready 文档验收单篇/全部检索范围，
> 补齐 F2-B 专项场景，并完成双用户产品隔离验收；
> Element Plus 仍按需后置，不作为基础架构依赖。
> 成员管理与租户切换仍留到 P7；前端不读取 HttpOnly Cookie，也不向业务接口提交 `user_id`。

前端开发必须先阅读：

1. [HTTP API 总览](../shared/api/http-api-overview.md)；
2. [前端应用架构](architecture/frontend-application-architecture.md)；
3. [前端分层与阶段路线](architecture/frontend-roadmap.md)；
4. [P6 个人用户域与私有数据闭环](../shared/architecture/personal-user-domain.md)；
5. [工程化、个人用户与团队演进路线](../shared/architecture/product-evolution-roadmap.md)；
6. [P6 个人用户认证前端交接](architecture/p6-authentication-handoff.md)；
7. [F2 文档上传去重联调交接](architecture/f2-document-upload-integration-handoff.md)；
8. [F2-B 文档操作闭环](architecture/f2b-document-lifecycle.md)；
9. [F2 批量导入与解析队列](architecture/f2-batch-import-queue.md)；
10. [F2-C 单篇 / 全部文档检索范围选择器](architecture/f2c-document-scope-picker.md)；
11. [F3 关键词检索结果高亮](architecture/f3-keyword-result-highlighting.md)；
12. [F3 多关键词与位置范围检索设计](architecture/f3-multi-keyword-search-design.md)；
13. [F3 检索对 Chunk 质量的依赖](architecture/f3-chunk-quality-dependency.md)；
14. [F3 文档解析器替换与并行评估](architecture/f3-document-parser-package-evaluation.md)；
15. [用户可选向量化与文档编辑前端交接](architecture/document-vectorization-editing-handoff.md)；
16. Git 忽略目录中的 `chatgpt/前端/README.md` 和当前进度记录。

## 文档边界

- 本目录只记录已经确认、需要长期可信的前端架构与开发路线；
- 实际安装版本以 `web/package.json` 和锁文件为准；
- 对话过程、每日进度、学习复盘和问题调查保存在 Git 忽略的 `chatgpt/前端/`；
- 请求字段、响应 DTO、HTTP 状态和错误语义统一以 `docs/shared/api/` 为权威入口；
- 选型不等于已接入，文档必须明确区分“已确认”“已安装”“已验证”。
