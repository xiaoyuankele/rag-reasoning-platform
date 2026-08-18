# RAG Reasoning Platform Web

本目录是轻量文档知识中台的前端工程。F0 工程骨架与 F1 API 基础已经完成，
`/search` 路由已开放基础关键词检索；P6 Auth DTO/API、三态 Auth Store、登录、注册、退出、忘记/重置密码、
公共认证布局和受保护工作台外壳已经实现，并通过真实 Go 后端、PostgreSQL、Mailpit、Vite `/api` 代理和
浏览器表单纵向联调；文档管理、语义检索和问答继续按后续阶段推进。

已安装并验证的基础技术：

- Vue 3；
- TypeScript；
- Vite；
- Vue Router；
- Pinia；
- Axios；
- Vitest + Vue Test Utils；
- ESLint + Prettier。

Element Plus 仍是后续业务阶段的按需选项，当前没有安装。Pinia 只保存跨页面认证状态；
健康检查、表单输入和单次搜索的局部状态不进入全局 Store。

## 本地命令

```powershell
cd web
npm.cmd install
npm.cmd run dev
npm.cmd run type-check
npm.cmd run lint
npm.cmd run test
npm.cmd run format:check
npm.cmd run build
```

开发服务器中的 `/api/*` 会代理到默认的 `http://localhost:8080/*`。复制 `.env.example`
为 `.env.local` 后可以调整浏览器 API 基础路径或本地后端代理地址。

## 目录入口

```text
src/
├─ app/       # 应用外壳、插件入口与路由
├─ pages/     # 路由页面
├─ features/  # 按用户能力组织的流程与界面状态
├─ entities/  # 前端领域展示模型
└─ shared/    # API、通用 UI、样式和工具
```

当前 PowerShell 会优先解析被执行策略阻止的 `npm.ps1`，因此本机使用 `npm.cmd`。

## 文档入口

- [前端正式文档](../docs/frontend/README.md)
- [前端应用架构](../docs/frontend/architecture/frontend-application-architecture.md)
- [前端阶段路线](../docs/frontend/architecture/frontend-roadmap.md)
- [HTTP API 总览](../docs/shared/api/http-api-overview.md)
- [P6 个人用户域与私有数据闭环](../docs/shared/architecture/personal-user-domain.md)
- [工程化、个人用户与团队演进路线](../docs/shared/architecture/product-evolution-roadmap.md)

Git 忽略目录中的 `chatgpt/前端/` 保存本地进度、复盘与问题调查。
