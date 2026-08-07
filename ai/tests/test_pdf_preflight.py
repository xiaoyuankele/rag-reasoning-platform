from __future__ import annotations

from pathlib import Path
import sys
import tempfile
import unittest

from pypdf import PdfWriter
from pypdf.constants import UserAccessPermissions

from tests.pdf_test_support import write_text_pdf


AI_ROOT = Path(__file__).resolve().parents[1]
SOURCE_ROOT = AI_ROOT / "src"
sys.path.insert(0, str(SOURCE_ROOT))

from rag_ai.parsing.errors import DocumentProcessingError  # noqa: E402
from rag_ai.parsing.pdf import (  # noqa: E402
    PDFPageText,
    PDFPreflightResult,
    _validate_page_count,
    extract_pdf_pages,
    preflight_pdf,
)


class PDFPreflightTests(unittest.TestCase):
    def test_preflight_returns_page_count_for_normal_pdf(self) -> None:
        with tempfile.TemporaryDirectory() as directory:
            source_path = Path(directory) / "normal.pdf"
            self.write_pdf(source_path, page_count=2)

            result = preflight_pdf(
                source_path,
                max_file_bytes=1024 * 1024,
                max_pages=10,
            )

        self.assertEqual(
            result,
            PDFPreflightResult(page_count=2, encrypted=False),
        )

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
            ``preflight_pdf`` 抛出的 ``DocumentProcessingError``。
        """

        with self.assertRaises(DocumentProcessingError) as raised:
            preflight_pdf(
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
    def test_extract_pdf_pages_preserves_page_numbers_and_text(self) -> None:
        with tempfile.TemporaryDirectory() as directory:
            source_path = Path(directory) / "two-pages.pdf"
            write_text_pdf(
                source_path,
                ["First page text", "Second page text"],
            )

            pages = extract_pdf_pages(
                source_path,
                max_file_bytes=1024 * 1024,
                max_pages=10,
            )

        self.assertEqual(
            pages,
            [
                PDFPageText(page_number=1, text="First page text"),
                PDFPageText(page_number=2, text="Second page text"),
            ],
        )

    def test_extract_pdf_pages_preserves_blank_page_as_empty_text(self) -> None:
        with tempfile.TemporaryDirectory() as directory:
            source_path = Path(directory) / "blank.pdf"
            write_text_pdf(source_path, [""])

            pages = extract_pdf_pages(
                source_path,
                max_file_bytes=1024 * 1024,
                max_pages=10,
            )

        self.assertEqual(
            pages,
            [PDFPageText(page_number=1, text="")],
        )



if __name__ == "__main__":
    unittest.main()
