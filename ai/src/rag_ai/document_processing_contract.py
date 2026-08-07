"""Versioned wire contract shared with the Go Python processor adapter."""

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
REQUEST_ID_PATTERN = re.compile(r"^[A-Za-z0-9._:-]+$")


@dataclass(frozen=True)
class ProcessingDocument:
    """Document fields that Python is allowed to read."""

    id: int
    original_name: str
    source_path: Path
    mime_type: str


@dataclass(frozen=True)
class ProcessingOptions:
    """Options controlled by Go for one processing invocation."""

    max_chunk_characters: int
    max_pdf_file_bytes: int
    max_pdf_pages: int


@dataclass(frozen=True)
class ProcessingRequest:
    """Validated v1 request received from Go."""

    contract_version: str
    request_id: str
    document: ProcessingDocument
    options: ProcessingOptions


@dataclass(frozen=True)
class ProcessingChunk:
    """One normalized text chunk produced by a document parser."""

    index: int
    content: str
    page_start: int | None = None
    page_end: int | None = None


class ContractError(Exception):
    """Expected request or document failure that can cross the process boundary."""

    def __init__(
        self,
        code: str,
        message: str,
        *,
        retryable: bool = False,
    ) -> None:
        super().__init__(message)
        self.code = code
        self.message = message
        self.retryable = retryable


def parse_request(payload: Any) -> ProcessingRequest:
    """Validate an untrusted JSON value and return a typed request."""

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
) -> dict[str, Any]:
    """Build a validated success response for the Go backend."""

    _validate_request_id(request_id)
    _validate_processing_chunks(chunks)

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

    return {
        "contract_version": CONTRACT_VERSION,
        "request_id": request_id,
        "status": "succeeded",
        "chunks": chunk_payloads,
    }


def failure_response(
    request_id: str,
    error: ContractError,
) -> dict[str, Any]:
    """Build a v1 failure response without exposing exception internals."""

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
    """Extract a valid correlation ID for an invalid-request response."""

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


def _validate_processing_chunks(chunks: list[ProcessingChunk]) -> None:
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
    actual = set(value)
    if actual != expected:
        missing = sorted(expected - actual)
        unknown = sorted(actual - expected)
        raise ContractError(
            "invalid_request",
            f"{field} fields are invalid; missing={missing}, unknown={unknown}",
        )


def _require_string(value: Any, field: str) -> str:
    if not isinstance(value, str):
        raise ContractError(
            "invalid_request",
            f"{field} must be a string",
        )
    return value


def _require_nonblank_string(value: Any, field: str) -> str:
    text = _require_string(value, field)
    if not text.strip():
        raise ContractError(
            "invalid_request",
            f"{field} must not be blank",
        )
    return text


def _require_integer(value: Any, field: str) -> int:
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
    actual = set(value)
    missing = sorted(required - actual)
    unknown = sorted(actual - allowed)
    if missing or unknown:
        raise ContractError(
            "invalid_request",
            f"{field} fields are invalid; missing={missing}, unknown={unknown}",
        )
