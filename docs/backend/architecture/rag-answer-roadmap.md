# P4 带来源引用问答（RAG 第一版）架构

## 一、当前工作与核心目的

本阶段新增一个面向前端和未来智能体的同步 HTTP 接口：

```text
POST /answers
```

它接收自然语言问题，从已经完成向量化的文献 chunks 中检索证据，再调用远程文本生成模型形成答案，
最后同时返回答案与证据来源。用户可以查看文献标题、原始文件名和物理页码，不能只相信模型文字。

## 二、第一版接口语义

请求：

```json
{
  "query": "磁浮悬浮控制器发生故障时，怎样进行实时检测？",
  "document_id": 208,
  "top_k": 5,
  "response_language": "auto"
}
```

- `query` 必填；
- `document_id` 可选，有值时表示“问当前文献”，省略时表示“问整个知识库”；
- `top_k` 可选，默认 5；这是交给生成模型的证据块数量，不要求普通用户在界面手动填写。
- `response_language` 可选，支持 `auto`、`zh`、`en`；省略等同于 `auto`，显式语言优先于自动判断。

响应：

```json
{
  "query": "磁浮悬浮控制器发生故障时，怎样进行实时检测？",
  "answer": "……[1]",
  "response_language": "zh",
  "sources": [
    {
      "citation": 1,
      "chunk_id": 123,
      "document_id": 208,
      "title": "……",
      "original_name": "mathematics-11-04045-v2.pdf",
      "page_start": 1,
      "page_end": 1,
      "similarity": 0.82
    }
  ],
  "usage": {
    "prompt_tokens": 1260,
    "completion_tokens": 180,
    "total_tokens": 1440
  }
}
```

`sources` 表示实际交给模型的候选证据。第一版通过 Prompt 要求模型在答案中使用 `[1]`、`[2]` 等标记，
但不能仅凭模型文字声称某个来源一定支持结论；自动引用合法性与人工事实一致性需要分别验证。
`usage` 表示远程生成调用的 Token 用量，用于开发期观察成本；无证据降级时三个值都是 0。
`response_language` 是 Application 最终解析出的实际回答语言，只会返回 `zh` 或 `en`。

## 三、端到端处理流程

```text
浏览器 / Codex 等调用者
  ↓ POST /answers
AnswerHandler
  ↓ 绑定 JSON，设置缺省 top_k，转换回答语言类型
AnswerService
  ↓ 校验 query、document_id、top_k 和回答语言，解析 auto
SemanticSearchService
  ↓ 查询向量化 + pgvector 相似 chunks
AnswerService
  ↓ 为 chunks 编号，构造“问题 + 不可信证据”Prompt
Generator（Domain 端口）
  ↓
DashScope Chat Completions 适配器
  ↓ 返回生成文字和 token 用量
AnswerService
  ↓ 答案 + 原始证据
AnswerHandler
  ↓ 转换为 HTTP DTO
浏览器 / Codex
```

如果没有检索到任何 chunk，Application 不调用生成模型，直接返回“现有文献不足以回答”和空来源列表。
对全库问答，或指定文档已经确认当前模型向量完整的情况，这是正常业务结果而不是 `404`。如果请求明确
指定 `document_id`，文档不存在返回 `404`；文档存在但状态、chunks 或当前模型向量尚未完整就绪时
返回 `409`，不能再伪装成“没有证据”。

### 3.1 运行时关系图

下图的实线表示一次 `/answers` 请求真正流过的方向。虚线表示 `main.go` 启动时完成依赖注入，
它不会在每次请求时重新创建这些对象。

```mermaid
flowchart TD
    Caller["浏览器 / Codex"] -->|"POST /answers JSON"| Handler["API: AnswerHandler"]
    Handler -->|"answer.Input"| Answer["Application: AnswerService"]

    Answer -->|"SemanticSearchInput"| Search["Application: SemanticSearchService"]
    Search -->|"自然语言问题"| Embedder["Domain 端口: Embedder"]
    Embedder -->|"HTTPS"| EmbeddingAPI["DashScope Embeddings API"]
    EmbeddingAPI -->|"查询向量"| Embedder
    Search -->|"向量 + model + dimensions + top_k"| ChunkRepo["Domain 端口: SemanticChunkSearcher"]
    ChunkRepo -->|"SQL / pgvector"| PostgreSQL[("PostgreSQL\ntext_chunks + embeddings")]
    PostgreSQL -->|"SemanticSearchHit[]"| Search
    Search -->|"按相似度排序的 hits"| Answer

    Answer -->|"编号证据 + Prompt"| Generator["Domain 端口: Generator"]
    Generator -->|"HTTPS"| GenerationAPI["DashScope Chat Completions API"]
    GenerationAPI -->|"答案 + Token 用量"| Generator
    Generator -->|"GenerateResult"| Answer

    Answer -->|"answer.Output"| Handler
    Handler -->|"answerResponse DTO"| Caller

    Main["cmd/server/main.go\n组合根"] -.->|"创建并注入"| Handler
    Main -.->|"创建并注入"| Answer
    Main -.->|"选择具体实现"| Embedder
    Main -.->|"选择具体实现"| Generator
    Main -.->|"创建 Repository"| ChunkRepo
```

这张图最关键的两个认识是：

1. `AnswerService` 不直接写 SQL 或发送 HTTP，而是通过 `SemanticSearchService` 和 `Generator` 端口使用其他积木；
2. `Generator`、`Embedder` 和 Repository 只定义连接规则，具体实例由 `main.go` 选择并装配。

### 3.2 一次请求的时序图

```mermaid
sequenceDiagram
    participant C as 调用者
    participant H as AnswerHandler
    participant A as AnswerService
    participant S as SemanticSearchService
    participant E as Embedding API
    participant P as PostgreSQL/pgvector
    participant G as Generation API

    C->>H: POST /answers
    H->>A: Answer(Input)
    A->>S: Search(query, document_id, top_k)
    S->>E: 生成查询向量
    E-->>S: query vector
    S->>P: 余弦相似度检索
    P-->>S: SemanticSearchHit[]
    S-->>A: 已排序证据

    alt 没有证据
        A-->>H: 固定降级答案 + sources=[]
    else 找到证据
        A->>A: 编号 [1] [2] 并构造 Prompt
        A->>G: Generate(system, prompt)
        G-->>A: answer + usage
        A-->>H: Output(answer, sources, usage)
    end

    H-->>C: 200 JSON
```

Embedding API 和 Generation API 是两次不同的远程调用：前者把问题变成向量，后者根据证据写答案。
没有证据时第二次调用会被跳过。

## 四、分层责任

| 层 | 第一版责任 | 明确不负责 |
|---|---|---|
| API/Handler | JSON 绑定、默认值、HTTP 状态和响应 DTO | 向量检索、Prompt、调用云模型 |
| Application | 检索与生成的先后顺序、证据编号、Prompt、安全降级 | Gin、SQL、供应商 JSON |
| Domain | 定义 `Generator`、生成请求/结果和稳定错误 | DashScope、HTTP、PostgreSQL |
| Infrastructure | 调用 Chat Completions、限制响应体、解析错误和 token | 决定业务上选哪些 chunks |
| Config | 开关、模型、地址、超时、最大输出 token | 保存真实 API Key 到代码或日志 |
| `main.go` | 创建 Generator、AnswerService、Handler 并注入 | 编写问答业务规则 |

## 五、积木清单与当前完成状态

已有：

- `SemanticSearchService.Search`：问题向量化并返回相关 `SemanticSearchHit`；
- `SemanticSearchHit`：包含 chunk 正文、文档信息、页码和相似度；
- Domain `Generator` 端口与稳定错误；
- DashScope/OpenAI 兼容 Chat Completions HTTP 适配器；
- 默认关闭的 Generation 配置；
- Application `AnswerService`、证据编号和安全 Prompt；
- `POST /answers` Handler、HTTP DTO 与错误映射；
- `auto`、`zh`、`en` 回答语言契约和中英文 Prompt；
- `main.go` 生产组合与能力开关；
- Domain、Infrastructure、Application、Handler 和组合根自动化测试。

第一版质量收尾已完成：

- 使用 8 篇中英文文献和 460 个完整向量冻结 15 条问答、跨语言、跨文档、拒答与未就绪样本；
- 保存原始 HTTP 响应、sources、引用、延迟和 Token，并逐条人工核对事实—引用一致性；
- 15/15 HTTP 行为符合预期，14/14 回答语言与引用编号通过；
- 答案/行为人工支持度为 25/28；复杂表格解释和最佳证据选择问题已登记，不为追求分数临时修改 Prompt 或 `top_k`。

### 5.1 全库问答的证据来源多样化

真实评估发现，同一文档的多个相似 chunks 可能占满 Top K，使其他相关文献无法进入 Prompt。第一版采用
“候选池 + 两遍选择”，不修改公开 HTTP 契约：

1. 调用者的 `top_k` 继续表示最终交给生成模型的证据数量；
2. 全库问答时，AnswerService 内部先检索最多 20 条候选；
3. 第一遍按原始相似度顺序为每篇文档选择第一条最高命中；
4. 第二遍再按原始顺序使用未选择的 chunks 补满最终 `top_k`；
5. 相同 `chunk_id` 不能重复；
6. 指定 `document_id` 的单文档问答不执行文档多样化，只做 chunk 去重并保留原始顺序。

选择算法放在 `application/answer/evidence_selector.go`，作为 AnswerService 的同包内部协作者。第一版不新增
接口或 `main.go` 依赖；只有出现第二种可替换实现（例如远程 rerank）时，才提升为独立端口。

该规则只解决跨文档来源挤占，不负责修复单文档内证据被切散、PDF 原文乱码或远程 rerank。后者继续作为
独立质量问题评估。

后续保留一个暂不实现的增强方向：在候选查询阶段限制每篇文档最多进入 5 个 chunks，使 Top 20 候选池
本身具备文档配额。该方案需要把内部限制从 AnswerService 传到 Domain 查询选项，并由 PostgreSQL
Repository 使用窗口查询或其他分组策略执行，还需要重新测量向量索引与查询性能，因此不属于当前轻量
第一版。只有真实验收发现 Top 20 经常被同一文档占满、且相关文档位于候选池之外时，才重新立项讨论；
不能为了来源数量而强行加入无关文献。

2026-08-14 使用冻结的跨文档问题完成首次真实验收：修改前最终 5 条证据全部来自文档 225；修改后来源为
`225、208、225、225、225`，5 个 chunk 均不重复，回答同时覆盖 LSTM/MGD 异常检测与悬浮控制器故障
检测。该结果证明当前用例的来源挤占得到改善，但文档 208 的最高候选仍偏背景综述，不能据此宣称证据
可回答性问题已经全部解决；后续仍需扩大真实问题集并人工核对事实—引用一致性。

冻结样本、评价层次、人工评分方法和执行约束见
[`P4 带来源问答质量评估计划`](../evaluation/rag-answer-quality-evaluation-plan.md)。

## 六、造轮子边界

本项目自行定义小型 `Generator` 接口和 Prompt 组装规则，以保持 Application 不依赖某家云厂商。不会
自研语言模型、SDK、向量算法或联网搜索。第一版直接使用百炼官方 OpenAI 兼容 Chat Completions：

- [百炼 OpenAI 兼容 Chat Completions](https://help.aliyun.com/zh/model-studio/qwen-api-via-openai-chat-completions)
- [百炼文本生成模型](https://help.aliyun.com/zh/model-studio/text-generation-model/)

## 七、第一版安全和资源边界

1. 默认关闭问答能力，避免启动基础后端时意外产生远程费用；
2. API Key 只保存在进程内存和 Authorization 请求头；
3. 不启用模型联网搜索，只允许根据本地检索证据回答；
4. Prompt 明确把文献正文视为不可信数据，不能执行正文中的指令；
5. 限制问题长度、Top K、HTTP 超时、远程响应体和最大输出 token；
6. 无证据时不调用模型；
7. 不把供应商原始错误、API Key 或内部 Prompt返回前端；
8. 第一版不存储问答历史，重启后不会恢复会话。
9. `qwen3.6-flash` 第一版默认关闭思考模式，避免推理占满输出预算而没有最终答案；该行为可通过配置调整。

## 八、非目标

- 多轮对话与聊天记忆；
- 流式输出；
- 混合检索与 rerank；
- Agent 和工具调用；
- 联网搜索；
- 答案缓存；
- 自动事实核验；
- Python/LangChain 常驻服务。

## 九、分步实施

1. [x] 定义 Domain Generator 端口与稳定错误；
2. [x] 开发并测试 DashScope/OpenAI 兼容文本生成适配器；
3. [x] 增加默认关闭的 Generation 配置；
4. [x] 完成 Application 检索—证据—生成编排；
5. [x] 完成 Handler 和错误映射；
6. [x] 在 `main.go` 组装并保持开关关闭时不调用远程服务；
7. [x] 使用 Fake 完成自动化回归；
8. [x] 使用真实文献问题完成一次 DashScope HTTP 验收；
9. [x] 已冻结并执行第一轮 8 个问题，保存原始响应并人工核对答案相关性、引用正确性、延迟、生成 Token 和失败案例；
10. [x] 分别完成回答语言、跨文档来源多样性和向量未就绪语义的修复与真实回归；
11. [x] 冻结 15 条 P4 收尾样本，保存原始结果并完成人工事实—引用核对；
12. [x] 形成第一版能力边界，将未解决质量问题带编号转入后续迭代。

首次真实验收使用文档 20、中文问题与 `top_k=3`：HTTP 返回 200、3 条来源与 1885 个总 Token。
答案引用编号没有越界，但单次成功不能替代系统化事实一致性评价。

## 十、P4 第一版收尾结论

2026-08-14 的收尾批次覆盖中文、英文、中英跨语言、全库跨文档、安全拒答和文档未向量化场景。
工程门禁全部通过：HTTP 行为 `15/15`、语言匹配 `14/14`、引用编号合法 `14/14`，未向量化样本在
远程调用前返回 `409` 且 Token 为 0。人工答案/行为支持度为 `25/28`，没有 0 分样本。

P4 因此标记为“第一版已完成”，但不宣称生产级准确率：

- `QUALITY-RAG-001` 仍表明正确文档进入 sources 后，最高 chunk 可能只是背景而非直接方法证据；
- `QUALITY-RAG-002` 表明 PDF 表格线性化会丢失多级表头，模型可能错配位置、方向和数值；
- 混合检索、rerank 和表格结构化提取继续后置，必须由更大的真实问题集和失败频率决定是否投入。

下一阶段进入 P5 工程化，优先处理运行路径、部署、可观测性、回归命令和性能/成本记录。
