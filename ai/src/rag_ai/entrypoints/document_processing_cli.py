"""供 Go 后端启动的文档处理 CLI 入口。"""

from __future__ import annotations

import json
import sys
from typing import Any

from rag_ai.application.document_processor import ProcessDocumentService
from rag_ai.contracts.document_processing_v1 import (
    ContractError,
    failure_response,
    parse_request,
    safe_request_id,
)
from rag_ai.domain.errors import DocumentProcessingError
from rag_ai.entrypoints.document_processing_handler import process_request
from rag_ai.infrastructure.parsing.pypdf_extractor import (
    PyPDFPageExtractor,
    PyPDFTitleExtractor,
)
from rag_ai.infrastructure.splitting.simple_text_splitter import (
    SimpleTextSplitter,
)


def configure_standard_streams() -> None:
    """把跨进程 JSON 标准输入和标准输出固定为 UTF-8。

    Windows 在管道模式下可能让 Python 使用系统代码页（例如 GBK）。真实
    文献经常包含数学符号和其他代码页无法表达的 Unicode 字符，因此不能
    依赖操作系统默认编码。Go/Python v1 契约明确使用 UTF-8 字节。

    Raises:
        OSError: Python 进程无法重新配置标准流；这种情况属于进程级故障，
            不应伪装成结构化文档处理失败。
    """

    sys.stdin.reconfigure(encoding="utf-8", errors="strict")
    sys.stdout.reconfigure(
        encoding="utf-8",
        errors="strict",
        newline="\n",
    )


def build_service() -> ProcessDocumentService:
    """创建当前 Python 进程复用的文档处理应用服务。

    Returns:
        已注入 PDF 提取器和文本切分器的应用服务。

    Notes:
        常驻模式只在进程启动时创建一次服务，从而复用已经导入的模块和
        无状态适配器。单份文档的数据仍只存在于一次 ``process`` 调用中。
    """

    return ProcessDocumentService(
        page_extractor=PyPDFPageExtractor(),
        title_extractor=PyPDFTitleExtractor(),
        text_splitter=SimpleTextSplitter(),
    )


def process_payload(
    payload: Any,
    service: ProcessDocumentService,
) -> dict[str, Any]:
    """把一条已完成 JSON 解码的请求转换为稳定协议响应。

    Args:
        payload: 来自 Go 的不可信 JSON 值。
        service: 当前 Python 进程复用的文档处理应用服务。

    Returns:
        成功结果或已经安全分类的失败响应。

    Notes:
        所有单文档异常都在这一边界收敛，因此 stream 模式中的一份坏文档
        不会让整个 Python 进程退出，也不会污染下一份请求。
    """

    request_id = safe_request_id(payload)

    try:
        request = parse_request(payload)
        request_id = request.request_id
        return process_request(request, service)
    except ContractError as error:
        return failure_response(request_id, error)
    except DocumentProcessingError as error:
        return failure_response(
            request_id,
            ContractError(
                error.code,
                error.message,
                retryable=error.retryable,
            ),
        )
    except Exception as error:  # pragma: no cover - defensive process boundary
        # stderr is for diagnostics only; stdout remains valid protocol JSON.
        print(
            f"unexpected Python processor error: {type(error).__name__}",
            file=sys.stderr,
        )
        return failure_response(
            request_id,
            ContractError(
                "internal_error",
                "Python document processor failed unexpectedly",
                retryable=True,
            ),
        )


def invalid_json_response() -> dict[str, Any]:
    """构造无法完成 JSON 解码时的稳定失败响应。"""

    return failure_response(
        "invalid-request",
        ContractError("invalid_request", "stdin must contain one JSON object"),
    )


def write_response(response: dict[str, Any]) -> None:
    """向 stdout 写入一条单行 UTF-8 JSON 响应并立即刷新。

    JSON Lines 使用物理换行作为消息边界。``json.dump`` 会把正文中的换行
    转义为 ``\\n``，因此一份响应始终只占 stdout 的一行。
    """

    json.dump(
        response,
        sys.stdout,
        ensure_ascii=False,
        separators=(",", ":"),
    )
    sys.stdout.write("\n")
    sys.stdout.flush()


def run_once(service: ProcessDocumentService) -> int:
    """执行兼容旧版行为的一次性 stdin/stdout 调用。"""

    try:
        payload: Any = json.load(sys.stdin)
        response = process_payload(payload, service)
    except json.JSONDecodeError:
        response = invalid_json_response()

    write_response(response)
    return 0


def run_stream(service: ProcessDocumentService) -> int:
    """通过 JSON Lines 在同一个 Python 进程中连续处理请求。

    每读取一行就输出一行响应。stdin 到达 EOF 表示 Go 主动回收进程，此时
    正常返回 0；单条请求失败只产生结构化失败响应，循环继续处理下一行。
    """

    for request_line in sys.stdin:
        try:
            payload: Any = json.loads(request_line)
            response = process_payload(payload, service)
        except json.JSONDecodeError:
            response = invalid_json_response()

        write_response(response)

    return 0


def main(arguments: list[str] | None = None) -> int:
    """根据启动参数执行一次性模式或常驻 JSON Lines 模式。

    Returns:
        进程退出码。只要 CLI 能按协议返回成功或结构化失败 JSON，就返回 0；
        无法进入本函数等进程级故障才由操作系统产生非零退出码。

    Notes:
        stdout 只能写协议 JSON；诊断信息必须写入 stderr，避免 Go 解码失败。
    """

    configure_standard_streams()
    service = build_service()
    selected_arguments = sys.argv[1:] if arguments is None else arguments

    if not selected_arguments:
        return run_once(service)
    if selected_arguments == ["--stream"]:
        return run_stream(service)

    print(
        "usage: python -m rag_ai.entrypoints.document_processing_cli [--stream]",
        file=sys.stderr,
    )
    return 2


if __name__ == "__main__":
    raise SystemExit(main())
