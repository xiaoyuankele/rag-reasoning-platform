# RAG Reasoning Platform Web

本目录是轻量文档知识中台的前端工程。F0 工程骨架与 F1 API 基础已经完成，
`/search` 路由已开放基础关键词检索；文档管理、语义检索和问答仍按后续阶段推进。

已安装并验证的基础技术：

- Vue 3；
- TypeScript；
- Vite；
- Vue Router；
- Pinia；
- Axios；
- Vitest + Vue Test Utils；
- ESLint + Prettier。

Element Plus 仍是后续业务阶段的按需选项，当前没有安装。Pinia 已注册到应用入口，
但遵循最小共享原则，健康检查和单次搜索的局部状态不进入全局 Store。

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
- [个人版到多用户产品演进路线](../docs/shared/architecture/product-evolution-roadmap.md)

Git 忽略目录中的 `chatgpt/前端/` 保存本地进度、复盘与问题调查。
