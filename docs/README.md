# 项目正式文档索引

`docs/` 保存需要随 Git 版本管理的项目事实、架构决策、接口契约、评估方法和开发规范。
对话过程、个人学习复盘、临时日志和本机测试产物不放在这里，而放在 Git 忽略的
`chatgpt/` 中。

## 目录边界

```text
docs/
├── shared/       # 前端、后端共同遵守的正式契约、系统边界与产品演进路线
├── backend/      # Go、Python、数据库、AI 链路的正式文档
└── frontend/     # 前端架构、路线图、技术决策和验收标准
```

## 阅读入口

- 前后端共同接口：[HTTP API 总览](shared/api/http-api-overview.md)
- 个人版、前端与多用户演进：[产品演进路线](shared/architecture/product-evolution-roadmap.md)
- 后端与 AI 架构：[后端正式文档](backend/README.md)
- 前端开发准备：[前端正式文档](frontend/README.md)

## 维护规则

1. 已实现能力必须与代码和测试一致，计划能力必须明确标注“计划中”。
2. 前后端都会受影响的 HTTP 字段、状态码或错误契约，先更新 `shared/`。
3. 只影响一侧的内部设计分别放入 `backend/` 或 `frontend/`。
4. `docs/` 是长期事实；每天做了什么、学到了什么不写入这里。
5. 如果后续引入 OpenAPI，OpenAPI 文件作为机器可读契约，本目录保留面向人的说明与决策。
