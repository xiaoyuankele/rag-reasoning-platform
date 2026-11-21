# F4 带来源问答前端交接

> 状态：2026-08-21 基础页面已实现。前端已经按 `POST /answers` 正式契约接入单次问答、全部/单篇范围、回答语言、
> 证据数量、引用来源、Token 用量和安全错误状态。当前 Docker 后端 `ANSWER_ENABLED=false`，已验证功能关闭提示；
> 真实生成成功链路必须在用户明确启用远程能力后验收。

## 1. 当前范围

- [x] 复用共享“全部 / 单篇”文档范围，不在问答模块复制文档列表状态；
- [x] 显式输入问题后才调用远程模型，页面挂载和切换范围不自动请求；
- [x] 支持 `auto/zh/en` 回答语言和 3/5/8/10 条最大证据数；
- [x] 展示回答、`[n]` 引用、文档、页码、chunk、相似度和 Token 用量；
- [x] 区分加载、成功、无证据降级、向量未就绪、功能关闭、超时、上游异常和普通失败；
- [x] 新问题取消旧请求，失败可以重试；
- [ ] 多轮对话、问答历史、流式输出、来源正文展开和任意多篇文档范围。

## 2. 模块与数据流

```text
AnswerPage
  ├─ DocumentScopePicker：读取当前用户 ready 文档，维护全部/单篇范围
  └─ GroundedAnswerPanel：问题、语言、top_k、费用提示和结果展示
       → useGroundedAnswer：单次请求状态、取消、重试和安全错误映射
       → answer-api：POST /answers、unknown DTO 运行时校验
       → Go Answer Service：语义证据检索 → 有证据才调用 Generation
```

`entities/answer` 只保存前端回答、来源和用量模型；HTTP 的 snake_case 只存在于 `features/answer/api`。页面只组合范围与问答，
不直接操作 Axios，也不把结果放入 Pinia。

## 3. 正式请求与响应边界

请求：

```json
{
  "query": "磁悬浮车辆与轨道梁耦合振动的主要影响因素是什么？",
  "document_id": 20,
  "top_k": 5,
  "response_language": "auto"
}
```

`document_id` 在全部范围下省略。问题前端限制 1～1000 个字符，`top_k` 使用后端允许的 1～20 范围内预设值。问答请求单独
使用 70 秒客户端超时，以覆盖后端默认 60 秒 Generation 超时，不改变其他 API 的 10 秒基础超时。

响应必须运行时校验：问题和回答非空；语言为 `auto/zh/en`；来源引用从 1 连续编号；文档、chunk、页码和相似度合法；
`total_tokens = prompt_tokens + completion_tokens`。无证据时 `sources=[]`、Token 全 0，页面显示后端降级答案，不伪造来源。

## 4. 费用与安全

- 页面打开、文档选项加载、范围切换都不会调用 `/answers`；
- 用户点击“生成带来源回答”前持续显示远程调用和额度提示；
- 前端不发送 `user_id`，OwnerScope 和来源隔离由后端 Session、Application 与 Repository 保证；
- `409 document embeddings are not ready` 引导到向量化页面，不退回无来源自由回答；
- 后端未注册 `/answers` 时显示 `ANSWER_ENABLED` 未启用，而不是误写成所选文档不存在；
- 502/503、超时和请求编号使用安全文案，不展示上游密钥或内部异常。

## 5. 验收状态

- API、composable、组件与页面集成自动化已覆盖成功、无证据、409、功能关闭、重试和范围切换；
- 全量 30 个测试文件、111 个测试，以及类型、Lint、格式和生产构建通过；
- 真实登录态页面读取 94 篇 ready 文档，挂载和范围读取未调用模型；
- `ANSWER_ENABLED=false` 下显式提交返回功能关闭提示和请求编号，没有远程生成调用；
- 仍需用户明确启用 `ANSWER_ENABLED=true` 后验证真实答案、引用、Token、超时与模型费用。
