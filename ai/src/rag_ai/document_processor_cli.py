"""供 Go 后端启动的单请求文档处理 CLI 入口。"""

from __future__ import annotations

import json
import sys
from typing import Any

from rag_ai.document_processing_contract import (
    ContractError,
    failure_response,
    parse_request,
    safe_request_id,
)
from rag_ai.document_processor import process_request
from rag_ai.parsing.errors import DocumentProcessingError


def main() -> int:
    """从 stdin 读取一条 JSON 请求，并向 stdout 写一条 JSON 响应。

    Returns:
        进程退出码。只要 CLI 能按协议返回成功或结构化失败 JSON，就返回 0；
        无法进入本函数等进程级故障才由操作系统产生非零退出码。

    Notes:
        stdout 只能写协议 JSON；诊断信息必须写入 stderr，避免 Go 解码失败。
    """

    payload: Any = None
    request_id = "invalid-request"

    try:
        payload = json.load(sys.stdin)
        request_id = safe_request_id(payload)
        request = parse_request(payload)
        request_id = request.request_id
        response = process_request(request)
    except json.JSONDecodeError:
        response = failure_response(
            request_id,
            ContractError("invalid_request", "stdin must contain one JSON object"),
        )
    except ContractError as error:
        response = failure_response(request_id, error)
    except DocumentProcessingError as error:
        response = failure_response(
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
        response = failure_response(
            request_id,
            ContractError(
                "internal_error",
                "Python document processor failed unexpectedly",
                retryable=True,
            ),
        )

    json.dump(
        response,
        sys.stdout,
        ensure_ascii=False,
        separators=(",", ":"),
    )
    sys.stdout.write("\n")
    return 0


if __name__ == "__main__":
    raise SystemExit(main())
