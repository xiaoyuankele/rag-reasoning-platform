# 文献标题与文档内检索架构

## 目的

第一版在不引入独立 `literatures` 表的前提下，让系统自动读取数字版 PDF 的标题、由 Go
保存到 PostgreSQL，并让列表、详情和搜索结果同时返回文献标题与原始文件名。

## 第一版对象边界

- `title`：用户主要查看的文献标题，可以为空；
- `original_name`：上传时的原始文件名，永久保留用于追溯；
- `storage_path`：后端内部稳定存储路径，不返回前端；
- `document_id`：系统和前端之间使用的稳定标识，用户不需要手工输入；
- `text_chunk`：具体文件产生的文本块，继续通过 `document_id` 关联来源文件。

第一版暂时把一个上传文件视为一条文献记录。未来需要让一篇文献关联多个文件时，可以
新增 `literatures` 表并给 `documents` 增加 `literature_id`；现有
`text_chunks.document_id` 关系保持不变。

## 标题数据流

```text
Python pypdf 读取 PDF 元数据 Title
→ 返回可选标题候选和文本块
→ Go PythonProcessor 校验并转换处理结果
→ Worker 保存文本块
→ 成功收尾事务写入 title，并同步 document/job 成功状态
→ 列表、详情和搜索接口从 PostgreSQL 返回最终标题
```

Python 不连接 PostgreSQL，也不决定是否覆盖已有标题。Go Application 负责业务策略，Go
Infrastructure 负责持久化。

## 第一版标题规则

1. 只读取 PDF 元数据中的标题，不做首页版式推测、OCR 或 DOI 查询；
2. 标题缺失不是处理失败，文档仍可进入 `ready`；
3. 自动标题必须去除首尾空白并遵守长度限制；
4. `title` 为空时，前端使用 `original_name` 作为展示兜底；
5. 后续用户确认或修改的标题拥有最高优先级，重新解析不能自动覆盖；
6. Markdown/TXT 第一版不自动生成标题。

## 搜索契约

搜索继续以文本块为结果单位，并增加可选 `document_id` 过滤：

```http
GET /search?q=control&document_id=20&page=1&page_size=20
```

前端先通过文档目录取得“标题与 ID”的对应关系，向用户展示标题，内部发送 ID。

单条搜索结果至少返回：

```json
{
  "chunk_id": 159,
  "document_id": 20,
  "chunk_index": 3,
  "title": "基于深度强化学习的磁浮列车协同控制",
  "original_name": "0459-1879-24-440.pdf",
  "mime_type": "application/pdf",
  "content": "命中的文本块内容",
  "page_start": 2,
  "page_end": 2
}
```

`document_id` 缺省时跨全部 `ready` 文档搜索；显式提供时必须是正整数。

## 下载文件名边界

第一版不修改服务器物理文件名。未来下载接口根据 `title` 动态生成安全的
`Content-Disposition` 文件名，标题为空时回退到 `original_name`。这样无需让数据库与
文件系统参加跨资源事务，也不会改变稳定的 `storage_path`。

## 第一版非目标

- 独立文献表和一篇文献关联多个文件；
- 自动修改服务器物理文件名；
- 首页版式标题识别、OCR 标题识别和 DOI 元数据查询；
- 相关性评分、向量检索和复杂过滤组合。

## 真实样本结论

2026-08-10 使用已验收的中英文学术 PDF 检查元数据，两份文件均没有可用的
`Title`，提取器按契约返回 `None`。这说明第一版能够覆盖“元数据完整”的
PDF，但实际文献仍需要 `original_name` 回退。首页标题识别和 DOI 元数据属于后续
增强，不扩大本阶段范围。

## 已记录但尚未实现的多策略标题识别

真实样本进一步检查表明，两份 PDF 的首页正文都包含文献标题，但元数据中没有
`/Title`。因此现有 `PyPDFTitleExtractor` 的行为符合“只读元数据”契约，但不等于
系统已经具备真实学术文献的完整标题识别能力。

后续候选方案按成本从低到高排列：

1. 优先使用合法的 PDF `/Title` 元数据；
2. 对数字版 PDF 的首页文本执行标题候选识别；
3. 识别 DOI 后通过外部元数据服务校对标题；
4. 对扫描版 PDF 可选启用 OCR 后再执行标题识别。

为避免低可信结果污染文献目录，未来 Python 宜返回带有 `value`、`source`和
`confidence` 的标题候选；Go 负责候选采用策略和持久化，用户手工确认的标题优先级
始终最高。本节仅是开发设想，尚未进入代码实现。
