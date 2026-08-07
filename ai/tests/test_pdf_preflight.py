from __future__ import annotations

from pathlib import Path
import sys
import tempfile
import unittest

from pypdf import PdfWriter
from pypdf.constants import UserAccessPermissions


AI_ROOT = Path(__file__).resolve().parents[1]
SOURCE_ROOT = AI_ROOT / "src"
sys.path.insert(0, str(SOURCE_ROOT))

from rag_ai.parsing.errors import DocumentProcessingError  # noqa: E402
from rag_ai.parsing.pdf import (  # noqa: E402
    PDFPreflightResult,
    _validate_page_count,
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


if __name__ == "__main__":
    unittest.main()
