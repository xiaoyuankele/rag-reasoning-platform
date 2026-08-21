"""应用层处理文档时依赖的最小能力接口。"""

from __future__ import annotations

from pathlib import Path
from typing import Protocol

from rag_ai.domain.models import (
    DocumentSource,
    ExtractedDocument,
    ProcessingLimits,
    ProcessingResult,
)


class DocumentExtractor(Protocol):
    """一次读取源文档并提取 Application 所需内容的应用层端口。"""

    def extract(
        self,
        source_path: Path,
        *,
        max_file_bytes: int,
        max_pages: int,
    ) -> ExtractedDocument:
        """一次返回页面文字和可选标题，并遵守文件与页数限制。"""


class TextSplitter(Protocol):
    """把一段规范化文字切成有稳定顺序的小块。"""

    def split(
        self,
        text: str,
        *,
        max_chunk_characters: int,
    ) -> list[str]:
        """返回不超过字符上限的非空文本块。"""


class DocumentProcessingUseCase(Protocol):
    """对外提供单份文档处理能力的应用层入站端口。"""

    def process(
        self,
        source: DocumentSource,
        limits: ProcessingLimits,
    ) -> ProcessingResult:
        """处理文档并返回统一领域结果。"""
