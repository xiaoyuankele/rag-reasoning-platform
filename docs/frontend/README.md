# 前端正式文档

> 当前状态：F0 工程代码与自动化验收已完成，真实后端联调待验收。Vue 3 + TypeScript + Vite、
> Vue Router、Pinia、Axios、Vitest、Vue Test Utils、ESLint 和 Prettier 已安装并接入；
> Element Plus 仍按需后置，不作为基础架构依赖。
> 当前前端面向单个本地用户和一个隐含个人工作区，不包含登录、成员管理或租户切换。

前端开发必须先阅读：

1. [HTTP API 总览](../shared/api/http-api-overview.md)；
2. [前端应用架构](architecture/frontend-application-architecture.md)；
3. [前端分层与阶段路线](architecture/frontend-roadmap.md)；
4. [个人版到多用户产品演进路线](../shared/architecture/product-evolution-roadmap.md)；
5. Git 忽略目录中的 `chatgpt/前端/README.md` 和当前进度记录。

## 文档边界

- 本目录只记录已经确认、需要长期可信的前端架构与开发路线；
- 实际安装版本以 `web/package.json` 和锁文件为准；
- 对话过程、每日进度、学习复盘和问题调查保存在 Git 忽略的 `chatgpt/前端/`；
- 请求字段、响应 DTO、HTTP 状态和错误语义统一以 `docs/shared/api/` 为权威入口；
- 选型不等于已接入，文档必须明确区分“已确认”“已安装”“已验证”。
