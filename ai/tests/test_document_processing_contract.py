from __future__ import annotations

import json
import os
from pathlib import Path
import subprocess
import sys
import tempfile
import unittest

AI_ROOT = Path(__file__).resolve().parents[1]
SOURCE_ROOT = AI_ROOT / "src"
sys.path.insert(0, str(SOURCE_ROOT))

from rag_ai.document_processing_contract import (  # noqa: E402
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

        response = success_response("job-123", chunks)

        self.assertEqual(response["contract_version"], "v1")
        self.assertEqual(response["request_id"], "job-123")
        self.assertEqual(response["status"], "succeeded")
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


class ProcessorCLITests(unittest.TestCase):
    def run_cli(self, input_text: str) -> subprocess.CompletedProcess[str]:
        environment = os.environ.copy()
        environment["PYTHONPATH"] = str(SOURCE_ROOT)
        return subprocess.run(
            [sys.executable, "-m", "rag_ai.document_processor_cli"],
            input=input_text,
            text=True,
            capture_output=True,
            cwd=AI_ROOT,
            env=environment,
            check=False,
            timeout=10,
        )

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


if __name__ == "__main__":
    unittest.main()
