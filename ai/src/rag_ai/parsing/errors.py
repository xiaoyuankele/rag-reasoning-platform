"""Stable internal errors produced while parsing one document."""

from __future__ import annotations


# Retryability belongs to the error category, not to an individual parser.
# Keeping the policy in one table prevents PDF and DOCX implementations from
# returning contradictory retry decisions for the same stable error code.
ERROR_RETRYABILITY: dict[str, bool] = {
    "unsupported_format": False,
    "source_not_found": False,
    "source_access_denied": False,
    "password_required": False,
    "extraction_not_permitted": False,
    "ocr_required": False,
    "invalid_content": False,
    "resource_limit_exceeded": False,
    "parse_failed": False,
    "internal_error": True,
}


def retryable_for(code: str) -> bool:
    """Return the stable retry policy for one processing error code."""

    if code not in ERROR_RETRYABILITY:
        raise ValueError(f"unknown document processing error code: {code!r}")

    return ERROR_RETRYABILITY[code]


class DocumentProcessingError(Exception):
    """Expected document failure independent of the wire protocol."""

    def __init__(self, code: str, message: str) -> None:
        message = message.strip()
        if not message:
            raise ValueError("document processing error message must not be blank")

        super().__init__(message)
        self.code = code
        self.message = message
        self.retryable = retryable_for(code)
