"""使用 pypdf 完成 PDF 安全预检与逐页文字提取。"""

from __future__ import annotations

from dataclasses import dataclass
from pathlib import Path

from pypdf import PasswordType, PdfReader
from pypdf.constants import UserAccessPermissions
from pypdf.errors import EmptyFileError, LimitReachedError, PdfReadError

from rag_ai.domain.errors import DocumentProcessingError
from rag_ai.domain.models import PageText


PDF_HEADER_PREFIX = b"%PDF-"


@dataclass(frozen=True)
class PDFPreflightResult:
    """PDF 通过预检后交给文字提取阶段使用的安全元数据。

    Attributes:
        page_count: PDF 的物理页数。
        encrypted: PDF 是否带有加密标记；能通过预检的加密 PDF 只能
            使用空密码打开，并且必须允许提取文字。
    """

    page_count: int
    encrypted: bool


class PyPDFPageExtractor:
    """通过 pypdf 实现应用层 ``PageTextExtractor`` 端口。"""

    def extract(
        self,
        source_path: Path,
        *,
        max_file_bytes: int,
        max_pages: int,
    ) -> list[PageText]:
        """安全预检 PDF，逐页提取原始文字并保留物理页码。

        Args:
            source_path: Go 文件存储层解析出的可信 PDF 绝对路径。
            max_file_bytes: 本次任务允许处理的最大文件字节数。
            max_pages: 本次任务允许处理的最大物理页数。

        Returns:
            按物理页顺序排列的 ``PageText``；空白页仍以空字符串保留。

        Raises:
            ValueError: 资源限制不是正整数。
            DocumentProcessingError: 文件不可读、损坏、需要密码、禁止提取、
                超过资源限制，或者页面文字提取失败。
        """

        return extract_pdf_pages(
            source_path,
            max_file_bytes=max_file_bytes,
            max_pages=max_pages,
        )


def preflight_pdf(
    source_path: Path,
    *,
    max_file_bytes: int,
    max_pages: int,
) -> PDFPreflightResult:
    """在提取正文前检查 PDF 是否安全且适合当前处理策略。

    Args:
        source_path: Go 文件存储层解析出的可信 PDF 绝对路径。
        max_file_bytes: 当前任务允许处理的最大 PDF 文件字节数。
        max_pages: 当前任务允许处理的最大 PDF 页数。

    Returns:
        包含页数和加密状态的 ``PDFPreflightResult``。

    Raises:
        ValueError: 文件或页数限制不是正整数。
        DocumentProcessingError: 文件不存在、不可读、损坏、需要密码、禁止
            提取，或者超过资源限制。错误消息不会包含绝对路径或正文。
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


def extract_pdf_pages(
    source_path: Path,
    *,
    max_file_bytes: int,
    max_pages: int,
) -> list[PageText]:
    """预检 PDF 后逐页提取原始文字，并保留物理页边界。

    Args:
        source_path: Go 文件存储层解析出的可信 PDF 绝对路径。
        max_file_bytes: 当前任务允许处理的最大 PDF 文件字节数。
        max_pages: 当前任务允许处理的最大 PDF 页数。

    Returns:
        按物理页顺序排列的 ``PageText``。空白页仍然保留，文字为空字符串。

    Raises:
        ValueError: 文件或页数限制不是正整数。
        DocumentProcessingError: 预检失败、文件在处理期间不可读，或者某页
            文字提取失败。
    """

    preflight = preflight_pdf(
        source_path,
        max_file_bytes=max_file_bytes,
        max_pages=max_pages,
    )

    try:
        with source_path.open("rb") as source:
            reader = PdfReader(source, strict=False)
            if reader.is_encrypted:
                reader.decrypt("")

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

            if len(pages) != preflight.page_count:
                raise DocumentProcessingError(
                    "parse_failed",
                    "PDF page count changed during text extraction",
                )

            return pages

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
    except LimitReachedError as error:
        raise DocumentProcessingError(
            "resource_limit_exceeded",
            "PDF parser reached a safety limit",
        ) from error
    except (EmptyFileError, PdfReadError, ValueError) as error:
        raise DocumentProcessingError(
            "parse_failed",
            "PDF text extraction failed",
        ) from error
    except OSError as error:
        raise DocumentProcessingError(
            "source_access_denied",
            "source document cannot be read",
        ) from error


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
    """确认已打开的加密 PDF 明确允许提取文字。

    Args:
        reader: 已使用空密码成功解密的 pypdf ``PdfReader``。

    Raises:
        DocumentProcessingError: PDF 权限无效或禁止提取文字。
    """

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
