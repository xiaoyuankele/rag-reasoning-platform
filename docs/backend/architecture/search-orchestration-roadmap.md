# 多能力检索编排路线图

> 状态：已记录，尚未实现。当前 `GET /search`、`POST /semantic-search` 和 `POST /answers`
> 继续作为稳定、可独立验收的基础能力，不在第一版中堆叠全部专业检索规则。

## 一、为什么需要独立编排层

真实学术问题可能同时包含语言、文献范围、方法解释、公式、概念、元数据和跨文献比较等要求。单个
关键词或语义检索接口无法可靠覆盖所有专业任务；如果不断在 Handler 或现有 Service 中增加条件分支，
最终会把输入判断、能力选择、检索实现和结果融合混在一起。

未来采用“原子检索能力 + Application 编排器”的结构：

```text
前端 / Codex
  ↓ 统一检索请求
Handler
  ↓ 只做协议绑定与 HTTP 映射
SearchOrchestrator（Application）
  ├─ QueryPlanner：形成受控执行计划
  ├─ KeywordSearch：精确术语与字面搜索
  ├─ SemanticSearch：自然语言语义搜索
  ├─ MetadataSearch：标题、作者、年份、DOI 等
  ├─ ConceptSearch：概念、别名和中英文映射（候选）
  └─ 专业 Search：公式、方法、对比等后续细分能力
  ↓ 统一 SearchResult / Evidence
Handler
  ↓
前端 / Codex
```

## 二、这里不是 Gin Middleware

Gin Middleware 适合处理鉴权、日志、CORS、请求 ID、限流等横切能力。根据问题内容决定调用哪种
Search 属于业务决策，应由 Application 层的 `SearchOrchestrator` / `QueryPlanner` 负责。

这样可以避免：

- Middleware 直接依赖数据库、Embedding 或专业检索实现；
- Handler 内出现大量 `if language == ...`；
- HTTP 框架与检索策略绑定；
- 单元测试必须启动 Gin 才能验证检索选择规则。

## 三、五个不能混淆的输入

| 输入 | 含义 | 示例 |
|---|---|---|
| `query` | 用户真正的问题 | “这篇论文怎样检测控制器故障？” |
| `response_language` | 最终答案语言 | `zh`、`en`、`auto` |
| `query_language` | 问题文本的主要语言 | `zh`、`en`、`mixed`、`auto` |
| `search_mode` | 希望使用的检索方式 | `auto`、`keyword`、`semantic` |
| `task_type` | 问题属于哪类专业任务 | `overview`、`method`、`formula`、`compare` |

`response_language` 不能直接决定检索能力。中文问题可能需要搜索英文论文，英文回答也可能引用中文原文；
检索计划至少要结合问题语言、任务类型、文档范围和用户显式选择。

## 四、第一版规划规则

未来第一版编排器优先使用确定性规则，不额外调用大模型判断路由：

1. 用户显式提供合法 `search_mode` 时优先遵守；
2. DOI、标题、作者、年份等结构化条件优先走 Metadata Search；
3. 引号包围的精确短语、符号或专业缩写可以补充 Keyword Search；
4. 普通自然语言问题默认走 Semantic Search；
5. `compare` 等跨文档任务可以形成多步计划，但每一步仍调用已验证的原子能力；
6. 某项能力不可用时只能按冻结规则降级，不能静默改用语义完全不同的能力。

只有确定性规则无法覆盖真实问题、并有冻结测试集证明收益后，才讨论使用模型作为 Query Planner。

## 五、接口与分层候选

现有原子接口继续保留，方便调试、评估和智能体精确调用：

```text
GET  /search
POST /semantic-search
POST /answers
```

未来可以新增统一入口，例如 `POST /retrieval`，但只有在以下契约冻结后才实现：

- 统一请求字段与枚举；
- 原子能力共同返回的最小 Hit/Evidence 字段；
- 多路结果的排序、去重和来源解释；
- 前端如何显示“实际采用了什么策略”；
- Codex 等智能体如何显式覆盖自动规划；
- 失败、降级、零命中与向量未就绪的状态语义。

组合根 `main.go` 仍只负责创建能力并注入编排器，不负责根据问题内容编写选择规则。

## 六、分阶段开发

1. **当前阶段**：冻结关键词、语义检索和带来源问答的基础契约，不继续为单接口追加专业规则；
2. **任务拆分阶段**：收集真实问题并标记 `task_type`、语言、范围和期望能力；
3. **原子能力阶段**：分别开发并验收 Metadata、Concept、Formula 等确有需求的 Search；
4. **统一模型阶段**：定义内部 `SearchPlan`、`SearchResult` 和能力端口；
5. **确定性编排阶段**：实现规则式 Query Planner 和可观察的降级行为；
6. **统一入口阶段**：证据充分后再决定是否公开 `POST /retrieval`；
7. **高级阶段**：再评估混合检索、rerank 或模型驱动规划，不能提前引入。

## 七、边界与风险

- 不允许前端传入任意后端函数名，只接受白名单枚举；
- 不把输出语言与检索语言绑定；
- 不让编排器直接实现 SQL 或远程 HTTP；
- 不让某个专业 Search 返回完全不同且无法统一解释的来源格式；
- 每次执行需要记录实际选择的能力、耗时和降级原因，但不能记录 API Key 或敏感全文；
- 当前证据可回答性优化、混合检索和 chunk overlap 作为后续细分任务保留，不继续修改基础接口。
