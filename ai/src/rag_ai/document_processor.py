"""Application-level document format dispatch for one validated request."""

from __future__ import annotations

from typing import Any

from rag_ai.document_processing_contract import ProcessingRequest
from rag_ai.parsing.errors import DocumentProcessingError
from rag_ai.parsing.pdf import preflight_pdf


PDF_MIME_TYPE = "application/pdf"


def process_request(request: ProcessingRequest) -> dict[str, Any]:
    """Dispatch one validated request to its format-specific processor."""

    if request.document.mime_type != PDF_MIME_TYPE:
        raise DocumentProcessingError(
            "unsupported_format",
            f"no Python processor is registered for {request.document.mime_type!r}",
        )

    preflight_pdf(
        request.document.source_path,
        max_file_bytes=request.options.max_pdf_file_bytes,
        max_pages=request.options.max_pdf_pages,
    )

    # PDF-2 stops after preflight. PDF-3 will replace this temporary failure
    # with page extraction and success_response(...).
    raise DocumentProcessingError(
        "parse_failed",
        "PDF passed preflight but text extraction is not implemented",
    )
