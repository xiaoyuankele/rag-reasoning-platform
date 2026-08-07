"""Bounded PDF preflight checks before text extraction."""

from __future__ import annotations

from dataclasses import dataclass
from pathlib import Path

from pypdf import PasswordType, PdfReader
from pypdf.constants import UserAccessPermissions
from pypdf.errors import EmptyFileError, LimitReachedError, PdfReadError

from rag_ai.parsing.errors import DocumentProcessingError


PDF_HEADER_PREFIX = b"%PDF-"


@dataclass(frozen=True)
class PDFPreflightResult:
    """Safe metadata needed by the later extraction stage."""

    page_count: int
    encrypted: bool


def preflight_pdf(
    source_path: Path,
    *,
    max_file_bytes: int,
    max_pages: int,
) -> PDFPreflightResult:
    """Validate one trusted PDF path and return bounded metadata.

    Messages are intentionally safe: they do not contain the absolute source
    path, passwords, or document content because they can cross into Go logs.
    """

    _require_positive_limit(max_file_bytes, "max_file_bytes")
    _require_positive_limit(max_pages, "max_pages")

    try:
        source_size = source_path.stat().st_size
        if source_size > max_file_bytes:
            raise DocumentProcessingError(
                "resource_limit_exceeded",
                "PDF exceeds the processing file size limit",
            )

        with source_path.open("rb") as source:
            if source.read(len(PDF_HEADER_PREFIX)) != PDF_HEADER_PREFIX:
                raise DocumentProcessingError(
                    "invalid_content",
                    "source document is not a valid PDF",
                )
            source.seek(0)

            reader = PdfReader(source, strict=False)
            encrypted = reader.is_encrypted
            if encrypted:
                password_type = reader.decrypt("")
                if password_type == PasswordType.NOT_DECRYPTED:
                    raise DocumentProcessingError(
                        "password_required",
                        "PDF requires a password",
                    )
                _require_text_extraction_permission(reader)

            page_count = len(reader.pages)
            _validate_page_count(page_count, max_pages)

            return PDFPreflightResult(
                page_count=page_count,
                encrypted=encrypted,
            )

    except DocumentProcessingError:
        raise
    except FileNotFoundError as error:
        raise DocumentProcessingError(
            "source_not_found",
            "source document was not found",
        ) from error
    except PermissionError as error:
        raise DocumentProcessingError(
            "source_access_denied",
            "source document cannot be read",
        ) from error
    except IsADirectoryError as error:
        raise DocumentProcessingError(
            "invalid_content",
            "source document is not a regular PDF file",
        ) from error
    except LimitReachedError as error:
        raise DocumentProcessingError(
            "resource_limit_exceeded",
            "PDF parser reached a safety limit",
        ) from error
    except (EmptyFileError, PdfReadError, ValueError) as error:
        raise DocumentProcessingError(
            "invalid_content",
            "source document is not a valid PDF",
        ) from error
    except OSError as error:
        raise DocumentProcessingError(
            "source_access_denied",
            "source document cannot be read",
        ) from error


def _require_positive_limit(value: int, name: str) -> None:
    if isinstance(value, bool) or not isinstance(value, int) or value <= 0:
        raise ValueError(f"{name} must be a positive integer")


def _validate_page_count(page_count: int, max_pages: int) -> None:
    """Reject empty documents and PDFs beyond the configured page limit."""
    if page_count <= 0:
        raise DocumentProcessingError(
            "invalid_content",
            "PDF must contain at least one page",
        )

    if page_count > max_pages:
        raise DocumentProcessingError(
            "resource_limit_exceeded",
            "PDF exceeds the processing page limit",
        )


def _require_text_extraction_permission(reader: PdfReader) -> None:
    permissions = reader.user_access_permissions
    permissions_are_valid = reader.are_permissions_valid

    if permissions_are_valid is False or (
        permissions is not None
        and not permissions & UserAccessPermissions.EXTRACT
    ):
        raise DocumentProcessingError(
            "extraction_not_permitted",
            "PDF permissions do not allow text extraction",
        )
