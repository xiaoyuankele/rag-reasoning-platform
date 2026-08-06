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


@dataclass(frozen=True)
class ProcessingRequest:
    """Validated v1 request received from Go."""

    contract_version: str
    request_id: str
    document: ProcessingDocument
    options: ProcessingOptions


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


def process_request(request: ProcessingRequest) -> dict[str, Any]:
    """Dispatch a validated request to a future PDF or DOCX parser.

    The protocol is ready before parser libraries are selected. Returning a
    structured unsupported error proves the failure path without pretending
    that complex document parsing already exists.
    """

    raise ContractError(
        "unsupported_format",
        f"no Python processor is registered for {request.document.mime_type!r}",
    )


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
    _require_exact_keys(
        options,
        {"max_chunk_characters"},
        "options",
    )

    max_chunk_characters = _require_integer(
        options["max_chunk_characters"],
        "options.max_chunk_characters",
    )
    if not MIN_CHUNK_CHARACTERS <= max_chunk_characters <= MAX_CHUNK_CHARACTERS:
        raise ContractError(
            "invalid_request",
            "options.max_chunk_characters must be between "
            f"{MIN_CHUNK_CHARACTERS} and {MAX_CHUNK_CHARACTERS}",
        )

    return ProcessingOptions(
        max_chunk_characters=max_chunk_characters,
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
