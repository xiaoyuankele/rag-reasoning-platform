"""文档处理用例以及格式提取、规范化、分块的应用层编排。"""

from __future__ import annotations

from rag_ai.application.ports import (
    DocumentTitleExtractor,
    PageTextExtractor,
    TextSplitter,
)
from rag_ai.domain.errors import DocumentProcessingError
from rag_ai.domain.models import (
    DocumentSource,
    PageText,
    ProcessingLimits,
    ProcessingResult,
    TextChunk,
)


PDF_MIME_TYPE = "application/pdf"


class ProcessDocumentService:
    """通过应用层端口编排一份 PDF 的提取、规范化和分块。"""

    def __init__(
        self,
        page_extractor: PageTextExtractor,
        title_extractor: DocumentTitleExtractor,
        text_splitter: TextSplitter,
    ) -> None:
        """注入页面、标题提取器和文本分块器，不在应用层创建具体工具。"""

        self._page_extractor = page_extractor
        self._title_extractor = title_extractor
        self._text_splitter = text_splitter

    def process(
        self,
        source: DocumentSource,
        limits: ProcessingLimits,
    ) -> ProcessingResult:
        """处理一份文档并返回与 JSON、pypdf 和 LangChain 无关的结果。

        Args:
            source: 可信文档路径和 MIME 类型。
            limits: 当前任务的文件、页数和分块限制。

        Returns:
            按原文顺序排列、保留物理页码的 ``ProcessingResult``。

        Raises:
            DocumentProcessingError: 格式不受支持、需要 OCR 或具体提取器
                返回其他稳定文档处理错误。
        """

        if source.mime_type != PDF_MIME_TYPE:
            raise DocumentProcessingError(
                "unsupported_format",
                f"no Python processor is registered for {source.mime_type!r}",
            )

        extracted_pages = self._page_extractor.extract(
            source.source_path,
            max_file_bytes=limits.max_file_bytes,
            max_pages=limits.max_pages,
        )
        normalized_pages = prepare_pages(extracted_pages)
        chunks = build_chunks(
            normalized_pages,
            self._text_splitter,
            max_chunk_characters=limits.max_chunk_characters,
        )
        detected_title = self._title_extractor.extract_title(source.source_path)

        return ProcessingResult(
            chunks=chunks,
            detected_title=detected_title,
        )


def normalize_page_text(text: str) -> str:
    """把一页原始文字规范化为适合后续分块的单空格文本。

    Args:
        text: 页面提取器返回的一页原始文字。

    Returns:
        删除空字符，并把换行、制表符和连续空格折叠后的文字。

    Raises:
        TypeError: 页面文字不是字符串，说明提取器违反应用层端口约定。
    """

    if not isinstance(text, str):
        raise TypeError("page text must be a string")

    return " ".join(text.replace("\x00", "").split())


def prepare_pages(pages: list[PageText]) -> list[PageText]:
    """规范化逐页文字，并判断整份文档是否需要 OCR。

    Args:
        pages: 页面提取器按物理页顺序返回的原始结果。

    Returns:
        页码和顺序不变、文字已经规范化的页面列表；局部空白页仍然保留。

    Raises:
        DocumentProcessingError: 所有页面规范化后都没有文字。
    """

    normalized_pages = [
        PageText(
            page_number=page.page_number,
            text=normalize_page_text(page.text),
        )
        for page in pages
    ]

    if not any(page.text for page in normalized_pages):
        raise DocumentProcessingError(
            "ocr_required",
            "PDF contains no extractable text and requires OCR",
        )

    return normalized_pages


def build_chunks(
    pages: list[PageText],
    text_splitter: TextSplitter,
    *,
    max_chunk_characters: int,
) -> list[TextChunk]:
    """使用注入的分块器生成全局连续、带来源页码的文本块。

    Args:
        pages: 已规范化且保留物理页码的页面列表。
        text_splitter: 应用层只通过该端口请求分块能力。
        max_chunk_characters: 单个文本块允许的最大字符数。

    Returns:
        按页面和页内顺序排列的文本块；空白页不产生文本块。
    """

    chunks: list[TextChunk] = []
    for page in pages:
        page_contents = text_splitter.split(
            page.text,
            max_chunk_characters=max_chunk_characters,
        )

        for content in page_contents:
            chunks.append(
                TextChunk(
                    index=len(chunks),
                    content=content,
                    page_start=page.page_number,
                    page_end=page.page_number,
                )
            )

    return chunks
