"""使用一个 pypdf Reader 完成 PDF 安全校验、标题和正文提取。"""

from __future__ import annotations

from pathlib import Path

from pypdf import PasswordType, PdfReader
from pypdf.constants import UserAccessPermissions
from pypdf.errors import EmptyFileError, LimitReachedError, PdfReadError

from rag_ai.domain.errors import DocumentProcessingError
from rag_ai.domain.models import ExtractedDocument, PageText


PDF_HEADER_PREFIX = b"%PDF-"
MAX_DETECTED_TITLE_CHARACTERS = 500


class PyPDFDocumentExtractor:
    """通过 pypdf 实现应用层统一 DocumentExtractor 端口。"""

    def extract(
        self,
        source_path: Path,
        *,
        max_file_bytes: int,
        max_pages: int,
    ) -> ExtractedDocument:
        """一次打开 PDF，完成安全校验、标题读取和逐页文字提取。

        Args:
            source_path: Go 文件存储层解析出的可信 PDF 绝对路径。
            max_file_bytes: 本次任务允许处理的最大文件字节数。
            max_pages: 本次任务允许处理的最大物理页数。

        Returns:
            与 pypdf 解耦的页面文字和可选标题。文件句柄与 PdfReader
            都会在本方法返回前释放。

        Raises:
            ValueError: 资源限制不是正整数。
            DocumentProcessingError: 文件不可读、损坏、需要密码、禁止提取、
                超过资源限制，或者页面文字提取失败。
        """

        return extract_pdf_document(
            source_path,
            max_file_bytes=max_file_bytes,
            max_pages=max_pages,
        )


def extract_pdf_document(
    source_path: Path,
    *,
    max_file_bytes: int,
    max_pages: int,
) -> ExtractedDocument:
    """使用一个文件句柄和一个 PdfReader 提取完整 PDF 中间结果。

    安全预检仍然先于正文读取，但不再为了阶段分工重复打开文件。标题属于
    可选增强信息，损坏或缺失不会让正文处理失败。
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
            if reader.is_encrypted:
                password_type = reader.decrypt("")
                if password_type == PasswordType.NOT_DECRYPTED:
                    raise DocumentProcessingError(
                        "password_required",
                        "PDF requires a password",
                    )
                _require_text_extraction_permission(reader)

            page_count = len(reader.pages)
            _validate_page_count(page_count, max_pages)
            detected_title = _extract_optional_title(reader)

            pages: list[PageText] = []
            for page_number, page in enumerate(reader.pages, start=1):
                try:
                    text = page.extract_text()
                except (KeyError, TypeError, ValueError, PdfReadError) as error:
                    raise DocumentProcessingError(
                        "parse_failed",
                        f"PDF page {page_number} text extraction failed",
                    ) from error

                pages.append(
                    PageText(
                        page_number=page_number,
                        text=text or "",
                    )
                )

            return ExtractedDocument(
                pages=pages,
                detected_title=detected_title,
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


def normalize_pdf_title(value: object) -> str | None:
    """把不可信 PDF 元数据值转换成可落库的可选标题。

    Args:
        value: pypdf 元数据中读取出的不可信标题值。

    Returns:
        已折叠连续空白且不超过长度上限的标题；无可靠标题时返回 None。
    """

    if not isinstance(value, str):
        return None

    normalized = " ".join(value.split())

    if not normalized:
        return None

    if len(normalized) > MAX_DETECTED_TITLE_CHARACTERS:
        return None

    return normalized


def _extract_optional_title(reader: PdfReader) -> str | None:
    """从已经完成安全校验的 Reader 中尽力读取可选标题。"""

    try:
        metadata = reader.metadata
        raw_title = metadata.title if metadata is not None else None
        return normalize_pdf_title(raw_title)
    except (
        LimitReachedError,
        PdfReadError,
        AttributeError,
        KeyError,
        OSError,
        TypeError,
        ValueError,
    ):
        # 标题是可选增强字段，元数据损坏不能让正文处理失败。
        return None


def _require_positive_limit(value: int, name: str) -> None:
    """校验内部资源限制必须是正整数，布尔值不能冒充整数。"""

    if isinstance(value, bool) or not isinstance(value, int) or value <= 0:
        raise ValueError(f"{name} must be a positive integer")


def _validate_page_count(page_count: int, max_pages: int) -> None:
    """拒绝零页 PDF 以及超过任务页数限制的 PDF。"""

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
    """确认已打开的加密 PDF 明确允许提取文字。"""

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
