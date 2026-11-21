from __future__ import annotations

from pathlib import Path
import sys
import tempfile
import unittest
from unittest.mock import patch

from pypdf import PdfWriter
from pypdf.constants import UserAccessPermissions

from tests.pdf_test_support import write_text_pdf


AI_ROOT = Path(__file__).resolve().parents[1]
SOURCE_ROOT = AI_ROOT / "src"
sys.path.insert(0, str(SOURCE_ROOT))

from rag_ai.domain.errors import DocumentProcessingError  # noqa: E402
from rag_ai.domain.models import ExtractedDocument, PageText  # noqa: E402
from rag_ai.infrastructure.parsing.pypdf_extractor import (  # noqa: E402
    PyPDFDocumentExtractor,
    _validate_page_count,
    extract_pdf_document,
    normalize_pdf_title,
)


class PDFPreflightTests(unittest.TestCase):
    def test_preflight_returns_page_count_for_normal_pdf(self) -> None:
        with tempfile.TemporaryDirectory() as directory:
            source_path = Path(directory) / "normal.pdf"
            self.write_pdf(source_path, page_count=2)

            result = PyPDFDocumentExtractor().extract(
                source_path,
                max_file_bytes=1024 * 1024,
                max_pages=10,
            )

        self.assertEqual(
            result.pages,
            [
                PageText(page_number=1, text=""),
                PageText(page_number=2, text=""),
            ],
        )
        self.assertIsNotNone(result.metrics)
        assert result.metrics is not None
        self.assertEqual(result.metrics.page_count, 2)
        self.assertIn(result.metrics.slowest_page_number, (1, 2))
        self.assertGreaterEqual(result.metrics.source_open_ms, 0)
        self.assertGreaterEqual(result.metrics.metadata_read_ms, 0)
        self.assertGreaterEqual(result.metrics.text_extract_ms, 0)
        self.assertGreaterEqual(result.metrics.slowest_page_ms, 0)

    def test_preflight_rejects_pdf_requiring_password(self) -> None:
        with tempfile.TemporaryDirectory() as directory:
            source_path = Path(directory) / "protected.pdf"
            self.write_pdf(source_path, password="secret")

            error = self.capture_processing_error(
                source_path,
                max_file_bytes=1024 * 1024,
                max_pages=10,
            )

        self.assertEqual(error.code, "password_required")
        self.assertFalse(error.retryable)

    def test_preflight_rejects_extraction_restricted_pdf(self) -> None:
        with tempfile.TemporaryDirectory() as directory:
            source_path = Path(directory) / "restricted.pdf"
            self.write_pdf(source_path, restrict_extraction=True)

            error = self.capture_processing_error(
                source_path,
                max_file_bytes=1024 * 1024,
                max_pages=10,
            )

        self.assertEqual(error.code, "extraction_not_permitted")

    def test_preflight_rejects_invalid_pdf(self) -> None:
        with tempfile.TemporaryDirectory() as directory:
            source_path = Path(directory) / "invalid.pdf"
            source_path.write_bytes(b"not a PDF")

            error = self.capture_processing_error(
                source_path,
                max_file_bytes=1024 * 1024,
                max_pages=10,
            )

        self.assertEqual(error.code, "invalid_content")

    def test_preflight_rejects_missing_source(self) -> None:
        with tempfile.TemporaryDirectory() as directory:
            source_path = Path(directory) / "missing.pdf"

            error = self.capture_processing_error(
                source_path,
                max_file_bytes=1024 * 1024,
                max_pages=10,
            )

        self.assertEqual(error.code, "source_not_found")

    def test_preflight_rejects_oversized_source(self) -> None:
        with tempfile.TemporaryDirectory() as directory:
            source_path = Path(directory) / "large.pdf"
            self.write_pdf(source_path)

            error = self.capture_processing_error(
                source_path,
                max_file_bytes=1,
                max_pages=10,
            )

        self.assertEqual(error.code, "resource_limit_exceeded")

    def test_validate_page_count_accepts_bounded_positive_value(self) -> None:
        self.assertIsNone(_validate_page_count(page_count=5, max_pages=10))

    def test_validate_page_count_rejects_empty_pdf(self) -> None:
        with self.assertRaises(DocumentProcessingError) as raised:
            _validate_page_count(page_count=0, max_pages=10)

        self.assertEqual(raised.exception.code, "invalid_content")

    def test_validate_page_count_rejects_limit_exceeded(self) -> None:
        with self.assertRaises(DocumentProcessingError) as raised:
            _validate_page_count(page_count=11, max_pages=10)

        self.assertEqual(raised.exception.code, "resource_limit_exceeded")

    def capture_processing_error(
        self,
        source_path: Path,
        *,
        max_file_bytes: int,
        max_pages: int,
    ) -> DocumentProcessingError:
        """执行 PDF 预检并返回测试期望捕获的处理异常。

        Args:
            source_path: 待预检测试 PDF 的路径。
            max_file_bytes: 测试使用的文件字节上限。
            max_pages: 测试使用的页数上限。

        Returns:
            统一 PDF 提取器抛出的 ``DocumentProcessingError``。
        """

        with self.assertRaises(DocumentProcessingError) as raised:
            PyPDFDocumentExtractor().extract(
                source_path,
                max_file_bytes=max_file_bytes,
                max_pages=max_pages,
            )
        return raised.exception

    def write_pdf(
        self,
        path: Path,
        *,
        page_count: int = 1,
        password: str | None = None,
        restrict_extraction: bool = False,
    ) -> None:
        """生成用于 PDF 预检测试的最小空白 PDF。

        Args:
            path: 测试 PDF 的输出路径。
            page_count: 需要生成的物理页数。
            password: 可选的打开密码；传入后生成需要密码的 PDF。
            restrict_extraction: 是否生成允许打开但禁止提取文字的 PDF。
        """

        writer = PdfWriter()
        for _ in range(page_count):
            writer.add_blank_page(width=612, height=792)

        if password is not None:
            writer.encrypt(password, algorithm="AES-256-R5")
        elif restrict_extraction:
            writer.encrypt(
                "",
                owner_password="owner",
                permissions_flag=UserAccessPermissions.PRINT,
                algorithm="AES-256-R5",
            )

        with path.open("wb") as output:
            writer.write(output)


class PDFTextExtractionTests(unittest.TestCase):
    def test_pypdf_extractor_satisfies_document_extraction_port(self) -> None:
        with tempfile.TemporaryDirectory() as directory:
            source_path = Path(directory) / "adapter.pdf"
            write_text_pdf(source_path, ["adapter page"])

            result = PyPDFDocumentExtractor().extract(
                source_path,
                max_file_bytes=1024 * 1024,
                max_pages=10,
            )

        self.assertEqual(
            result.pages,
            [PageText(page_number=1, text="adapter page")],
        )
        self.assertIsNotNone(result.metrics)

    def test_extract_pdf_document_preserves_page_numbers_and_text(self) -> None:
        with tempfile.TemporaryDirectory() as directory:
            source_path = Path(directory) / "two-pages.pdf"
            write_text_pdf(
                source_path,
                ["First page text", "Second page text"],
            )

            result = extract_pdf_document(
                source_path,
                max_file_bytes=1024 * 1024,
                max_pages=10,
            )

        self.assertEqual(
            result.pages,
            [
                PageText(page_number=1, text="First page text"),
                PageText(page_number=2, text="Second page text"),
            ],
        )

    def test_extract_pdf_document_preserves_blank_page_as_empty_text(self) -> None:
        with tempfile.TemporaryDirectory() as directory:
            source_path = Path(directory) / "blank.pdf"
            write_text_pdf(source_path, [""])

            result = extract_pdf_document(
                source_path,
                max_file_bytes=1024 * 1024,
                max_pages=10,
            )

        self.assertEqual(
            result.pages,
            [PageText(page_number=1, text="")],
        )

    def test_pypdf_document_extractor_opens_source_once(self) -> None:
        with tempfile.TemporaryDirectory() as directory:
            source_path = Path(directory) / "single-open.pdf"
            write_text_pdf(source_path, ["single open"])
            original_open = Path.open
            opened_paths: list[Path] = []

            def recording_open(
                path: Path,
                *args: object,
                **kwargs: object,
            ):  # type: ignore[no-untyped-def]
                opened_paths.append(path)
                return original_open(path, *args, **kwargs)

            with patch.object(Path, "open", recording_open):
                PyPDFDocumentExtractor().extract(
                    source_path,
                    max_file_bytes=1024 * 1024,
                    max_pages=10,
                )

        self.assertEqual(opened_paths, [source_path])


class PDFTitleExtractionTests(unittest.TestCase):
    def test_normalize_pdf_title(self) -> None:
        tests = [
            ("  Maglev\n  control study  ", "Maglev control study"),
            ("磁浮列车协同控制", "磁浮列车协同控制"),
            (None, None),
            (42, None),
            (" \n\t ", None),
            ("a" * 501, None),
        ]

        for raw_title, expected in tests:
            with self.subTest(raw_title=raw_title):
                self.assertEqual(normalize_pdf_title(raw_title), expected)

    def test_pypdf_title_extractor_returns_metadata_title(self) -> None:
        with tempfile.TemporaryDirectory() as directory:
            source_path = Path(directory) / "title.pdf"
            writer = PdfWriter()
            writer.add_blank_page(width=612, height=792)
            writer.add_metadata({"/Title": "  Maglev\n control study  "})
            with source_path.open("wb") as output:
                writer.write(output)

            title = PyPDFDocumentExtractor().extract(
                source_path,
                max_file_bytes=1024 * 1024,
                max_pages=10,
            ).detected_title

        self.assertEqual(title, "Maglev control study")

    def test_pypdf_title_extractor_returns_none_without_metadata_title(self) -> None:
        with tempfile.TemporaryDirectory() as directory:
            source_path = Path(directory) / "untitled.pdf"
            writer = PdfWriter()
            writer.add_blank_page(width=612, height=792)
            with source_path.open("wb") as output:
                writer.write(output)

            title = PyPDFDocumentExtractor().extract(
                source_path,
                max_file_bytes=1024 * 1024,
                max_pages=10,
            ).detected_title

        self.assertIsNone(title)



if __name__ == "__main__":
    unittest.main()
