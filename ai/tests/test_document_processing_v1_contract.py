from __future__ import annotations

import json
import os
from pathlib import Path
import subprocess
import sys
import tempfile
import unittest

from pypdf import PdfWriter

from tests.pdf_test_support import write_text_pdf

AI_ROOT = Path(__file__).resolve().parents[1]
SOURCE_ROOT = AI_ROOT / "src"
sys.path.insert(0, str(SOURCE_ROOT))

from rag_ai.contracts.document_processing_v1 import (  # noqa: E402
    CONTRACT_VERSION,
    ContractError,
    ProcessingChunk,
    parse_request,
    success_response,
)


def valid_payload(source_path: Path) -> dict[str, object]:
    return {
        "contract_version": "v1",
        "request_id": "job-123",
        "document": {
            "id": 42,
            "original_name": "research.pdf",
            "source_path": str(source_path),
            "mime_type": "application/pdf",
        },
        "options": {
            "max_chunk_characters": 1000,
            "max_pdf_file_bytes": 50 * 1024 * 1024,
            "max_pdf_pages": 500,
        },
    }


class ProcessingContractTests(unittest.TestCase):
    def test_parse_request_returns_typed_values(self) -> None:
        with tempfile.TemporaryDirectory() as directory:
            source_path = Path(directory) / "research.pdf"
            request = parse_request(valid_payload(source_path))

        self.assertEqual(request.contract_version, CONTRACT_VERSION)
        self.assertEqual(request.request_id, "job-123")
        self.assertEqual(request.document.id, 42)
        self.assertEqual(request.document.source_path, source_path)
        self.assertEqual(request.document.mime_type, "application/pdf")
        self.assertEqual(request.options.max_chunk_characters, 1000)
        self.assertEqual(request.options.max_pdf_file_bytes, 50 * 1024 * 1024)
        self.assertEqual(request.options.max_pdf_pages, 500)

    def test_parse_request_rejects_unknown_field(self) -> None:
        with tempfile.TemporaryDirectory() as directory:
            payload = valid_payload(Path(directory) / "research.pdf")
            payload["debug"] = True

            with self.assertRaises(ContractError) as raised:
                parse_request(payload)

        self.assertEqual(raised.exception.code, "invalid_request")

    def test_parse_request_rejects_boolean_document_id(self) -> None:
        with tempfile.TemporaryDirectory() as directory:
            payload = valid_payload(Path(directory) / "research.pdf")
            document = payload["document"]
            assert isinstance(document, dict)
            document["id"] = True

            with self.assertRaises(ContractError) as raised:
                parse_request(payload)

        self.assertEqual(raised.exception.code, "invalid_request")

    def test_parse_request_rejects_relative_source_path(self) -> None:
        payload = valid_payload(Path("documents/research.pdf"))

        with self.assertRaises(ContractError) as raised:
            parse_request(payload)

        self.assertEqual(raised.exception.code, "invalid_request")

    def test_parse_request_uses_defaults_for_optional_pdf_limits(self) -> None:
        with tempfile.TemporaryDirectory() as directory:
            payload = valid_payload(Path(directory) / "research.pdf")
            options = payload["options"]
            assert isinstance(options, dict)
            del options["max_pdf_file_bytes"]
            del options["max_pdf_pages"]

            request = parse_request(payload)

        self.assertEqual(request.options.max_pdf_file_bytes, 50 * 1024 * 1024)
        self.assertEqual(request.options.max_pdf_pages, 500)

    def test_success_response_preserves_optional_page_range(self) -> None:
        chunks = [
            ProcessingChunk(
                index=0,
                content="first page content",
                page_start=2,
                page_end=3,
            ),
            ProcessingChunk(
                index=1,
                content="text without fixed pages",
            ),
        ]

        response = success_response(
            "job-123",
            chunks,
            detected_title="Maglev control study",
        )

        self.assertEqual(response["contract_version"], "v1")
        self.assertEqual(response["request_id"], "job-123")
        self.assertEqual(response["status"], "succeeded")
        self.assertEqual(
            response["metadata"],
            {"title": "Maglev control study"},
        )
        self.assertEqual(response["chunks"][0]["page_start"], 2)
        self.assertEqual(response["chunks"][0]["page_end"], 3)
        self.assertNotIn("page_start", response["chunks"][1])
        self.assertNotIn("page_end", response["chunks"][1])

    def test_success_response_rejects_invalid_page_range(self) -> None:
        invalid_chunks = [
            ProcessingChunk(index=0, content="content", page_start=1),
            ProcessingChunk(index=0, content="content", page_end=1),
            ProcessingChunk(index=0, content="content", page_start=0, page_end=1),
            ProcessingChunk(index=0, content="content", page_start=3, page_end=2),
        ]

        for chunk in invalid_chunks:
            with self.subTest(chunk=chunk):
                with self.assertRaises(ContractError) as raised:
                    success_response("job-123", [chunk])

                self.assertEqual(raised.exception.code, "internal_error")
                self.assertTrue(raised.exception.retryable)

    def test_success_response_rejects_invalid_detected_title(self) -> None:
        chunk = ProcessingChunk(index=0, content="content")
        invalid_titles: list[object] = [
            "",
            " title ",
            "磁" * 501,
            42,
        ]

        for title in invalid_titles:
            with self.subTest(title=title):
                with self.assertRaises(ContractError) as raised:
                    success_response(
                        "job-123",
                        [chunk],
                        detected_title=title,  # type: ignore[arg-type]
                    )

                self.assertEqual(raised.exception.code, "internal_error")


class ProcessorCLITests(unittest.TestCase):
    def run_cli(
        self,
        input_text: str,
        *,
        arguments: tuple[str, ...] = (),
        python_io_encoding: str | None = None,
    ) -> subprocess.CompletedProcess[str]:
        """以明确 UTF-8 字节启动真实 CLI，并允许模拟错误系统代码页。"""

        environment = os.environ.copy()
        environment["PYTHONPATH"] = str(SOURCE_ROOT)
        if python_io_encoding is not None:
            environment["PYTHONIOENCODING"] = python_io_encoding
        return subprocess.run(
            [
                sys.executable,
                "-m",
                "rag_ai.entrypoints.document_processing_cli",
                *arguments,
            ],
            input=input_text,
            text=True,
            encoding="utf-8",
            capture_output=True,
            cwd=AI_ROOT,
            env=environment,
            check=False,
            timeout=10,
        )

    def test_cli_forces_utf8_for_unicode_request_and_response(self) -> None:
        """Windows 默认代码页不能破坏请求路径或响应中的 Unicode。"""

        with tempfile.TemporaryDirectory() as directory:
            payload = valid_payload(Path(directory) / "research.pdf")
            document = payload["document"]
            assert isinstance(document, dict)
            document["original_name"] = "文献∗.pdf"
            document["mime_type"] = "application/x-∗"

            completed = self.run_cli(
                json.dumps(payload, ensure_ascii=False),
                python_io_encoding="gbk",
            )

        self.assertEqual(completed.returncode, 0)
        self.assertEqual(completed.stderr, "")
        response = json.loads(completed.stdout)
        self.assertEqual(response["status"], "failed")
        self.assertEqual(response["error"]["code"], "unsupported_format")
        self.assertIn("∗", response["error"]["message"])

    def test_cli_returns_structured_unsupported_format_response(self) -> None:
        with tempfile.TemporaryDirectory() as directory:
            payload = valid_payload(Path(directory) / "research.pdf")
            document = payload["document"]
            assert isinstance(document, dict)
            document["mime_type"] = "application/vnd.openxmlformats-officedocument.wordprocessingml.document"
            completed = self.run_cli(json.dumps(payload))

        self.assertEqual(completed.returncode, 0)
        self.assertEqual(completed.stderr, "")
        self.assertEqual(len(completed.stdout.splitlines()), 1)

        response = json.loads(completed.stdout)
        self.assertEqual(response["contract_version"], "v1")
        self.assertEqual(response["request_id"], "job-123")
        self.assertEqual(response["status"], "failed")
        self.assertEqual(response["error"]["code"], "unsupported_format")
        self.assertFalse(response["error"]["retryable"])

    def test_cli_returns_structured_invalid_json_response(self) -> None:
        completed = self.run_cli("not-json")

        self.assertEqual(completed.returncode, 0)
        self.assertEqual(completed.stderr, "")
        response = json.loads(completed.stdout)
        self.assertEqual(response["request_id"], "invalid-request")
        self.assertEqual(response["status"], "failed")
        self.assertEqual(response["error"]["code"], "invalid_request")

    def test_stream_cli_processes_multiple_lines_after_invalid_request(self) -> None:
        """常驻模式必须逐行响应，并在坏请求后继续处理下一份文档。"""

        with tempfile.TemporaryDirectory() as directory:
            payload = valid_payload(Path(directory) / "research.docx")
            document = payload["document"]
            assert isinstance(document, dict)
            document["mime_type"] = (
                "application/vnd.openxmlformats-officedocument."
                "wordprocessingml.document"
            )
            input_text = "not-json\n" + json.dumps(payload) + "\n"

            completed = self.run_cli(
                input_text,
                arguments=("--stream",),
            )

        self.assertEqual(completed.returncode, 0)
        self.assertEqual(completed.stderr, "")
        response_lines = completed.stdout.splitlines()
        self.assertEqual(len(response_lines), 2)

        invalid_response = json.loads(response_lines[0])
        self.assertEqual(invalid_response["request_id"], "invalid-request")
        self.assertEqual(invalid_response["error"]["code"], "invalid_request")

        unsupported_response = json.loads(response_lines[1])
        self.assertEqual(unsupported_response["request_id"], "job-123")
        self.assertEqual(unsupported_response["error"]["code"], "unsupported_format")

    def test_cli_returns_invalid_content_for_fake_pdf(self) -> None:
        with tempfile.TemporaryDirectory() as directory:
            source_path = Path(directory) / "invalid.pdf"
            source_path.write_bytes(b"not a real PDF")

            payload = valid_payload(source_path)
            completed = self.run_cli(json.dumps(payload))

        self.assertEqual(completed.returncode, 0)
        self.assertEqual(completed.stderr, "")
        response = json.loads(completed.stdout)
        self.assertEqual(response["status"], "failed")
        self.assertEqual(response["error"]["code"], "invalid_content")
        self.assertFalse(response["error"]["retryable"])

    def test_cli_returns_ocr_required_for_blank_pdf(self) -> None:
        with tempfile.TemporaryDirectory() as directory:
            source_path = Path(directory) / "blank.pdf"
            writer = PdfWriter()
            writer.add_blank_page(width=612, height=792)

            with source_path.open("wb") as output:
                writer.write(output)

            payload = valid_payload(source_path)
            completed = self.run_cli(json.dumps(payload))

        self.assertEqual(completed.returncode, 0)
        self.assertEqual(completed.stderr, "")

        response = json.loads(completed.stdout)
        self.assertEqual(response["status"], "failed")
        self.assertEqual(response["error"]["code"], "ocr_required")
        self.assertFalse(response["error"]["retryable"])

    def test_cli_returns_page_sourced_chunks_for_text_pdf(self) -> None:
        with tempfile.TemporaryDirectory() as directory:
            source_path = Path(directory) / "text.pdf"
            write_text_pdf(
                source_path,
                ["one two three", "final page"],
            )

            payload = valid_payload(source_path)
            options = payload["options"]
            assert isinstance(options, dict)
            options["max_chunk_characters"] = 7
            completed = self.run_cli(json.dumps(payload))

        self.assertEqual(completed.returncode, 0)
        self.assertEqual(completed.stderr, "")

        response = json.loads(completed.stdout)
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
                    "page_start": 2,
                    "page_end": 2,
                },
                {
                    "index": 3,
                    "content": "page",
                    "page_start": 2,
                    "page_end": 2,
                },
            ],
        )


if __name__ == "__main__":
    unittest.main()
