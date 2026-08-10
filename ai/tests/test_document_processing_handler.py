"""v1 文档处理边界适配器的单元测试。"""

from __future__ import annotations

from pathlib import Path
import unittest

from rag_ai.contracts.document_processing_v1 import (
    CONTRACT_VERSION,
    ProcessingDocument,
    ProcessingOptions,
    ProcessingRequest,
)
from rag_ai.domain.models import (
    DocumentSource,
    ProcessingLimits,
    ProcessingResult,
    TextChunk,
)
from rag_ai.entrypoints.document_processing_handler import process_request


class RecordingProcessDocumentService:
    """记录边界适配器传入参数并返回预设领域结果的测试 Fake。"""

    def __init__(self, result: ProcessingResult) -> None:
        self._result = result
        self.sources: list[DocumentSource] = []
        self.limits: list[ProcessingLimits] = []

    def process(
        self,
        source: DocumentSource,
        limits: ProcessingLimits,
    ) -> ProcessingResult:
        """记录一次应用调用，避免单元测试启动真实 pypdf。"""

        self.sources.append(source)
        self.limits.append(limits)
        return self._result


class DocumentProcessorBoundaryTests(unittest.TestCase):
    """验证契约 DTO、领域对象和响应 DTO 之间的转换。"""

    def test_process_request_maps_contract_to_application_and_back(self) -> None:
        source_path = Path.cwd() / "research.pdf"
        request = ProcessingRequest(
            contract_version=CONTRACT_VERSION,
            request_id="request-123",
            document=ProcessingDocument(
                id=42,
                original_name="research.pdf",
                source_path=source_path,
                mime_type="application/pdf",
            ),
            options=ProcessingOptions(
                max_chunk_characters=7,
                max_pdf_file_bytes=1024 * 1024,
                max_pdf_pages=10,
            ),
        )
        service = RecordingProcessDocumentService(
            ProcessingResult(
                chunks=[
                    TextChunk(
                        index=0,
                        content="one two",
                        page_start=1,
                        page_end=1,
                    ),
                    TextChunk(
                        index=1,
                        content="three",
                        page_start=1,
                        page_end=1,
                    ),
                ],
                detected_title="Maglev research",
            )
        )

        response = process_request(request, service)

        self.assertEqual(
            service.sources,
            [
                DocumentSource(
                    source_path=source_path,
                    mime_type="application/pdf",
                )
            ],
        )
        self.assertEqual(
            service.limits,
            [
                ProcessingLimits(
                    max_chunk_characters=7,
                    max_file_bytes=1024 * 1024,
                    max_pages=10,
                )
            ],
        )
        self.assertEqual(
            response,
            {
                "contract_version": CONTRACT_VERSION,
                "request_id": "request-123",
                "status": "succeeded",
                "metadata": {"title": "Maglev research"},
                "chunks": [
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
                ],
            },
        )


if __name__ == "__main__":
    unittest.main()
