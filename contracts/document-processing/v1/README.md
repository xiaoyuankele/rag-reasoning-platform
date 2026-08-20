# Document Processing Contract v1

本文档定义 Go 后端与 Python 文档处理子进程之间的 v1 协议。

## 责任边界

Go 负责：

- 从数据库领取任务；
- 校验并解析安全的本地文件绝对路径；
- 创建、超时取消和回收 Python 子进程；
- 写入文本块；
- 更新任务与文档状态。

Python 负责：

- 读取 Go 授权的单个源文件；
- 根据 MIME 类型解析内容；
- 输出统一文本块或结构化失败信息；
- 在 oneshot 模式处理完成后退出，或在 stream 模式等待下一条请求。

Python 不连接 PostgreSQL、不更新任务状态、不调用 Go HTTP API，也不修改上传文件。

## 传输规则

v1 的 JSON 消息结构支持两种进程传输模式，Schema 和业务语义完全相同。

### Oneshot

1. 一个 Python 子进程只处理一个请求。
2. Go 向标准输入写入一个 UTF-8 JSON 对象，然后关闭 stdin。
3. Python 向标准输出写入一个 UTF-8 JSON 响应对象并退出。

### Stream

1. Go 和 Python 使用 JSON Lines：一行请求严格对应一行响应。
2. JSON 正文中的换行必须由 JSON 编码器转义，不能产生额外物理行。
3. Python 每写完一条响应必须立即 flush，然后继续等待下一行。
4. `request_id` 必须与对应请求一致；不一致时 Go 销毁该进程。
5. Go 关闭 stdin 表示主动回收；Python 读到 EOF 后正常退出。
6. 单条结构化业务失败不能终止 stream 循环；进程崩溃、协议错误、超时或输出超限会让 Go 销毁并在后续请求时重建该槽位。

两种模式都必须满足：stdout 只能包含协议 JSON，诊断日志写入 stderr；合法成功或失败响应都表示单条请求已经完成；Go 必须限制单任务运行时间和 stdout/stderr 大小。非零退出码表示 Python 崩溃、启动失败或未能继续完成协议。

## 请求

Schema：`request.schema.json`

```json
{
  "contract_version": "v1",
  "request_id": "job-123",
  "document": {
    "id": 456,
    "original_name": "research.pdf",
    "source_path": "E:\\data\\documents\\document-456.pdf",
    "mime_type": "application/pdf"
  },
  "options": {
    "max_chunk_characters": 1000,
    "max_pdf_file_bytes": 52428800,
    "max_pdf_pages": 500
  }
}
```

`source_path` 必须由 Go 在受控存储根目录内安全解析为绝对路径。Python 不接受来自 HTTP 客户端的任意路径。

解析限制由 Go 配置并逐次传给 Python：

- `max_chunk_characters`：单个统一文本块的最大字符数；
- `max_pdf_file_bytes`：PDF 解析允许的最大源文件字节数，独立于上传上限；可选，省略时为 52428800（50 MiB）；
- `max_pdf_pages`：PDF 解析允许的最大页数；可选，省略时为 500。

两个 PDF 限制是 v1 后续增加的可选字段。新版 Go 始终发送；Python 保留默认值以兼容尚未增加字段的旧版 v1 请求。

## 成功响应

Schema：`success-response.schema.json`

```json
{
  "contract_version": "v1",
  "request_id": "job-123",
  "status": "succeeded",
  "chunks": [
    {
      "index": 0,
      "content": "normalized document content",
      "page_start": 1,
      "page_end": 1
    }
  ]
}
```

除 JSON Schema 外，还必须满足：

- 至少返回一个文本块；
- `index` 从 0 开始连续递增；
- `content` 去除首尾空白后不能为空；
- `page_start` 和 `page_end` 必须同时出现或同时省略；
- 页码从 1 开始，且 `page_end` 不能早于 `page_start`；
- Markdown/TXT 等没有固定页码的来源省略两个页码字段；
- 块顺序就是最终持久化顺序。

## 失败响应

Schema：`failure-response.schema.json`

```json
{
  "contract_version": "v1",
  "request_id": "job-123",
  "status": "failed",
  "error": {
    "code": "unsupported_format",
    "message": "document format is not supported",
    "retryable": false
  }
}
```

稳定错误码：

| code | 含义 | 通常是否重试 |
|---|---|---|
| `invalid_request` | 请求字段或版本不合法 | 否 |
| `unsupported_format` | 当前处理器不支持该 MIME | 否 |
| `source_not_found` | 授权文件不存在 | 否 |
| `source_access_denied` | 操作系统拒绝读取授权文件 | 否 |
| `password_required` | PDF 需要非空打开密码 | 否 |
| `extraction_not_permitted` | PDF 权限禁止提取文本 | 否 |
| `ocr_required` | 文件没有可用文本层，需要 OCR | 否 |
| `invalid_content` | 文件内容损坏或与格式不符 | 否 |
| `resource_limit_exceeded` | 文件、页数或解析资源超过限制 | 否 |
| `parse_failed` | 解析库无法完成处理 | 通常否 |
| `internal_error` | Python 内部非预期错误 | 可以有限重试 |

`message` 用于后端诊断，不能包含密钥或完整文件内容。Go 对前端仍使用自己的安全错误说明。

## 版本兼容

- v1 字段的类型和语义发布后不直接修改。
- 增加可选字段通常保持 v1 兼容。
- 删除字段、改变必填性或改变字段语义必须升级到 v2。
- Go 和 Python 都必须拒绝未知的 `contract_version`。

## 文件说明

```text
request.schema.json
success-response.schema.json
failure-response.schema.json
examples/request.json
examples/success-response.json
examples/failure-response.json
```

JSON Schema 是语言无关的协议说明；Go/Python 仍需要各自的类型和业务不变量校验。
