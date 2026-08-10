"""分层后 Python 文档处理 Application Service 的单元测试。"""

from __future__ import annotations

from pathlib import Path
import sys
import unittest


AI_ROOT = Path(__file__).resolve().parents[1]
SOURCE_ROOT = AI_ROOT / "src"
sys.path.insert(0, str(SOURCE_ROOT))

from rag_ai.application.document_processor import (  # noqa: E402
    ProcessDocumentService,
)
from rag_ai.domain.errors import DocumentProcessingError  # noqa: E402
from rag_ai.domain.models import (  # noqa: E402
    DocumentSource,
    PageText,
    ProcessingLimits,
    ProcessingResult,
    TextChunk,
)


class RecordingPageExtractor:
    """返回固定页面并记录调用参数的测试 Fake。"""

    def __init__(self, pages: list[PageText]) -> None:
        self.pages = pages
        self.calls: list[tuple[Path, int, int]] = []

    def extract(
        self,
        source_path: Path,
        *,
        max_file_bytes: int,
        max_pages: int,
    ) -> list[PageText]:
        """记录应用服务传入的来源和限制后返回预设页面。"""

        self.calls.append((source_path, max_file_bytes, max_pages))
        return self.pages


class RecordingTitleExtractor:
    """返回固定标题并记录被读取的文档路径。"""

    def __init__(self, title: str | None) -> None:
        self.title = title
        self.calls: list[Path] = []

    def extract_title(self, source_path: Path) -> str | None:
        """记录来源路径并返回预设的可选标题。"""

        self.calls.append(source_path)
        return self.title


class RecordingTextSplitter:
    """按空格模拟分块并记录调用参数的测试 Fake。"""

    def __init__(self) -> None:
        self.calls: list[tuple[str, int]] = []

    def split(
        self,
        text: str,
        *,
        max_chunk_characters: int,
    ) -> list[str]:
        """记录调用；空白页返回空列表，其他文字按空格切开。"""

        self.calls.append((text, max_chunk_characters))
        return text.split()


class ProcessDocumentServiceTests(unittest.TestCase):
    def test_process_uses_ports_and_returns_page_sourced_chunks(self) -> None:
        source_path = Path.cwd() / "research.pdf"
        extractor = RecordingPageExtractor(
            [
                PageText(page_number=1, text="  first\npage  "),
                PageText(page_number=2, text=""),
                PageText(page_number=3, text="final"),
            ]
        )
        title_extractor = RecordingTitleExtractor("Maglev research")
        splitter = RecordingTextSplitter()
        service = ProcessDocumentService(extractor, title_extractor, splitter)

        result = service.process(
            DocumentSource(
                source_path=source_path,
                mime_type="application/pdf",
            ),
            ProcessingLimits(
                max_chunk_characters=100,
                max_file_bytes=1024 * 1024,
                max_pages=10,
            ),
        )

        self.assertEqual(
            extractor.calls,
            [(source_path, 1024 * 1024, 10)],
        )
        self.assertEqual(
            splitter.calls,
            [
                ("first page", 100),
                ("", 100),
                ("final", 100),
            ],
        )
        self.assertEqual(
            result,
            ProcessingResult(
                chunks=[
                    TextChunk(0, "first", 1, 1),
                    TextChunk(1, "page", 1, 1),
                    TextChunk(2, "final", 3, 3),
                ],
                detected_title="Maglev research",
            ),
        )
        self.assertEqual(title_extractor.calls, [source_path])

    def test_process_rejects_unsupported_format_before_using_ports(self) -> None:
        extractor = RecordingPageExtractor([])
        title_extractor = RecordingTitleExtractor(None)
        splitter = RecordingTextSplitter()
        service = ProcessDocumentService(extractor, title_extractor, splitter)

        with self.assertRaises(DocumentProcessingError) as raised:
            service.process(
                DocumentSource(
                    source_path=Path.cwd() / "notes.docx",
                    mime_type=(
                        "application/vnd.openxmlformats-officedocument."
                        "wordprocessingml.document"
                    ),
                ),
                ProcessingLimits(100, 1024, 10),
            )

        self.assertEqual(raised.exception.code, "unsupported_format")
        self.assertEqual(extractor.calls, [])
        self.assertEqual(title_extractor.calls, [])
        self.assertEqual(splitter.calls, [])

    def test_process_requires_ocr_when_every_page_is_blank(self) -> None:
        extractor = RecordingPageExtractor(
            [
                PageText(page_number=1, text=" \n\t "),
                PageText(page_number=2, text="\x00"),
            ]
        )
        title_extractor = RecordingTitleExtractor(None)
        splitter = RecordingTextSplitter()
        service = ProcessDocumentService(extractor, title_extractor, splitter)

        with self.assertRaises(DocumentProcessingError) as raised:
            service.process(
                DocumentSource(
                    source_path=Path.cwd() / "scanned.pdf",
                    mime_type="application/pdf",
                ),
                ProcessingLimits(100, 1024, 10),
            )

        self.assertEqual(raised.exception.code, "ocr_required")
        self.assertEqual(title_extractor.calls, [])
        self.assertEqual(splitter.calls, [])


if __name__ == "__main__":
    unittest.main()
