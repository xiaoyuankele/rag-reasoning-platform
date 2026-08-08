# PDF 文献处理架构与分阶段路线

> 状态：PDF-1、PDF-2 已完成；PDF-3 代码与自动化纵向测试已完成，真实文献 HTTP 验收待执行。
> 最近更新：2026-08-07。

## 1. 目标

本方案用于在现有 Go 异步任务链路中逐步增加 Python PDF 文献处理能力，同时满足：

- 普通数字 PDF 能够逐页提取并生成统一文本块；
- 密码、提取权限、扫描件、损坏文件和资源超限能够被稳定分类；
- 文本块保留页码来源，为后续检索和引用奠定基础；
- Python 不访问 PostgreSQL、不修改任务状态、不调用 Go HTTP API；
- 单文档失败不会拖垮 Go 服务或影响 Markdown/TXT 基础能力；
- 第一版适合本机低内存、单并发运行，后续可以替换解析器而不修改 Worker。

## 2. 已有基础

当前生产调用链已经具备：

```text
Worker
  -> ProcessorDispatcher
  -> PythonProcessor
  -> Python CLI
  -> Go/Python v1 JSON 契约
  -> ProcessingResult
  -> text_chunks
```

已有的安全边界包括：

- Go 只向 Python 提供经过 `LocalStorage` 校验的绝对路径；
- 一次子进程只处理一份文档；
- Worker 使用 context 控制单文档超时和停机取消；
- stdout 默认限制为 32 MiB，stderr 默认限制为 1 MiB；
- Go 严格校验 Python 响应、请求 ID、失败码和 chunk 不变量；
- 进程崩溃、协议错误和结构化文档失败属于不同错误类别；
- Python 失败后由 Worker 负责安全地更新任务和文档状态。

当前 Python CLI 已接入 PDF 预检，能够稳定区分损坏文件、密码要求、提取权限限制、OCR 需求以及文件和页数超限。普通数字 PDF 已能逐页提取、规范化并生成带物理页码的统一文本块；两页测试 PDF 已完成 Python CLI、生产 Go 适配器和 PostgreSQL 持久化的自动化纵向验证。

## 3. 目标处理流水线

```text
可信 PDF 绝对路径
  -> PDF 预检
     -> 文件可读性
     -> 加密与密码状态
     -> 文字提取权限
     -> 页数和资源限制
  -> 逐页提取
  -> 扫描件判断
  -> 文本规范化
  -> 带页码分块
  -> v1 响应
  -> Go 二次校验
  -> PostgreSQL 原子替换 chunks
```

上传成功不等于一定能够解析。上传层只负责安全接收文件；耗时、复杂且可能失败的 PDF 检查由后台 Worker 完成。

## 4. PDF 结果分类

| 情况 | 稳定错误码 | 自动重试 | 处理策略 |
| --- | --- | --- | --- |
| 普通数字 PDF | 无 | 不适用 | 提取并生成 chunks |
| 需要打开密码 | `password_required` | 否 | 提示上传解除密码后的副本 |
| PDF 禁止文字提取 | `extraction_not_permitted` | 否 | 尊重权限并拒绝提取 |
| 操作系统拒绝读取源文件 | `source_access_denied` | 否 | 修复本机存储权限后人工重试 |
| 扫描件或没有有效文字层 | `ocr_required` | 否 | 第一版不自动 OCR |
| 文件损坏或结构无效 | `invalid_content` | 否 | 提示文件无效 |
| 文件、页数或内容流超限 | `resource_limit_exceeded` | 否 | 主动终止以保护内存 |
| 解析器无法处理合法 PDF | `parse_failed` | 通常否 | 后端保留完整错误链 |
| Python 内部非预期错误 | `internal_error` | 可有限重试 | 后端日志记录，前端使用安全消息 |

`retryable` 表示当前任务是否适合自动重试，不表示用户完成外部修复后永远不能再次提交。

## 5. 密码与权限策略

### 5.1 第一版不接收 PDF 密码

异步 Worker 如果接受密码，就必须解决密码加密存储、任务领取、日志脱敏、生命周期和销毁问题。第一版不建立秘密管理系统：

- 需要非空打开密码时返回 `password_required`；
- 前端提示用户上传解除密码后的副本；
- 密码不得写入数据库、任务错误、日志或 Go/Python JSON 契约。

如果以后确认有真实需求，再单独设计短期秘密存储，不能直接给 `document_jobs` 增加明文密码字段。

### 5.2 尊重 PDF 提取权限

对于可以用空密码打开、但权限位禁止复制或提取文字的 PDF，产品策略是拒绝提取并返回 `extraction_not_permitted`。即使底层库技术上可以读取，也不主动绕过文档设置。

### 5.3 区分三种“权限失败”

- `password_required`：PDF 加密认证没有通过；
- `extraction_not_permitted`：PDF 已经可以打开，但文档权限禁止提取；
- `source_access_denied`：操作系统不允许 Python 读取本地文件。

它们的修复方法不同，不能都压缩成一个无法判断的 `parse_failed`。

## 6. 统一文本块与来源信息

学术文献后续需要展示引用页码。正式实现 PDF 提取前，统一 chunk 模型应增加最小来源信息：

```go
type ChunkInput struct {
    Index     int
    Content   string
    PageStart *int
    PageEnd   *int
}
```

约定：

- Markdown/TXT 没有固定页码，两个字段为 `nil`；
- PDF 页码从 1 开始；
- 第一版 PDF chunk 不跨页，因此起止页相同；
- 将来允许跨页时可以保留页码范围；
- 暂时不加入坐标、表格编号和完整章节树，避免过度设计。

数据库 `text_chunks` 将增加可空的 `page_start`、`page_end` 以及对应约束。Go/Python 契约、领域模型、PostgreSQL 仓储和测试必须在同一阶段同步更新。

## 7. Python 内部结构

随着普通数字 PDF 纵向链路稳定，Python 已从最小脚本结构渐进整理为轻量分层结构：

```text
ai/src/rag_ai/
├─ domain/                         # 稳定模型和处理错误
├─ application/                    # 文档处理用例与端口
├─ contracts/
│  └─ document_processing_v1.py    # Go/Python v1 JSON 契约
├─ entrypoints/
│  ├─ document_processing_cli.py   # stdin/stdout 进程入口与组合根
│  └─ document_processing_handler.py # 契约 DTO 与领域对象转换
└─ infrastructure/
   ├─ parsing/pypdf_extractor.py   # pypdf 页面提取适配器
   └─ splitting/simple_text_splitter.py # 轻量文本分块适配器
```

责任边界：

- `contracts/document_processing_v1.py`：只负责 v1 请求/响应 DTO、校验和序列化载荷；
- `entrypoints/document_processing_cli.py`：只负责 stdin、stdout、依赖组装和最终异常边界；
- `entrypoints/document_processing_handler.py`：负责契约 DTO 与领域对象的双向转换；
- `application/document_processor.py`：编排提取、规范化和分块，不认识 JSON 或 pypdf；
- `application/ports.py`：定义入站用例与出站提取、切分端口；
- `infrastructure/`：实现 pypdf 和具体文本切分能力。

依赖方向保持为 `entrypoints -> application -> domain`，基础设施通过 application 定义的
Protocol 插入用例。未来加入 OCR、DOCX 或 LangChain 时优先增加适配器，不让框架对象渗透
到 application 和 domain。

## 8. 解析库策略

第一版选择 `pypdf[crypto]`：

- 使用 pypdf 完成加密状态、权限、页数和数字文字提取；
- 使用 `crypto` extra 支持 AES PDF；
- 安装后锁定精确依赖版本；
- 暂时不同时引入多个 PDF 引擎。

官方资料：

- [pypdf 文本提取与内存说明](https://pypdf.readthedocs.io/en/latest/user/extract-text.html)
- [pypdf 加密与解密说明](https://pypdf.readthedocs.io/en/latest/user/encryption-decryption.html)
- [pypdf 权限接口](https://pypdf.readthedocs.io/en/stable/_modules/pypdf/_doc_common.html)

第二阶段质量评估时，可以比较 `pdfplumber` 对双栏、表格和布局的效果。暂不直接采用 PyMuPDF，因为其 AGPL/商业双许可证需要单独评估项目发布方式。

## 9. 资源保护

项目上传上限为 200 MiB，但第一版 PDF 解析能力可以使用更低的独立上限。初始建议：

- PDF 解析文件上限：50 MiB；
- 最大页数：500；
- Worker 单文档超时：沿用 5 分钟；
- Python stdout：32 MiB；
- Python stderr：1 MiB；
- Worker 并发数：1；
- 逐页提取和分块，不在 Python 中长期保留所有页面布局对象。

这些值应进入配置并经过测试，而不是散落在解析函数中。完成真实样本测量后再调整。

pypdf 官方说明某些大型解压内容流可能消耗远高于文件体积的内存。因此，文件大小和页数限制不能完全替代未来的进程级内存隔离。Windows 本地 MVP 先依靠单并发、超时、输出限制和保守解析上限；部署阶段再评估容器内存限制或操作系统级进程约束。

## 10. 扫描件和部分空白页面

第一版不安装 OCR：

- 整份文档几乎没有有效文字时返回 `ocr_required`；
- 个别封面、图片或空白页没有文字时，不直接判定整份文档失败；
- 不对所有 PDF 强制 OCR，避免增加资源消耗和识别误差；
- 后续 OCR 只处理真正需要的页面，并且能够通过配置关闭。

第一版暂不增加 `succeeded_with_warnings` 状态。部分空白页先通过后端诊断记录，等前端确实需要展示警告时再扩充任务模型。

## 11. 分阶段开发与验收

### PDF-1：来源页码与错误分类地基

工作：

- chunk 领域模型增加可选页码范围；
- 数据库迁移增加 `page_start`、`page_end`；
- PostgreSQL 仓储和测试同步；
- Go/Python v1 契约同步可选页码；
- 增加 PDF 稳定失败码；
- 任务查询能够逐步暴露安全错误码。

验收：Markdown/TXT 原有链路不变，带页码和不带页码的 chunk 都能原子入库并正确查询。

### PDF-2：PDF 预检

工作：

- 添加并锁定 `pypdf[crypto]`；
- 识别正常、密码、禁止提取、损坏、超页数和源文件权限失败；
- 所有库异常转换为稳定内部错误；
- CLI 继续只在 stdout 输出一个合法 JSON。

验收：每类样本都有确定错误码，错误不包含密码、完整本地路径或文件正文。

### PDF-3：普通数字 PDF 垂直链路

工作：

- 按页提取数字文字；
- 规范化空白和常见断行；
- 按页生成带来源的 chunks；
- 完成真实上传、排队、处理、入库和 ready 状态验证。

验收：一份普通中英文数字 PDF 能从 HTTP 上传一直处理到 PostgreSQL 文本块，查询结果保留正确页码。

当前进度：

- 已完成逐页文字提取、基础空白规范化、整份无文字时的 OCR 分类；
- 已完成页内分块、全局连续 chunk 序号和物理页码映射；
- 已用合成两页数字 PDF 验证 Python CLI -> Go `PythonProcessor` -> PostgreSQL；
- 待用真实中文和英文文献完成 HTTP 上传、排队、`ready` 状态与正文质量验收。

### PDF-4：学术文献质量改进

样本覆盖：

- 单栏和双栏论文；
- 中文和英文；
- 页眉页脚；
- 连字符断行；
- 参考文献；
- 表格、公式和图注。

先建立质量样本和测量，再决定是否增加 pdfplumber 或第二解析引擎。

### PDF-5：可选 OCR

- 检测扫描页；
- 只对需要的页面 OCR；
- 默认关闭并设置独立超时、页数和资源上限；
- OCR 不可用时不影响 Go 文档管理和普通 PDF 处理。

### PDF-6：密码支持评估

只有真实需求足够强时才开展。必须先设计短期秘密存储、加密、读取授权、日志脱敏和任务结束销毁流程。

## 12. 测试样本原则

自动化测试至少覆盖：

- 普通数字 PDF；
- 空密码可打开的加密 PDF；
- 需要非空密码的 PDF；
- 禁止提取文字的 PDF；
- 损坏或截断 PDF；
- 扫描 PDF；
- 超页数或资源限制 PDF；
- Python 超时和进程崩溃。

不能提交的论文原文放在被 Git 忽略的本地测试目录，只记录匿名化质量指标。可提交测试夹具必须自行生成或确认许可证允许再分发。

## 13. 明确暂不实现

- 不在第一版保存用户 PDF 密码；
- 不默认对全部 PDF 执行 OCR；
- 不承诺精确还原所有表格、公式和阅读顺序；
- 不让 Python 访问 PostgreSQL；
- 不让 PDF 解析失败影响 Markdown/TXT 和文档管理接口；
- 不同时引入多个重量级 PDF/AI 框架；
- 不在缺少真实样本测量时提前实现复杂版面算法。
