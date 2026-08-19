# F3 文档解析器替换与并行评估

> 状态：候选方案已调研，尚未安装或替换生产依赖。推荐先并行试点 Docling，不直接删除当前 pypdf 路径。

## 1. 当前架构允许替换，但现有模型过窄

项目当前锁定 `pypdf[crypto]==6.14.2`。Python Application 只依赖 `PageTextExtractor`、`DocumentTitleExtractor` 和
`TextSplitter` Protocol，因此可以增加新的 Infrastructure Adapter，而不必把第三方对象泄漏给 Go、数据库或前端。

但当前中间模型只有“逐页纯文本”，Go/Python v1 输出也只有 title、content 和 page range。如果新包识别出了标题、段落、
页眉页脚、表格、阅读顺序或坐标，却立即压平为 `PageText`，成熟解析器的大部分价值仍会丢失。因此正式升级需要先定义
`ParsedDocument / ParsedBlock` 之类的内部结构，随后再进行结构化 chunking。

## 2. 候选方案

| 方案 | 适合用途 | 主要边界 |
| --- | --- | --- |
| pypdf | 安全预检、加密/权限、快速纯文本基线与回退 | 扫描件需外部 OCR；复杂布局仍需自定义处理 |
| pdfplumber | 字符/单词坐标、表格、裁剪与可视化质量诊断 | 面向数字 PDF，无 OCR；正文和表格按阅读顺序合并仍需自定义逻辑 |
| Docling | 学术 PDF 结构恢复、标题层级、正文/页眉页脚、表格、来源、结构化 RAG chunking | 依赖与模型较重；大型 PDF 的 CPU 延迟、内存和镜像体积必须实测 |
| PyMuPDF / PyMuPDF4LLM | 高性能 PDF 和面向 RAG 的 Markdown/JSON 提取 | AGPL/商业双许可证，采用前必须确认项目发布和分发方式 |

Docling 的统一 `DoclingDocument` 能表达正文树、标题/分组、表格、图片、页眉页脚、边界框和来源；其 `HybridChunker` 在
文档结构之上按目标 embedding tokenizer 拆分过大块并合并同标题下的小块。这与本项目的 chunk 质量问题最接近，但仍需
使用项目真实中英文论文验证，不能把官方能力列表当成当前语料上的质量结论。

## 3. 推荐架构

```text
PDF 安全预检
→ Parser Adapter（pypdf baseline / Docling candidate）
→ 项目自有 ParsedDocument IR
   ├─ page + bbox
   ├─ heading path
   ├─ paragraph/sentence/block
   └─ table/caption/furniture
→ Structure-aware Chunker
→ Go/Python v2 契约
→ text_chunks + chunking_version
```

- pypdf 先保留为稳定基线、快速路径和 Docling 失败时的显式回退；
- 新增 `DoclingDocumentParser` Adapter，并立即转换到项目自有 IR；
- 不让 Docling、LangChain 或 LlamaIndex 类型进入 Application、Go 协议或数据库；
- 解析器选择使用配置/feature flag，并记录 `parser_name`、`parser_version`、`chunking_version`；
- 回退必须写入任务诊断，不能静默让同批文档混用不同质量而不可追踪。

## 4. 评估门禁

选择 10～20 份可合法本地使用的真实样本，至少覆盖中英文、单/双栏、公式、表格、页眉页脚、扫描页和异常字体，对比：

- 阅读顺序与段落/标题完整率；
- 页眉页脚重复率、断词和乱码比例；
- 表格/公式保真与来源页码/坐标；
- 解析耗时、峰值内存、安装与容器镜像体积；
- chunk 数量/Token 分布、关键词召回、语义 Recall@K 和问答来源正确率；
- 失败分类、超时、进程退出和回退是否可观测。

只有候选方案在质量收益上明显超过资源与维护成本，才切换默认解析器。切换后必须按新版本重新解析、替换 chunks 并重新
向量化；不能让新旧 parser/chunking 版本无标识混用。

## 5. 当前建议

第一选择：**Docling 作为增强解析器试点，pypdf 保留为 baseline/fallback**。如果当前目标只是定位表格、坐标和异常页面，
先增加 pdfplumber 审计工具会更轻。PyMuPDF 暂不进入依赖，除非先完成许可证评估。

官方资料：

- [pypdf 文本提取](https://pypdf.readthedocs.io/en/latest/user/extract-text.html)
- [pdfplumber README](https://github.com/jsvine/pdfplumber/blob/stable/README.md)
- [Docling Document](https://docling-project.github.io/docling/concepts/docling_document/)
- [Docling Chunking](https://docling-project.github.io/docling/concepts/chunking/)
- [Docling Slim 可选依赖](https://github.com/docling-project/docling/blob/main/packages/docling-slim/README.md)
- [PyMuPDF 官方文档与许可证说明](https://pymupdf.readthedocs.io/en/latest/)
