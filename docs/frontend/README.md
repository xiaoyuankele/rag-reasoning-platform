# 前端正式文档

> 当前状态：F0 工程骨架与 F1 API 基础已完成，F3 关键词检索最小可用切片已经开放并通过真实联调；
> P6 前端第一批认证基础、公共页面和受保护应用外壳已经实现，并通过真实 Go 后端、PostgreSQL、Mailpit、
> Vite `/api` 代理和浏览器表单纵向联调。
> Vue 3 + TypeScript + Vite、
> Vue Router、Pinia、Axios、Vitest、Vue Test Utils、ESLint 和 Prettier 已安装并接入；
> P6 后端 B1～B7、忘记/重置密码和全链路个人数据隔离均已完成并通过发布验收；前端已接入
> Auth DTO/API、三态 Auth Store、`/users/me` 启动恢复、登录/注册/退出、忘记/重置密码和受保护路由。
> 下一步补齐带登录态的 F2 文档管理，
> 并用文档选择器替代检索页中的临时文档 ID 输入；
> Element Plus 仍按需后置，不作为基础架构依赖。
> 成员管理与租户切换仍留到 P7；前端不读取 HttpOnly Cookie，也不向业务接口提交 `user_id`。

前端开发必须先阅读：

1. [HTTP API 总览](../shared/api/http-api-overview.md)；
2. [前端应用架构](architecture/frontend-application-architecture.md)；
3. [前端分层与阶段路线](architecture/frontend-roadmap.md)；
4. [P6 个人用户域与私有数据闭环](../shared/architecture/personal-user-domain.md)；
5. [工程化、个人用户与团队演进路线](../shared/architecture/product-evolution-roadmap.md)；
6. [P6 个人用户认证前端交接](architecture/p6-authentication-handoff.md)；
7. Git 忽略目录中的 `chatgpt/前端/README.md` 和当前进度记录。

## 文档边界

- 本目录只记录已经确认、需要长期可信的前端架构与开发路线；
- 实际安装版本以 `web/package.json` 和锁文件为准；
- 对话过程、每日进度、学习复盘和问题调查保存在 Git 忽略的 `chatgpt/前端/`；
- 请求字段、响应 DTO、HTTP 状态和错误语义统一以 `docs/shared/api/` 为权威入口；
- 选型不等于已接入，文档必须明确区分“已确认”“已安装”“已验证”。
