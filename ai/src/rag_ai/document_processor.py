"""对一条已校验请求执行文档格式分发和应用层编排。"""

from __future__ import annotations

from typing import Any

from rag_ai.document_processing_contract import (
    ProcessingChunk,
    ProcessingRequest,
    success_response,
)
from rag_ai.parsing.errors import DocumentProcessingError
from rag_ai.parsing.pdf import (
    PDFPageText,
    extract_pdf_pages,
    normalize_pdf_page_text,
)


PDF_MIME_TYPE = "application/pdf"


def process_request(request: ProcessingRequest) -> dict[str, Any]:
    """把已校验请求分发给对应格式处理器并构造协议响应。

    Args:
        request: 契约层已经完成字段和类型校验的 ``ProcessingRequest``。

    Returns:
        可以直接由 CLI 序列化到 stdout 的 v1 成功响应字典。

    Raises:
        DocumentProcessingError: MIME 类型不受支持，或者格式处理器返回可预期
            的文档失败，例如需要密码、禁止提取、需要 OCR 或解析失败。
    """

    if request.document.mime_type != PDF_MIME_TYPE:
        raise DocumentProcessingError(
            "unsupported_format",
            f"no Python processor is registered for {request.document.mime_type!r}",
        )

    extracted_pages = extract_pdf_pages(
        request.document.source_path,
        max_file_bytes=request.options.max_pdf_file_bytes,
        max_pages=request.options.max_pdf_pages,
    )
    normalized_pages = prepare_pdf_pages(extracted_pages)
    chunks = build_pdf_chunks(
        normalized_pages,
        request.options.max_chunk_characters,
    )

    return success_response(request.request_id, chunks)


def prepare_pdf_pages(pages: list[PDFPageText]) -> list[PDFPageText]:
    """规范化逐页文字，并执行“整份文档是否需要 OCR”的应用策略。

    Args:
        pages: PDF 提取层按物理页顺序返回的原始页面文字。

    Returns:
        页码和顺序不变、文字已经完成基础空白规范化的页面列表。局部空白页
        仍然保留，供后续分块阶段选择跳过。

    Raises:
        DocumentProcessingError: 所有页面规范化后都没有文字，需要进入尚未
            实现的 OCR 流程。
    """

    normalized_pages = [
        PDFPageText(
            page_number=page.page_number,
            text=normalize_pdf_page_text(page.text),
        )
        for page in pages
    ]

    if not any(page.text for page in normalized_pages):
        raise DocumentProcessingError(
            "ocr_required",
            "PDF contains no extractable text and requires OCR",
        )

    return normalized_pages


def split_pdf_page_text(
    text: str,
    max_chunk_characters: int,
) -> list[str]:
    """在单个物理页内按字符上限切分文字，并优先使用空格边界。

    Args:
        text: 已由 ``prepare_pdf_pages`` 完成空白规范化的一页文字。
        max_chunk_characters: 每个文本块允许包含的最大 Unicode 字符数。

    Returns:
        顺序稳定且每项不超过上限的字符串列表。空白页返回空列表；当一个
        连续词本身超过上限时，允许从字符边界硬切以保证资源限制。

    Raises:
        TypeError: ``text`` 不是字符串。
        ValueError: ``max_chunk_characters`` 不是正整数。
    """

    if not isinstance(text, str):
        raise TypeError("PDF page text must be a string")
    if (
        isinstance(max_chunk_characters, bool)
        or not isinstance(max_chunk_characters, int)
        or max_chunk_characters <= 0
    ):
        raise ValueError("max_chunk_characters must be a positive integer")

    remaining = text.strip()
    chunks: list[str] = []

    while len(remaining) > max_chunk_characters:
        split_at = remaining.rfind(" ", 0, max_chunk_characters + 1)
        if split_at <= 0:
            split_at = max_chunk_characters

        chunks.append(remaining[:split_at].rstrip())
        remaining = remaining[split_at:].lstrip()

    if remaining:
        chunks.append(remaining)

    return chunks


def build_pdf_chunks(
    pages: list[PDFPageText],
    max_chunk_characters: int,
) -> list[ProcessingChunk]:
    """把规范化 PDF 页面组装成全局连续、带来源页码的文本块。

    Args:
        pages: ``prepare_pdf_pages`` 返回的规范化页面列表。
        max_chunk_characters: 每个文本块允许包含的最大 Unicode 字符数。

    Returns:
        按页面和页内文字顺序排列的 ``ProcessingChunk`` 列表。空白页不产生
        文本块；第一版不跨页，因此每个块的起止页码相同。

    Raises:
        TypeError: 页面文字不是字符串。
        ValueError: 分块字符上限不是正整数。
    """

    chunks: list[ProcessingChunk] = []
    for page in pages:
        page_contents = split_pdf_page_text(
            page.text,
            max_chunk_characters,
        )

        for content in page_contents:
            chunk = ProcessingChunk(
                index=len(chunks),
                content=content,
                page_start=page.page_number,
                page_end=page.page_number,
            )
            chunks.append(chunk)

    return chunks
