"""Python 文档应用编排的单元测试。"""

from __future__ import annotations

from pathlib import Path
import sys
import unittest
from unittest.mock import patch


AI_ROOT = Path(__file__).resolve().parents[1]
SOURCE_ROOT = AI_ROOT / "src"
sys.path.insert(0, str(SOURCE_ROOT))

from rag_ai.document_processor import (  # noqa: E402
    build_pdf_chunks,
    prepare_pdf_pages,
    process_request,
    split_pdf_page_text,
)
from rag_ai.document_processing_contract import (  # noqa: E402
    CONTRACT_VERSION,
    ProcessingChunk,
    ProcessingDocument,
    ProcessingOptions,
    ProcessingRequest,
)
from rag_ai.parsing.errors import DocumentProcessingError  # noqa: E402
from rag_ai.parsing.pdf import PDFPageText  # noqa: E402


class PDFPagePreparationTests(unittest.TestCase):
    def test_prepare_pdf_pages_normalizes_text_and_preserves_blank_page(
        self,
    ) -> None:
        pages = [
            PDFPageText(
                page_number=1,
                text="  First\n\tpage\x00  content  ",
            ),
            PDFPageText(page_number=2, text=" \n\t "),
        ]

        normalized = prepare_pdf_pages(pages)

        self.assertEqual(
            normalized,
            [
                PDFPageText(page_number=1, text="First page content"),
                PDFPageText(page_number=2, text=""),
            ],
        )

    def test_prepare_pdf_pages_requires_ocr_when_every_page_is_blank(
        self,
    ) -> None:
        pages = [
            PDFPageText(page_number=1, text=""),
            PDFPageText(page_number=2, text=" \n\t\x00 "),
        ]

        with self.assertRaises(DocumentProcessingError) as raised:
            prepare_pdf_pages(pages)

        self.assertEqual(raised.exception.code, "ocr_required")
        self.assertFalse(raised.exception.retryable)


class PDFPageChunkSplittingTests(unittest.TestCase):
    def test_split_pdf_page_text_prefers_word_boundary(self) -> None:
        chunks = split_pdf_page_text(
            "one two three",
            max_chunk_characters=7,
        )

        self.assertEqual(chunks, ["one two", "three"])

    def test_split_pdf_page_text_hard_splits_oversized_word(self) -> None:
        chunks = split_pdf_page_text(
            "abcdefghij",
            max_chunk_characters=4,
        )

        self.assertEqual(chunks, ["abcd", "efgh", "ij"])

    def test_split_pdf_page_text_counts_unicode_characters(self) -> None:
        chunks = split_pdf_page_text(
            "你好世界测试",
            max_chunk_characters=4,
        )

        self.assertEqual(chunks, ["你好世界", "测试"])


class PDFChunkBuildingTests(unittest.TestCase):
    def test_build_pdf_chunks_preserves_global_indexes_and_page_numbers(
        self,
    ) -> None:
        pages = [
            PDFPageText(page_number=1, text="one two three"),
            PDFPageText(page_number=2, text=""),
            PDFPageText(page_number=3, text="abcdefghij"),
        ]

        chunks = build_pdf_chunks(
            pages,
            max_chunk_characters=7,
        )

        self.assertEqual(
            chunks,
            [
                ProcessingChunk(
                    index=0,
                    content="one two",
                    page_start=1,
                    page_end=1,
                ),
                ProcessingChunk(
                    index=1,
                    content="three",
                    page_start=1,
                    page_end=1,
                ),
                ProcessingChunk(
                    index=2,
                    content="abcdefg",
                    page_start=3,
                    page_end=3,
                ),
                ProcessingChunk(
                    index=3,
                    content="hij",
                    page_start=3,
                    page_end=3,
                ),
            ],
        )


class DocumentProcessorTests(unittest.TestCase):
    def test_process_request_returns_pdf_chunks_with_page_sources(self) -> None:
        request = ProcessingRequest(
            contract_version=CONTRACT_VERSION,
            request_id="request-123",
            document=ProcessingDocument(
                id=42,
                original_name="research.pdf",
                source_path=Path.cwd() / "research.pdf",
                mime_type="application/pdf",
            ),
            options=ProcessingOptions(
                max_chunk_characters=7,
                max_pdf_file_bytes=1024 * 1024,
                max_pdf_pages=10,
            ),
        )

        with patch(
            "rag_ai.document_processor.extract_pdf_pages",
            return_value=[
                PDFPageText(page_number=1, text="one two three"),
                PDFPageText(page_number=2, text=""),
                PDFPageText(page_number=3, text="final"),
            ],
        ):
            response = process_request(request)

        self.assertEqual(response["contract_version"], CONTRACT_VERSION)
        self.assertEqual(response["request_id"], "request-123")
        self.assertEqual(response["status"], "succeeded")
        self.assertEqual(
            response["chunks"],
            [
                {
                    "index": 0,
                    "content": "one two",
                    "page_start": 1,
                    "page_end": 1,
                },
                {
                    "index": 1,
                    "content": "three",
                    "page_start": 1,
                    "page_end": 1,
                },
                {
                    "index": 2,
                    "content": "final",
                    "page_start": 3,
                    "page_end": 3,
                },
            ],
        )


if __name__ == "__main__":
    unittest.main()
