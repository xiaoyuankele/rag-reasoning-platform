"""Go 与 Python 文档处理适配器共享的 v1 JSON 契约。"""

from __future__ import annotations

from dataclasses import dataclass
from pathlib import Path
import re
from typing import Any


CONTRACT_VERSION = "v1"
MAX_REQUEST_ID_LENGTH = 128
MIN_CHUNK_CHARACTERS = 1
MAX_CHUNK_CHARACTERS = 100_000
DEFAULT_PDF_FILE_BYTES = 50 * 1024 * 1024
DEFAULT_PDF_PAGES = 500
MAX_PDF_FILE_BYTES = 1024 * 1024 * 1024
MAX_PDF_PAGES = 10_000
MAX_DETECTED_TITLE_CHARACTERS = 500
REQUEST_ID_PATTERN = re.compile(r"^[A-Za-z0-9._:-]+$")


@dataclass(frozen=True)
class ProcessingDocument:
    """Go 允许 Python 读取的文档信息。

    Attributes:
        id: PostgreSQL 中的正整数文档 ID。
        original_name: 用户上传时的原始文件名，仅用于处理上下文。
        source_path: Go 在受控存储根目录中解析出的绝对路径。
        mime_type: Go 已确认并用于选择处理器的 MIME 类型。
    """

    id: int
    original_name: str
    source_path: Path
    mime_type: str


@dataclass(frozen=True)
class ProcessingOptions:
    """Go 为单次 Python 处理调用提供的安全限制。

    Attributes:
        max_chunk_characters: 单个统一文本块允许的最大字符数。
        max_pdf_file_bytes: PDF 解析文件上限，独立于上传文件上限。
        max_pdf_pages: 单份 PDF 允许解析的最大物理页数。
    """

    max_chunk_characters: int
    max_pdf_file_bytes: int
    max_pdf_pages: int


@dataclass(frozen=True)
class ProcessingRequest:
    """从 Go 收到并通过契约校验的 v1 处理请求。

    Attributes:
        contract_version: 当前固定为 ``v1``。
        request_id: 用于关联 Go 请求和 Python 响应的 ID。
        document: Python 被授权处理的文档信息。
        options: 本次处理必须遵守的资源和分块限制。
    """

    contract_version: str
    request_id: str
    document: ProcessingDocument
    options: ProcessingOptions


@dataclass(frozen=True)
class ProcessingChunk:
    """格式处理器生成的一条统一文本块。

    Attributes:
        index: 从 0 开始连续递增的块序号。
        content: 去除首尾空白后不能为空的文本内容。
        page_start: 可选的起始物理页码；PDF 页码从 1 开始。
        page_end: 可选的结束物理页码；必须和 ``page_start`` 同时出现。
    """

    index: int
    content: str
    page_start: int | None = None
    page_end: int | None = None


@dataclass(frozen=True)
class ProcessingMetrics:
    """可选的 Python 文档处理分阶段耗时响应 DTO。"""

    python_total_ms: int
    source_open_ms: int
    metadata_read_ms: int
    text_extract_ms: int
    text_split_ms: int
    page_count: int
    slowest_page_number: int
    slowest_page_ms: int


class ContractError(Exception):
    """能够安全跨进程返回的契约或文档处理失败。

    Attributes:
        code: v1 契约中的稳定错误码。
        message: 不包含密钥、绝对路径和正文的安全说明。
        retryable: Go Worker 是否适合有限自动重试。
    """

    def __init__(
        self,
        code: str,
        message: str,
        *,
        retryable: bool = False,
    ) -> None:
        """根据稳定错误码、安全消息和重试策略创建契约异常。"""

        super().__init__(message)
        self.code = code
        self.message = message
        self.retryable = retryable


def parse_request(payload: Any) -> ProcessingRequest:
    """校验来自 stdin 的不可信 JSON 值并转换为强类型请求。

    Args:
        payload: ``json.load`` 产生的任意 Python 值，通常是字典。

    Returns:
        字段、类型、范围和版本均已校验的 ``ProcessingRequest``。

    Raises:
        ContractError: 请求结构、字段、类型、取值、路径或契约版本不合法。
    """

    request = _require_object(payload, "request")
    _require_exact_keys(
        request,
        {"contract_version", "request_id", "document", "options"},
        "request",
    )

    contract_version = _require_string(
        request["contract_version"],
        "contract_version",
    )
    if contract_version != CONTRACT_VERSION:
        raise ContractError(
            "invalid_request",
            f"unsupported contract version: {contract_version!r}",
        )

    request_id = _validate_request_id(request["request_id"])
    document = _parse_document(request["document"])
    options = _parse_options(request["options"])

    return ProcessingRequest(
        contract_version=contract_version,
        request_id=request_id,
        document=document,
        options=options,
    )


def success_response(
    request_id: str,
    chunks: list[ProcessingChunk],
    *,
    detected_title: str | None = None,
    metrics: ProcessingMetrics | None = None,
) -> dict[str, Any]:
    """校验处理结果并构造供 Go 解码的 v1 成功响应。

    Args:
        request_id: 必须与请求相同的关联 ID。
        chunks: 格式处理器生成、顺序稳定的统一文本块列表。
        detected_title: 处理器自动识别并规范化的可选标题。
        metrics: 可选的 Python 内部阶段观测数据。

    Returns:
        可以交给 ``json.dump`` 的成功响应字典。

    Raises:
        ContractError: 请求 ID 或文本块的序号、内容、页码不合法。
    """

    _validate_request_id(request_id)
    _validate_processing_chunks(chunks)
    _validate_detected_title(detected_title)
    _validate_processing_metrics(metrics)

    chunk_payloads: list[dict[str, Any]] = []
    for chunk in chunks:
        chunk_payload: dict[str, Any] = {
            "index": chunk.index,
            "content": chunk.content,
        }
        if chunk.page_start is not None and chunk.page_end is not None:
            chunk_payload["page_start"] = chunk.page_start
            chunk_payload["page_end"] = chunk.page_end

        chunk_payloads.append(chunk_payload)

    response: dict[str, Any] = {
        "contract_version": CONTRACT_VERSION,
        "request_id": request_id,
        "status": "succeeded",
        "chunks": chunk_payloads,
    }
    if detected_title is not None:
        response["metadata"] = {"title": detected_title}
    if metrics is not None:
        response["metrics"] = {
            "python_total_ms": metrics.python_total_ms,
            "source_open_ms": metrics.source_open_ms,
            "metadata_read_ms": metrics.metadata_read_ms,
            "text_extract_ms": metrics.text_extract_ms,
            "text_split_ms": metrics.text_split_ms,
            "page_count": metrics.page_count,
            "slowest_page_number": metrics.slowest_page_number,
            "slowest_page_ms": metrics.slowest_page_ms,
        }

    return response


def failure_response(
    request_id: str,
    error: ContractError,
) -> dict[str, Any]:
    """把安全契约异常转换为供 Go 解码的 v1 失败响应。

    Args:
        request_id: 当前能够确定的请求关联 ID。
        error: 已经完成安全分类的 ``ContractError``。

    Returns:
        包含稳定错误码、安全消息和重试标记的响应字典。
    """

    return {
        "contract_version": CONTRACT_VERSION,
        "request_id": request_id,
        "status": "failed",
        "error": {
            "code": error.code,
            "message": error.message,
            "retryable": error.retryable,
        },
    }


def safe_request_id(payload: Any) -> str:
    """尽量从非法请求中提取仍可安全回传的关联 ID。

    Args:
        payload: 尚未通过完整契约校验的 JSON 值。

    Returns:
        合法的原始 ``request_id``；无法提取时返回 ``invalid-request``。
    """

    if isinstance(payload, dict):
        candidate = payload.get("request_id")
        if (
            isinstance(candidate, str)
            and 0 < len(candidate) <= MAX_REQUEST_ID_LENGTH
            and REQUEST_ID_PATTERN.fullmatch(candidate)
        ):
            return candidate
    return "invalid-request"


def _parse_document(value: Any) -> ProcessingDocument:
    """校验 document 对象并转换为 ``ProcessingDocument``。"""

    document = _require_object(value, "document")
    _require_exact_keys(
        document,
        {"id", "original_name", "source_path", "mime_type"},
        "document",
    )

    document_id = _require_integer(document["id"], "document.id")
    if document_id <= 0:
        raise ContractError(
            "invalid_request",
            "document.id must be positive",
        )

    original_name = _require_nonblank_string(
        document["original_name"],
        "document.original_name",
    )
    source_path_text = _require_nonblank_string(
        document["source_path"],
        "document.source_path",
    )
    source_path = Path(source_path_text)
    if not source_path.is_absolute():
        raise ContractError(
            "invalid_request",
            "document.source_path must be absolute",
        )

    mime_type = _require_nonblank_string(
        document["mime_type"],
        "document.mime_type",
    )

    return ProcessingDocument(
        id=document_id,
        original_name=original_name,
        source_path=source_path,
        mime_type=mime_type,
    )


def _parse_options(value: Any) -> ProcessingOptions:
    """校验 options 对象并为旧版 v1 请求补充 PDF 默认限制。"""

    options = _require_object(value, "options")
    _require_required_and_allowed_keys(
        options,
        required={"max_chunk_characters"},
        allowed={
            "max_chunk_characters",
            "max_pdf_file_bytes",
            "max_pdf_pages",
        },
        field="options",
    )

    max_chunk_characters = _require_integer(
        options["max_chunk_characters"],
        "options.max_chunk_characters",
    )
    _require_bounded_integer(
        max_chunk_characters,
        "options.max_chunk_characters",
        minimum=MIN_CHUNK_CHARACTERS,
        maximum=MAX_CHUNK_CHARACTERS,
    )

    max_pdf_file_bytes = _require_integer(
        options.get("max_pdf_file_bytes", DEFAULT_PDF_FILE_BYTES),
        "options.max_pdf_file_bytes",
    )
    _require_bounded_integer(
        max_pdf_file_bytes,
        "options.max_pdf_file_bytes",
        minimum=1,
        maximum=MAX_PDF_FILE_BYTES,
    )

    max_pdf_pages = _require_integer(
        options.get("max_pdf_pages", DEFAULT_PDF_PAGES),
        "options.max_pdf_pages",
    )
    _require_bounded_integer(
        max_pdf_pages,
        "options.max_pdf_pages",
        minimum=1,
        maximum=MAX_PDF_PAGES,
    )

    return ProcessingOptions(
        max_chunk_characters=max_chunk_characters,
        max_pdf_file_bytes=max_pdf_file_bytes,
        max_pdf_pages=max_pdf_pages,
    )


def _validate_request_id(value: Any) -> str:
    """校验请求关联 ID 的类型、长度和允许字符。"""

    request_id = _require_string(value, "request_id")
    if (
        not request_id
        or len(request_id) > MAX_REQUEST_ID_LENGTH
        or not REQUEST_ID_PATTERN.fullmatch(request_id)
    ):
        raise ContractError(
            "invalid_request",
            "request_id has an invalid format",
        )
    return request_id


def _validate_detected_title(title: str | None) -> None:
    """校验可选标题已经完成空白规范化并满足长度上限。"""

    if title is None:
        return
    if (
        not isinstance(title, str)
        or not title
        or title != title.strip()
        or len(title) > MAX_DETECTED_TITLE_CHARACTERS
    ):
        raise ContractError(
            "internal_error",
            "Python processor produced an invalid document title",
            retryable=True,
        )


def _validate_processing_metrics(
    metrics: ProcessingMetrics | None,
) -> None:
    """校验可选阶段指标，防止无效观测数据进入 Go 与数据库。"""

    if metrics is None:
        return

    durations = (
        metrics.python_total_ms,
        metrics.source_open_ms,
        metrics.metadata_read_ms,
        metrics.text_extract_ms,
        metrics.text_split_ms,
        metrics.slowest_page_ms,
    )
    if any(
        isinstance(value, bool)
        or not isinstance(value, int)
        or value < 0
        for value in durations
    ):
        raise ContractError(
            "internal_error",
            "Python processor produced invalid processing metrics",
            retryable=True,
        )

    if (
        isinstance(metrics.page_count, bool)
        or not isinstance(metrics.page_count, int)
        or metrics.page_count < 1
        or isinstance(metrics.slowest_page_number, bool)
        or not isinstance(metrics.slowest_page_number, int)
        or not 1
        <= metrics.slowest_page_number
        <= metrics.page_count
    ):
        raise ContractError(
            "internal_error",
            "Python processor produced invalid page metrics",
            retryable=True,
        )


def _validate_processing_chunks(chunks: list[ProcessingChunk]) -> None:
    """校验成功响应文本块的序号、内容和可选页码范围。"""

    if not chunks:
        raise ContractError(
            "internal_error",
            "Python processor must produce at least one chunk",
            retryable=True,
        )

    for expected_index, chunk in enumerate(chunks):
        if isinstance(chunk.index, bool) or chunk.index != expected_index:
            raise ContractError(
                "internal_error",
                "Python processor produced non-contiguous chunk indexes",
                retryable=True,
            )
        if not isinstance(chunk.content, str) or not chunk.content.strip():
            raise ContractError(
                "internal_error",
                "Python processor produced blank chunk content",
                retryable=True,
            )

        has_page_start = chunk.page_start is not None
        has_page_end = chunk.page_end is not None
        if has_page_start != has_page_end:
            raise ContractError(
                "internal_error",
                "Python processor produced an incomplete page range",
                retryable=True,
            )
        if not has_page_start:
            continue

        page_start = chunk.page_start
        page_end = chunk.page_end
        if (
            isinstance(page_start, bool)
            or isinstance(page_end, bool)
            or not isinstance(page_start, int)
            or not isinstance(page_end, int)
            or page_start < 1
            or page_end < page_start
        ):
            raise ContractError(
                "internal_error",
                "Python processor produced an invalid page range",
                retryable=True,
            )


def _require_object(value: Any, field: str) -> dict[str, Any]:
    """要求契约字段是 JSON object 对应的 Python 字典。"""

    if not isinstance(value, dict):
        raise ContractError(
            "invalid_request",
            f"{field} must be an object",
        )
    return value


def _require_exact_keys(
    value: dict[str, Any],
    expected: set[str],
    field: str,
) -> None:
    """要求对象字段集合与预期集合完全相等。"""

    actual = set(value)
    if actual != expected:
        missing = sorted(expected - actual)
        unknown = sorted(actual - expected)
        raise ContractError(
            "invalid_request",
            f"{field} fields are invalid; missing={missing}, unknown={unknown}",
        )


def _require_string(value: Any, field: str) -> str:
    """要求契约字段是字符串并返回原值。"""

    if not isinstance(value, str):
        raise ContractError(
            "invalid_request",
            f"{field} must be a string",
        )
    return value


def _require_nonblank_string(value: Any, field: str) -> str:
    """要求字段是去除空白后仍有内容的字符串。"""

    text = _require_string(value, field)
    if not text.strip():
        raise ContractError(
            "invalid_request",
            f"{field} must not be blank",
        )
    return text


def _require_integer(value: Any, field: str) -> int:
    """要求字段是整数，并显式拒绝可冒充整数的布尔值。"""

    # Python bool inherits from int, so reject bool explicitly.
    if isinstance(value, bool) or not isinstance(value, int):
        raise ContractError(
            "invalid_request",
            f"{field} must be an integer",
        )
    return value


def _require_bounded_integer(
    value: int,
    field: str,
    *,
    minimum: int,
    maximum: int,
) -> None:
    """要求整数位于包含上下限的闭区间内。"""

    if not minimum <= value <= maximum:
        raise ContractError(
            "invalid_request",
            f"{field} must be between {minimum} and {maximum}",
        )


def _require_required_and_allowed_keys(
    value: dict[str, Any],
    *,
    required: set[str],
    allowed: set[str],
    field: str,
) -> None:
    """检查对象是否包含全部必需字段且没有未知字段。"""

    actual = set(value)
    missing = sorted(required - actual)
    unknown = sorted(actual - allowed)
    if missing or unknown:
        raise ContractError(
            "invalid_request",
            f"{field} fields are invalid; missing={missing}, unknown={unknown}",
        )
